#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p build
python3 tools/generate_project.py
xcodebuild -project MyAppBeta.xcodeproj -scheme MyAppBeta -configuration Debug \
  -destination "generic/platform=iOS Simulator" -derivedDataPath build/TestDerivedData \
  CODE_SIGNING_ALLOWED=NO build-for-testing | tee build/simulator-test-build.log

# GitHub's macos-15 image can pair Xcode 16.4 with an iOS 18.5 runtime that is
# missing the system Swift WebKit overlay. The app and test bundle still compile,
# but the hosted simulator cannot launch the test host. Developers with a complete
# local simulator runtime can opt in to executing the tests.
if [[ "${RUN_SIMULATOR_TESTS:-0}" == "1" ]]; then
  device_id=$(xcrun simctl list devices available --json | python3 -c 'import json,sys; d=json.load(sys.stdin); print(next(x["udid"] for key,rows in d["devices"].items() if "iOS" in key for x in rows if x.get("isAvailable") and "iPhone" in x["name"]))')
  xcodebuild -project MyAppBeta.xcodeproj -scheme MyAppBeta -configuration Debug \
    -destination "platform=iOS Simulator,id=$device_id" -derivedDataPath build/TestDerivedData \
    -resultBundlePath "build/BetaTests-${BETA_BUILD_NUMBER:-local}.xcresult" \
    CODE_SIGNING_ALLOWED=NO test | tee build/simulator-tests.log
fi
