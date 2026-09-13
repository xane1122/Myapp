#!/usr/bin/env python3
from pathlib import Path
import json
import plistlib
import subprocess
import sys
import xml.etree.ElementTree as ET

root = Path(__file__).resolve().parents[1]
subprocess.run([sys.executable, str(root / 'tools/generate_project.py')], check=True)
plist = plistlib.loads((root / 'MyAppBeta/Info.plist').read_bytes())
assert plist['CFBundleDisplayName'] == 'MyApp Beta'
assert plist['CFBundleIdentifier'] == '$(PRODUCT_BUNDLE_IDENTIFIER)'
assert plist['CFBundleURLTypes'][0]['CFBundleURLSchemes'] == ['myapp-beta']
assert 'NSCameraUsageDescription' in plist
assert 'NSAppTransportSecurity' not in plist
assert 'UIBackgroundModes' not in plist  # Background URLSession needs no fake audio/fetch entitlement.
privacy = plistlib.loads((root / 'MyAppBeta/PrivacyInfo.xcprivacy').read_bytes())
assert privacy['NSPrivacyTracking'] is False
icons = root / 'MyAppBeta/Assets.xcassets/AppIcon.appiconset'
manifest = json.loads((icons / 'Contents.json').read_text())
for icon in manifest['images']:
    assert (icons / icon['filename']).is_file()
assert any(i['filename'] == 'AppIcon-1024.png' and i['size'] == '1024x1024'
           and i['idiom'] in {'universal', 'ios-marketing'} for i in manifest['images'])
ET.parse(root / 'MyAppBeta.xcodeproj/xcshareddata/xcschemes/MyAppBeta.xcscheme')
for file in root.rglob('*'):
    if file.is_file() and 'build' not in file.parts:
        assert file.suffix.lower() not in {'.p8', '.p12', '.mobileprovision', '.ipa'}, file
print('PASS: project, Beta identity, icon assets, Info.plist, privacy manifest and no signing credentials')
