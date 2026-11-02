import contextlib
import importlib.util
import io
import os
from pathlib import Path
import tempfile
import time
import unittest


spec = importlib.util.spec_from_file_location(
    "retention", Path(__file__).resolve().parents[1] / "cleanup-deployments.py")
retention = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retention)


class CleanupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.releases = self.root / "releases"
        self.backups = self.root / "backups"
        self.uploads = self.root / "uploads"
        for path in (self.releases, self.backups, self.uploads):
            path.mkdir()
        self.current = self.root / "current"
        self.previous = self.root / "previous"

    def run_cleanup(self, dry_run=False):
        with contextlib.redirect_stdout(io.StringIO()):
            retention.cleanup(self.releases, self.current, self.previous,
                              self.backups, self.uploads, dry_run)

    def release(self, char):
        path = self.releases / (char * 40)
        path.mkdir()
        return path

    def test_protects_pointers_even_if_older_than_failed_release(self):
        active, previous, failed = [self.release(c) for c in "abc"]
        self.current.symlink_to(active)
        self.previous.symlink_to(previous)
        os.utime(active, (1, 1))
        os.utime(previous, (1, 1))
        self.run_cleanup()
        self.assertTrue(active.exists())
        self.assertTrue(previous.exists())
        self.assertFalse(failed.exists())

    def test_keeps_three_complete_backups_and_removes_partial(self):
        snapshots = []
        for index in range(5):
            path = self.backups / f"deploy-20261102T01000{index}Z-ABC123"
            path.mkdir()
            dump = path / "database.dump"
            dump.write_bytes(b"dump")
            os.utime(dump, (index + 1, index + 1))
            snapshots.append(path)
        partial = self.backups / "deploy-20261102T020000Z-DEF456"
        partial.mkdir()
        (partial / "database.dump.partial").write_bytes(b"incomplete")
        self.run_cleanup()
        self.assertEqual([p.exists() for p in snapshots], [False, False, True, True, True])
        self.assertFalse(partial.exists())

    def test_prunes_only_stale_owned_uploads_and_staging(self):
        stale = [self.uploads / "123-1", self.releases / ".staging.ABC123"]
        recent = self.uploads / "124-1"
        unrelated = self.uploads / "another-project"
        for path in stale + [recent, unrelated]:
            path.mkdir()
        for path in stale + [unrelated]:
            os.utime(path, (time.time() - 90000,) * 2)
        self.run_cleanup()
        self.assertTrue(recent.exists())
        self.assertTrue(unrelated.exists())
        self.assertTrue(all(not p.exists() for p in stale))

    def test_ignores_unrelated_names_and_symlinks(self):
        unrelated = self.releases / "manual-backup"
        unrelated.mkdir()
        external = self.root / "other-project"
        external.mkdir()
        link = self.releases / ("d" * 40)
        link.symlink_to(external)
        self.run_cleanup()
        self.assertTrue(unrelated.exists())
        self.assertTrue(link.is_symlink())
        self.assertTrue(external.exists())

    def test_dry_run_changes_nothing(self):
        inactive = self.release("a")
        self.run_cleanup(dry_run=True)
        self.assertTrue(inactive.exists())

    def test_unsafe_pointer_aborts_before_any_deletion(self):
        inactive = self.release("a")
        self.current.symlink_to(self.root)
        with self.assertRaises(ValueError):
            self.run_cleanup()
        self.assertTrue(inactive.exists())

    def test_broken_pointer_aborts_before_any_deletion(self):
        inactive = self.release("a")
        self.current.symlink_to(self.releases / "missing")
        with self.assertRaises(FileNotFoundError):
            self.run_cleanup()
        self.assertTrue(inactive.exists())


if __name__ == "__main__":
    unittest.main()
