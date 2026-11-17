#!/usr/bin/python3
"""Queue nightly full scans; run with the API service's environment file."""
import argparse
from datetime import datetime, timedelta
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo


QUEUE_SQL = """
BEGIN;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';
-- Read every enabled range, independently of the HTTP list pagination.
WITH queued AS (
 INSERT INTO network_auto_scans(network_id,cidr,protocol,user_id,recheck_after)
 SELECT n.id,n.cidr,p.protocol,:'user_id'::uuid,:'cutoff'::timestamptz
 FROM networks n CROSS JOIN (VALUES ('grpc'),('cf')) AS p(protocol)
 WHERE n.enabled AND (p.protocol='grpc' OR NOT :'nodes_only'::boolean)
 AND (p.protocol='grpc' OR EXISTS(SELECT 1 FROM cf_targets))
 ON CONFLICT(network_id,protocol) DO UPDATE SET
  cidr=excluded.cidr,user_id=excluded.user_id,
  created_at=now(),recheck_after=excluded.recheck_after
 -- Preserve unfinished full scans, including those carried over from yesterday.
 WHERE network_auto_scans.cidr<>excluded.cidr OR network_auto_scans.recheck_after IS NULL
 RETURNING protocol
)
SELECT protocol,count(*) FROM queued GROUP BY protocol ORDER BY protocol;
"""


def nightly_cutoff(now=None):
    now = now or datetime.now(ZoneInfo('Asia/Shanghai'))
    cutoff = now.replace(hour=2, minute=0, second=0, microsecond=0)
    return cutoff if now >= cutoff else cutoff - timedelta(days=1)


def postgres_environment(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in ('postgres', 'postgresql'):
        raise ValueError('Expected a PostgreSQL URL')
    env = os.environ.copy()
    env['PGCONNECT_TIMEOUT'] = '10'
    for key, value in [('HOST', parsed.hostname), ('PORT', parsed.port),
                       ('USER', parsed.username), ('PASSWORD', parsed.password),
                       ('DATABASE', parsed.path.lstrip('/'))]:
        if value is not None:
            env['PG' + key] = urllib.parse.unquote(str(value))
    for key, value in urllib.parse.parse_qsl(parsed.query):
        env['PG' + ('DATABASE' if key == 'dbname' else key.upper())] = value
    return env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='Validate the queue transaction, then roll it back')
    parser.add_argument('--nodes-only', action='store_true', help='Repair a missed node run without queuing CF again')
    args = parser.parse_args()
    base = os.environ.get('API_URL', 'http://127.0.0.1:8081')
    cookie = ''

    def api(path, data=None):
        nonlocal cookie
        headers = {'Content-Type': 'application/json', 'Origin': os.environ['APP_ORIGIN']}
        if cookie:
            headers['Cookie'] = cookie
        request = urllib.request.Request(base + path, headers=headers,
                                         data=None if data is None else json.dumps(data).encode())
        with urllib.request.urlopen(request, timeout=20) as response:
            if path == '/auth/login':
                cookie = response.headers['Set-Cookie'].split(';', 1)[0]
            return json.load(response)

    api('/auth/login', {'username': os.environ['ADMIN_USERNAME'], 'password': os.environ['ADMIN_PASSWORD']})
    try:
        user_id = api('/auth/me')['id']
        # Keep database credentials out of process arguments and diagnostic output.
        env = postgres_environment(os.environ['DATABASE_URL'])
        cutoff = nightly_cutoff().isoformat()
        result = subprocess.run(
            ['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1',
             '-v', 'user_id=' + user_id, '-v', 'cutoff=' + cutoff,
             '-v', 'nodes_only=' + str(args.nodes_only).lower()],
            input=QUEUE_SQL + ('ROLLBACK;' if args.check else 'COMMIT;'),
            env=env, text=True, capture_output=True, timeout=60)
        if result.returncode:
            raise RuntimeError('Nightly queue transaction failed; no changes committed')
        print(f'Full scan cutoff: {cutoff}; ' + ('validation rolled back' if args.check else 'queue committed'), flush=True)
        for line in result.stdout.splitlines():
            print('Queued networks: ' + line, flush=True)
        if not result.stdout.strip():
            print('No new ranges queued; unfinished full scans are preserved', flush=True)
    finally:
        api('/auth/logout', {})


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Connection exceptions can include credential-bearing URLs.
        print(f'Daily scan failed ({type(error).__name__}); check API/database availability', file=sys.stderr)
        sys.exit(1)
