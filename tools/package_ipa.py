#!/usr/bin/env python3
"""Package only a real arm64 iPhoneOS application. Never patch the old IPA."""
import hashlib
import json
import plistlib
import struct
import sys
import zipfile
from pathlib import Path

def package(app, output):
    app, output = Path(app), Path(output)
    plist = plistlib.loads((app / 'Info.plist').read_bytes())
    assert plist['CFBundleIdentifier'] == 'com.xanelove.myapp.beta', 'Refusing non-Beta bundle'
    assert plist['CFBundleIdentifier'] != 'com.example.MyWebApp'
    assert plist['CFBundleDisplayName'] == 'MyApp Beta'
    assert 'iPhoneOS' in plist['CFBundleSupportedPlatforms'], 'Simulator builds are not installable IPAs'
    binary = app / plist['CFBundleExecutable']
    data = binary.read_bytes()
    magic, cpu = struct.unpack_from('<II', data)
    assert magic == 0xFEEDFACF and cpu == 0x0100000C, 'Expected thin arm64 Mach-O'
    ncmds = struct.unpack_from('<I', data, 16)[0]
    offset, device = 32, False
    for _ in range(ncmds):
        cmd, size = struct.unpack_from('<II', data, offset)
        assert size >= 8 and offset + size <= len(data)
        if cmd == 0x32:  # LC_BUILD_VERSION, platform=2 means iOS device.
            device = struct.unpack_from('<I', data, offset + 8)[0] == 2
        offset += size
    assert device, 'Missing iPhoneOS device platform load command'
    assert not (app / 'embedded.mobileprovision').exists(), 'Expected an unsigned app'
    output.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(app.rglob('*')):
            if path.is_file():
                archive.write(path, 'Payload/' + app.name + '/' + path.relative_to(app).as_posix())
    with zipfile.ZipFile(output) as archive:
        assert archive.testzip() is None
    report = {'bundle_id': plist['CFBundleIdentifier'], 'display_name': plist['CFBundleDisplayName'],
              'version': plist['CFBundleShortVersionString'], 'build': plist['CFBundleVersion'],
              'platform': 'iPhoneOS', 'architecture': 'arm64', 'signing': 'unsigned; sign with SideStore',
              'sha256': hashlib.sha256(output.read_bytes()).hexdigest()}
    output.with_suffix('.json').write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(report, indent=2))

if __name__ == '__main__':
    package(*sys.argv[1:])
