#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3 tools/generate_project.py
mkdir -p build
xcodebuild -version | tee build/xcode-version.txt
xcodebuild -project MyAppBeta.xcodeproj -scheme MyAppBeta -configuration Release \
  -destination 'generic/platform=iOS' -derivedDataPath build/DerivedData \
  CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY= \
  CURRENT_PROJECT_VERSION="${BETA_BUILD_NUMBER:-1}" build | tee build/device-build.log
python3 tools/package_ipa.py build/DerivedData/Build/Products/Release-iphoneos/MyAppBeta.app build/MyApp-Beta-unsigned.ipa
shasum -a 256 build/MyApp-Beta-unsigned.ipa > build/SHA256SUMS.txt
