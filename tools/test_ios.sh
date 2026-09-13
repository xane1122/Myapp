#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p build
python3 tools/generate_project.py
device_id=$(xcrun simctl list devices available --json | python3 -c 'import json,sys; d=json.load(sys.stdin); print(next(x["udid"] for key,rows in d["devices"].items() if "iOS" in key for x in rows if x.get("isAvailable") and "iPhone" in x["name"]))')
xcodebuild -project MyAppBeta.xcodeproj -scheme MyAppBeta -configuration Debug \
  -destination "platform=iOS Simulator,id=$device_id" -derivedDataPath build/TestDerivedData \
  -resultBundlePath "build/BetaTests-${BETA_BUILD_NUMBER:-local}.xcresult" \
  CODE_SIGNING_ALLOWED=NO test | tee build/simulator-tests.log
