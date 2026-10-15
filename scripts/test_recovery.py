import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('recovery', Path(__file__).resolve().parents[1] / 'internal/backup/recover.py')
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'replacement'
        self.backup = Path(self.temp.name) / 'backup'
        (self.root / 'etc').mkdir(parents=True)
        (self.root / 'etc/os-release').write_text('ID=debian\nVERSION_ID="13"\n')
        (self.root / 'usr/lib/panasms').mkdir(parents=True)
        (self.root / 'usr/lib/panasms/VERSION').write_text('0.2.28\n')
        self.backup.mkdir()
        (self.backup / 'system.json').write_text(json.dumps({'system': recovery.inventory(self.root)}))
        for name in recovery.DATABASES:
            with sqlite3.connect(self.backup / name) as db:
                db.execute('CREATE TABLE fixture(value TEXT)')

    def archive(self, name='etc/panasms/example.conf', link=False):
        with tarfile.open(self.backup / 'configuration.tar', 'w') as archive:
            entry = tarfile.TarInfo(name)
            if link:
                entry.type = tarfile.SYMTYPE
                entry.linkname = '/etc/shadow'
            else:
                entry.size = 7
            archive.addfile(entry, None if link else io.BytesIO(b'fixture'))

    def test_valid_plan_does_not_write_destination(self):
        self.archive()
        files = recovery.planned(self.backup, self.root)
        self.assertIn('etc/panasms/example.conf', files)
        self.assertFalse((self.root / 'etc/panasms').exists())

    def test_wrong_version(self):
        self.archive()
        (self.root / 'usr/lib/panasms/VERSION').write_text('0.2.29')
        with self.assertRaises(ValueError):
            recovery.planned(self.backup, self.root)

    def test_archive_traversal_and_links(self):
        for name, link in [('etc/panasms/../../shadow', False), ('etc/panasms/link', True), ('root/.ssh/authorized_keys', False)]:
            with self.subTest(name=name):
                self.archive(name, link)
                with self.assertRaises(ValueError):
                    recovery.planned(self.backup, self.root)

    def test_destination_symlink(self):
        self.archive()
        (self.root / 'etc/panasms').symlink_to(self.backup, target_is_directory=True)
        with self.assertRaises(ValueError):
            recovery.planned(self.backup, self.root)


if __name__ == '__main__':
    unittest.main()
