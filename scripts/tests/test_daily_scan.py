import importlib.util
import os
from pathlib import Path
import subprocess
import unittest
from datetime import datetime
from zoneinfo import ZoneInfo

spec = importlib.util.spec_from_file_location('daily_scan', Path(__file__).parents[1] / 'daily-scan.py')
daily = importlib.util.module_from_spec(spec)
spec.loader.exec_module(daily)


class NightlyScanTests(unittest.TestCase):
    def test_cutoff_uses_most_recent_beijing_two_am(self):
        for hour, day in [(1, 29), (2, 30), (9, 30)]:
            now = datetime(2026, 10, 30, hour, 0, tzinfo=ZoneInfo('Asia/Shanghai'))
            self.assertEqual(daily.nightly_cutoff(now), datetime(2026, 10, day, 2, tzinfo=now.tzinfo))

    def test_database_url_is_passed_as_separate_environment_values(self):
        env = daily.postgres_environment('postgresql://scan:p%40ss%3F@127.0.0.1:5432/ipguard?sslmode=disable')
        self.assertEqual([env[k] for k in ['PGUSER', 'PGPASSWORD', 'PGHOST', 'PGPORT', 'PGDATABASE', 'PGSSLMODE']],
                         ['scan', 'p@ss?', '127.0.0.1', '5432', 'ipguard', 'disable'])

    @unittest.skipUnless(os.environ.get('TEST_DATABASE_URL'), 'requires PostgreSQL for temporary-table integration test')
    def test_all_ranges_and_unfinished_work(self):
        # Temporary tables shadow application tables; all changes roll back.
        fixture = """
BEGIN;
CREATE TEMP TABLE networks(id uuid PRIMARY KEY,cidr cidr,enabled boolean);
CREATE TEMP TABLE cf_targets(id integer);
CREATE TEMP TABLE network_auto_scans(
 network_id uuid,cidr cidr,protocol text,user_id uuid,
 recheck_after timestamptz,created_at timestamptz DEFAULT now(),
 PRIMARY KEY(network_id,protocol));
INSERT INTO networks SELECT md5(i::text)::uuid,'198.18.0.0/16',true FROM generate_series(1,127) i;
INSERT INTO networks VALUES(md5('disabled')::uuid,'203.0.113.0/24',false);
INSERT INTO cf_targets VALUES(1);
"""
        queue = daily.QUEUE_SQL.replace('BEGIN;', '')
        verify_first = """
DO $$ BEGIN
 IF (SELECT count(*) FROM network_auto_scans)<>254 THEN RAISE EXCEPTION 'Missing enabled networks or pools'; END IF;
END $$;
-- Mark yesterday's full recheck and an ordinary incremental queue entry.
UPDATE network_auto_scans SET recheck_after='2026-10-29 02:00+08' WHERE network_id=md5('1')::uuid;
UPDATE network_auto_scans SET recheck_after=NULL WHERE network_id=md5('2')::uuid;
"""
        verify_second = """
DO $$ BEGIN
 IF (SELECT count(*) FROM network_auto_scans)<>254 THEN RAISE EXCEPTION 'Repeated queue added duplicates'; END IF;
 IF (SELECT count(*) FROM network_auto_scans WHERE recheck_after='2026-10-29 02:00+08')<>2 THEN RAISE EXCEPTION 'Unfinished full work reset'; END IF;
 IF EXISTS(SELECT 1 FROM network_auto_scans WHERE recheck_after IS NULL) THEN RAISE EXCEPTION 'Incremental queue not upgraded'; END IF;
END $$;
TRUNCATE network_auto_scans;
"""
        verify_nodes = """
DO $$ BEGIN
 IF (SELECT count(*) FROM network_auto_scans)<>127 OR EXISTS(SELECT 1 FROM network_auto_scans WHERE protocol<>'grpc') THEN
 RAISE EXCEPTION 'Node-only repair touched CF'; END IF;
END $$;
ROLLBACK;
"""
        sql = fixture + queue + verify_first + queue + verify_second + '\n\\set nodes_only true\n' + queue + verify_nodes
        env = daily.postgres_environment(os.environ['TEST_DATABASE_URL'])
        result = subprocess.run(['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1',
                                 '-v', 'user_id=00000000-0000-0000-0000-000000000001',
                                 '-v', 'cutoff=2026-10-30T02:00:00+08:00', '-v', 'nodes_only=false'],
                                input=sql, env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
