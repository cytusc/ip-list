#!/usr/bin/env python3
"""Bound storage owned by this deployment pipeline; preserve rollback targets."""

import argparse
from pathlib import Path
import re
import shutil
import time


def cleanup(releases, current, previous, backups, uploads, dry_run=False):
    protected = set()
    for link in (current, previous):
        if link.is_symlink():
            target = link.resolve(strict=True)
            if target.parent != releases.resolve():
                raise ValueError(f"Refusing cleanup: {link} points outside {releases}")
            protected.add(target)
        elif link.exists():
            raise ValueError(f"Refusing cleanup: {link} must be a symlink")

    def remove(path):
        print(f"{'Would remove' if dry_run else 'Removing'} {path}")
        if not dry_run:
            shutil.rmtree(path)

    def directories(root, pattern):
        if not root.exists():
            return []
        return [p for p in root.iterdir()
                if not p.is_symlink() and p.is_dir() and re.fullmatch(pattern, p.name)]

    # Use explicit pointers, not mtime, so an old rollback remains available.
    for release in directories(releases, r"[0-9a-f]{40}"):
        if release.resolve() not in protected:
            remove(release)

    snapshots = directories(backups, r"deploy-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9]+")
    valid = []
    for snapshot in snapshots:
        dump = snapshot / "database.dump"
        if dump.is_file() and dump.stat().st_size:
            valid.append(snapshot)
        else:
            remove(snapshot)
    valid.sort(key=lambda p: ((p / "database.dump").stat().st_mtime_ns, p.name), reverse=True)
    for snapshot in valid[3:]:
        remove(snapshot)

    # A cancelled runner may not execute its EXIT trap. Remove leftovers next time.
    cutoff = time.time() - 24 * 60 * 60
    for root, pattern in ((uploads, r"[0-9]+-[0-9]+"), (releases, r"\.staging\.[A-Za-z0-9]+")):
        for path in directories(root, pattern):
            if path.stat().st_mtime < cutoff:
                remove(path)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-root", type=Path, default=Path("/opt/ip-list-releases"))
    parser.add_argument("--current", type=Path, default=Path("/opt/ip-list-current"))
    parser.add_argument("--previous", type=Path, default=Path("/opt/ip-list-previous"))
    parser.add_argument("--backup-root", type=Path, default=Path("/opt/ip-list-backups/cicd"))
    parser.add_argument("--upload-root", type=Path, default=Path("/var/tmp/ip-list-cicd"))
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    cleanup(args.release_root, args.current, args.previous, args.backup_root, args.upload_root, args.dry_run)
