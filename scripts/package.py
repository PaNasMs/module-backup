import hashlib
import json
import os
from pathlib import Path
import struct
import subprocess
import zipfile

root = Path(__file__).resolve().parents[1]
manifest = json.loads((root / 'manifest.json').read_text())
architecture = subprocess.check_output(['go', 'env', 'GOARCH'], text=True).strip()
assert architecture in ('arm64', 'amd64'), 'Unsupported module architecture'
assert subprocess.check_output(['go', 'env', 'GOOS'], text=True).strip() == 'linux', 'Module must target Linux'
manifest['architecture'] = architecture
assert os.environ.get('GITHUB_REF_TYPE') != 'tag' or os.environ['GITHUB_REF_NAME'] == 'v' + manifest['version'], 'Tag must match manifest version'
server = (root / 'dist/bin/server').read_bytes()
assert server[:6] == b'\x7fELF\x02\x01' and struct.unpack('<H', server[18:20])[0] == {'arm64': 183, 'amd64': 62}[architecture]
payload = {'bin/server': server, 'LICENSE': (root / 'LICENSE').read_bytes(), 'NOTICE': (root / 'NOTICE').read_bytes()}
for path in (root / 'dist/ui').rglob('*'):
    if path.is_file():
        payload[path.relative_to(root / 'dist').as_posix()] = path.read_bytes()
manifest['files'] = {name: hashlib.sha256(data).hexdigest() for name, data in payload.items()}
output = root / 'dist' / f"backup-{manifest['version']}-{architecture}.unsigned.zip"
with zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as archive:
    archive.writestr('manifest.json', json.dumps(manifest, sort_keys=True, separators=(',', ':'), ensure_ascii=False))
    for name, data in sorted(payload.items()):
        archive.writestr(name, data)
print(output)
