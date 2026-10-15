import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
locale = root / 'frontend/locales'
en = json.loads((locale / 'en.json').read_text())
for language in ('ru', 'uk'):
    assert json.loads((locale / f'{language}.json').read_text()).keys() == en.keys()
print('Translation keys checked')
