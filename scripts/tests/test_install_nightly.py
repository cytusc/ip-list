import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('installer', Path(__file__).parents[1] / 'install-nightly.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class InstallNightlyTests(unittest.TestCase):
    def test_normalizes_crlf_and_preserves_previous_version(self):
        with tempfile.TemporaryDirectory() as directory:
            source, target = Path(directory) / 'source.py', Path(directory) / 'installed'
            source.write_bytes(b'#!/usr/bin/python3\r\nprint("new")\r\n')
            target.write_bytes(b'old')
            installer.install(source, target)
            self.assertEqual(target.read_bytes(), b'#!/usr/bin/python3\nprint("new")\n')
            self.assertEqual(Path(str(target) + '.prev').read_bytes(), b'old')

    def test_invalid_source_and_failed_replace_keep_installed_version(self):
        with tempfile.TemporaryDirectory() as directory:
            source, target = Path(directory) / 'source.py', Path(directory) / 'installed'
            target.write_bytes(b'old')
            source.write_bytes(b'def broken(')
            with self.assertRaises(SyntaxError):
                installer.install(source, target)
            source.write_bytes(b'print("new")')
            with patch.object(installer.os, 'replace', side_effect=OSError('test')):
                with self.assertRaises(OSError):
                    installer.install(source, target)
            self.assertEqual(target.read_bytes(), b'old')
            self.assertEqual(list(Path(directory).glob('.ip-list-daily-scan-*')), [])
