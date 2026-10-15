import os
from pathlib import Path
import sqlite3
import stat
import sys
import tarfile
import tempfile

ROOTS = ('panasms-cloud-sync', 'panasms-containers', 'panasms-file-drop', 'panasms-integration-widgets', 'panasms-backup')
SKIP = {'cache', 'tasks', 'staging', 'restores', 'github'}

with tarfile.open(sys.argv[1], 'w') as archive, tempfile.TemporaryDirectory() as temporary:
    for name in ROOTS:
        root = Path('/var/lib') / name
        if not root.exists():
            continue
        for parent, directories, files in os.walk(root, followlinks=False):
            directories[:] = [d for d in directories if d not in SKIP and not d.startswith('.')]
            for filename in files:
                if filename.startswith('.') or filename.endswith(('-wal', '-shm', '.sock', '.log', '.lock')):
                    continue
                source = Path(parent) / filename
                mode = source.lstat().st_mode
                if not stat.S_ISREG(mode):
                    continue
                if source.stat().st_size > 64 * 1024**2:
                    raise ValueError('Module configuration file exceeds 64 MiB: ' + str(source))
                item = archive.gettarinfo(str(source), arcname=str(source).lstrip('/'))
                if filename.endswith('.db'):
                    import urllib.parse
                    target = Path(temporary) / 'database.sqlite'
                    if target.exists():
                        target.unlink()
                    with sqlite3.connect('file:' + urllib.parse.quote(str(source)) + '?mode=ro', uri=True) as src, sqlite3.connect(target) as dst:
                        src.backup(dst)
                        if dst.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                            raise ValueError('Invalid module database: ' + str(source))
                    item.size = target.stat().st_size
                    with target.open('rb') as data:
                        archive.addfile(item, data)
                else:
                    with source.open('rb') as data:
                        archive.addfile(item, data)
