#!/usr/bin/python3
"""Sample database waits and host counters without recording SQL or arguments."""
import json
import os
from pathlib import Path
import subprocess
import time


WAIT_SQL = """
SELECT coalesce(json_agg(w), '[]'::json) FROM (
 SELECT pid,backend_start,state,
   round(extract(epoch FROM clock_timestamp()-query_start)::numeric,3) AS query_seconds,
   round(extract(epoch FROM clock_timestamp()-xact_start)::numeric,3) AS transaction_seconds,
   wait_event_type,wait_event,pg_blocking_pids(pid) AS blockers,
   CASE WHEN upper(trim(query)) IN ('COMMIT','ROLLBACK') THEN upper(trim(query))
     ELSE 'statement' END AS operation
 FROM pg_stat_activity
 WHERE datname=current_database() AND pid<>pg_backend_pid()
   AND state<>'idle' AND clock_timestamp()-query_start>interval '2 seconds'
 ORDER BY query_start LIMIT 10
) w;
"""


def database_waits():
    environment = dict(os.environ, PGAPPNAME='ip-list-diagnostics', PGCONNECT_TIMEOUT='2',
                       PGOPTIONS='-c statement_timeout=2000 -c lock_timeout=500')
    result = subprocess.run(['psql', '-XAtw', '-v', 'ON_ERROR_STOP=1', '-c', WAIT_SQL],
                            env=environment, capture_output=True, text=True, timeout=4,
                            check=True)
    return json.loads(result.stdout)


def host_counters(root=Path('/proc')):
    counters = {}
    for name, relative in [('cpu_ticks', 'stat'), ('io_pressure', 'pressure/io'),
                           ('load', 'loadavg'), ('diskstats', 'diskstats')]:
        try:
            data = (root / relative).read_text()
            if name == 'cpu_ticks':
                counters[name] = [int(v) for v in data.splitlines()[0].split()[1:]]
            elif name == 'diskstats':
                counters[name] = [line.split() for line in data.splitlines()
                                  if not line.split()[2].startswith(('loop', 'ram'))]
            else:
                counters[name] = data.strip()
        except (OSError, ValueError, IndexError):
            counters[name] = None
    return counters


def sample(previous):
    now = time.monotonic()
    entry = dict(time=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                 interval_seconds=round(now-previous, 3), **host_counters())
    try:
        entry['waits'] = database_waits()
    except (subprocess.SubprocessError, OSError, ValueError) as error:
        # stderr can contain SQL or connection details; retain only the error type.
        entry['database_error'] = type(error).__name__
    return now, entry


def main():
    previous, last_log = time.monotonic(), 0
    while True:
        previous, entry = sample(previous)
        if (entry.get('waits') or entry.get('database_error')
                or entry['interval_seconds'] > 10 or previous-last_log >= 60):
            print(json.dumps(entry, separators=(',', ':')), flush=True)
            last_log = previous
        time.sleep(max(0, 5-(time.monotonic()-previous)))


if __name__ == '__main__':
    main()
