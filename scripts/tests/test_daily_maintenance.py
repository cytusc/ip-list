"""Exercise retention and conditional disk reclamation against PostgreSQL."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('daily_scan', SCRIPTS / 'daily-scan.py')
daily = importlib.util.module_from_spec(spec)
spec.loader.exec_module(daily)


@unittest.skipUnless(os.environ.get('TEST_DATABASE_URL'), 'Requires TEST_DATABASE_URL')
class DailyMaintenanceTests(unittest.TestCase):
    def test_history_cleanup_preserves_current_data_and_reclaims_bloat(self):
        env = daily.postgres_environment(os.environ['TEST_DATABASE_URL'])
        database = f'maintenance_{os.getpid()}_test'
        subprocess.run(['createdb', database], env=env, check=True)
        self.addCleanup(subprocess.run, ['dropdb', database], env=env, check=True)
        env['PGDATABASE'] = database

        def sql(query):
            return subprocess.run(['psql', '-XqAt', '-v', 'ON_ERROR_STOP=1'],
                                  input=query, env=env, text=True, capture_output=True,
                                  check=True).stdout.strip()

        sql("""
CREATE TABLE node_scan_rounds(id int,started_at timestamptz,status text,updated_at timestamptz);
INSERT INTO node_scan_rounds VALUES
 (1,now()-interval '2 days','completed',now()),
 (2,now()-interval '2 days','running',now()),
 (3,now(),'completed',now()),
 (4,now()-interval '2 days','running',now()-interval '3 minutes');
CREATE TABLE dns_sync_runs(id int);
INSERT INTO dns_sync_runs SELECT generate_series(1,50);
CREATE TABLE latency_probe_results(checked_at timestamptz);
INSERT INTO latency_probe_results VALUES(now()),(now()-interval '25 hours');
CREATE TABLE latency_probe_rollups(bucket_start timestamptz);
INSERT INTO latency_probe_rollups VALUES(now()),(now()-interval '25 hours');
CREATE TABLE node_ip_pool(id int,payload text);
INSERT INTO node_ip_pool SELECT i,repeat('x',1024) FROM generate_series(1,80000) i;
DELETE FROM node_ip_pool WHERE id%5<>0;
CREATE TABLE ip_results(id int);
CREATE TABLE cf_ip_pool(id int);
CREATE TABLE cf_results(id int);
INSERT INTO ip_results VALUES(7);
""")
        before = int(sql("SELECT pg_relation_size('node_ip_pool');"))
        with tempfile.TemporaryDirectory() as backups:
            env['MAINTENANCE_BACKUP_DIR'] = backups
            result = subprocess.run(['bash', str(SCRIPTS / 'daily-maintenance.sh')],
                                    env=env, text=True, capture_output=True, timeout=120)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(len(list(Path(backups).glob('*.dump'))), 1)
            self.assertLess(int(sql("SELECT pg_relation_size('node_ip_pool');")), before // 2)
            self.assertEqual(sql('SELECT count(*) FROM node_ip_pool;'), '16000')
            self.assertEqual(sql('SELECT id FROM node_scan_rounds ORDER BY id;'), '2\n3')
            self.assertEqual(sql('SELECT count(*) FROM dns_sync_runs;'), '20')
            self.assertEqual(sql('SELECT count(*) FROM latency_probe_results;'), '1')
            self.assertEqual(sql('SELECT count(*) FROM latency_probe_rollups;'), '1')
            self.assertEqual(sql('SELECT id FROM ip_results;'), '7')
            # A second run keeps the same data and does not make another backup.
            result = subprocess.run(['bash', str(SCRIPTS / 'daily-maintenance.sh')],
                                    env=env, text=True, capture_output=True, timeout=120)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(len(list(Path(backups).glob('*.dump'))), 1)
