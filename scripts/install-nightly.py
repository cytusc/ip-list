"""Validate and atomically install the nightly script on the same filesystem."""
from pathlib import Path
import os
import shutil
import sys
import tempfile


def install(source, target):
    target = Path(target)
    content = Path(source).read_bytes().replace(b'\r\n', b'\n')
    compile(content, str(source), 'exec')
    if target.exists() and target.read_bytes() == content and target.stat().st_mode & 0o111:
        return
    fd, temporary = tempfile.mkstemp(prefix='.ip-list-daily-scan-', dir=target.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o755)
        if target.exists():
            shutil.copy2(target, str(target) + '.prev')
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


if __name__ == '__main__':
    install(sys.argv[1], sys.argv[2])
