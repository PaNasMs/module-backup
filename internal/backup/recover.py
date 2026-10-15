#!/usr/bin/env python3
"""Apply a platform backup to a replacement installation after explicit review."""
import argparse
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tarfile
import tempfile

ALLOWED = ('etc/panasms', 'etc/fstab', 'etc/crypttab', 'etc/mdadm', 'etc/samba', 'etc/exports',
           'etc/passwd', 'etc/group', 'etc/shadow', 'etc/gshadow', 'etc/NetworkManager/system-connections',
           'var/lib/panasms-modules/registry.json')
DATABASES = {'var_lib_panasms_state.db': 'var/lib/panasms/state.db',
             'var_lib_panasms-agent_jobs.db': 'var/lib/panasms-agent/jobs.db'}

def safe_target(root, name):
    parts = Path(name).parts
    if not parts or any(p in ('.', '..', '/') for p in parts):
        raise ValueError('Invalid archive path')
    target = root.joinpath(*parts)
    for parent in [target, *target.parents]:
        if parent == root.parent:
            break
        if parent.is_symlink():
            raise ValueError('Symbolic link in recovery destination: ' + str(parent))
    return target

def inventory(root):
    release = {}
    for line in (root / 'etc/os-release').read_text().splitlines():
        if '=' in line:
            k, v = line.split('=', 1)
            release[k] = v.strip('"')
    return {'os': release.get('ID'), 'version': release.get('VERSION_ID'),
            'core': (root / 'usr/lib/panasms/VERSION').read_text().strip()}

def planned(backup, root):
    meta = json.loads((backup / 'system.json').read_text())
    if inventory(root) != meta['system']:
        raise ValueError('Install the matching OS and PaNasMs version before restoring')
    files = []
    with tarfile.open(backup / 'configuration.tar') as archive:
        seen = set()
        for member in archive.getmembers():
            name = member.name.lstrip('/')
            if not any(name == p or name.startswith(p + '/') for p in ALLOWED):
                raise ValueError('Unexpected configuration archive path: ' + name)
            if name in seen or not (member.isfile() or member.isdir()):
                raise ValueError('Duplicate, linked or special archive entry: ' + name)
            seen.add(name)
            safe_target(root, name)
            if member.isfile():
                files.append(name)
    for name, dest in DATABASES.items():
        with sqlite3.connect('file:' + str(backup / name) + '?mode=ro', uri=True) as db:
            if db.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                raise ValueError('Invalid platform database')
        safe_target(root, dest)
        files.append(dest)
    return files

def atomic(path, data, mode, uid=0, gid=0):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.recovery-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), mode & 0o777)
            os.fchown(stream.fileno(), uid, gid)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('plan', 'apply'))
    parser.add_argument('--backup', required=True, type=Path)
    parser.add_argument('--target', default='/', type=Path)
    parser.add_argument('--confirm-replacement', action='store_true')
    args = parser.parse_args()
    root = args.target.resolve(strict=True)
    backup = args.backup.resolve(strict=True)
    files = planned(backup, root)
    # Hardware and network identity must come from the replacement OS, not the lost disk.
    preserve = ('etc/fstab', 'etc/crypttab', 'etc/mdadm', 'etc/NetworkManager/system-connections',
                'etc/passwd', 'etc/group', 'etc/shadow', 'etc/gshadow', 'var/lib/panasms-modules/registry.json')
    replaced = [f for f in files if not any(f == p or f.startswith(p + '/') for p in preserve)]
    result = {'target': str(root), 'replace': replaced,
              'reviewSeparately': [f for f in files if f not in replaced],
              'requirements': ['Recreate Linux users with their original numeric IDs before restoring user data.',
                               'Reconnect original data disks and unlock encrypted volumes without formatting.',
                               'Reinstall modules at the recorded versions through the signed catalog.']}
    print(json.dumps(result, indent=2))
    if args.operation == 'plan':
        return
    if not args.confirm_replacement or os.geteuid() != 0:
        raise ValueError('Apply requires root and --confirm-replacement on the replacement installation')
    saved = Path(tempfile.mkdtemp(prefix='panasms-before-recovery-', dir=root / 'var/backups'))
    os.chmod(saved, 0o700)
    services = []
    if root == Path('/'):
        output = subprocess.check_output(['systemctl', 'list-units', '--type=service', '--state=active', '--no-legend', '--plain'], text=True)
        services = [line.split()[0] for line in output.splitlines() if line.split()[0] in ('panasms-core.service', 'panasms-agent.service') or line.split()[0].startswith('panasms-module-')]
        if services:
            subprocess.run(['systemctl', 'stop', *services], check=True)
    try:
        for name in replaced:
            path = safe_target(root, name)
            if path.exists():
                dest = saved / name
                dest.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, dest)
        with tarfile.open(backup / 'configuration.tar') as archive:
            for member in archive.getmembers():
                name = member.name.lstrip('/')
                if name in replaced:
                    atomic(safe_target(root, name), archive.extractfile(member).read(), member.mode, member.uid, member.gid)
        for name, dest in DATABASES.items():
            target = safe_target(root, dest)
            for suffix in ('-wal', '-shm'):
                sidecar = Path(str(target) + suffix)
                if sidecar.exists():
                    shutil.move(sidecar, saved / (name + suffix))
            atomic(target, (backup / name).read_bytes(), 0o600)
            if dest == 'var/lib/panasms/state.db':
                import pwd
                account = pwd.getpwnam('panasms')
                os.chown(target, account.pw_uid, account.pw_gid)
    except Exception:
        print('Recovery interrupted. Services remain stopped. Previous files: ' + str(saved))
        raise
    if services:
        subprocess.run(['systemctl', 'start', *services], check=True)
    print('Applied platform configuration. Previous files: ' + str(saved))
    print('Verify login, storage, users, modules and applications before using the replacement system.')

if __name__ == '__main__':
    main()
