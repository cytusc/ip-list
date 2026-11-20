import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('diagnostics', Path(__file__).parents[1] / 'db-diagnostics.py')
diagnostics = importlib.util.module_from_spec(spec)
spec.loader.exec_module(diagnostics)


class DatabaseDiagnosticsTests(unittest.TestCase):
    def test_database_errors_do_not_expose_sql_or_credentials(self):
        for error in [subprocess.TimeoutExpired('secret', 4),
                      subprocess.CalledProcessError(1, 'secret', stderr='credential'),
                      OSError('credential')]:
            with patch.object(diagnostics, 'database_waits', side_effect=error), \
                    patch.object(diagnostics, 'host_counters', return_value={}):
                _, entry = diagnostics.sample(diagnostics.time.monotonic())
            self.assertEqual(entry['database_error'], type(error).__name__)
            self.assertNotIn('secret', str(entry))
            self.assertNotIn('credential', str(entry))

    def test_bounded_noninteractive_query(self):
        with patch.object(diagnostics.subprocess, 'run') as run:
            run.return_value.stdout = '[]'
            self.assertEqual(diagnostics.database_waits(), [])
            args, kwargs = run.call_args
            self.assertIn('-XAtw', args[0])
            self.assertEqual(kwargs['timeout'], 4)
            self.assertIn('statement_timeout=2000', kwargs['env']['PGOPTIONS'])
        self.assertIn('pg_blocking_pids(pid)', diagnostics.WAIT_SQL)
        self.assertNotIn('SELECT query', diagnostics.WAIT_SQL)

    def test_host_counters_and_missing_proc_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'stat').write_text('cpu 1 2 3 4 5 6 7 8\ncpu0 1 2')
            (root / 'diskstats').write_text('7 0 loop0 0\n8 0 sda 1\n')
            counters = diagnostics.host_counters(root)
        self.assertEqual(counters['cpu_ticks'][-1], 8)
        self.assertEqual(counters['diskstats'], [['8', '0', 'sda', '1']])
        self.assertIsNone(counters['io_pressure'])


if __name__ == '__main__':
    unittest.main()
