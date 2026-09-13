#!/usr/bin/env python3
"""Generate the checked-in Xcode project, using Python's standard library only."""
from pathlib import Path
import hashlib
import json
import plistlib

ROOT = Path(__file__).resolve().parents[1]
objects = {}

def uid(name):
    return hashlib.sha256(name.encode()).hexdigest()[:24].upper()

def add(key_name, **values):
    key = uid(key_name)
    objects[key] = values
    return key

def serialize(value, indent=0):
    if isinstance(value, dict):
        return '{\n' + ''.join('\t' * (indent + 1) + json.dumps(k) + ' = ' + serialize(v, indent + 1) + ';\n' for k, v in value.items()) + '\t' * indent + '}'
    if isinstance(value, list):
        return '(' + ', '.join(serialize(v, indent) for v in value) + ')'
    return json.dumps(str(value), ensure_ascii=False)

app_sources = sorted((ROOT / 'MyAppBeta').glob('*.swift'))
test_sources = sorted((ROOT / 'MyAppBetaTests').glob('*.swift'))
files = []

def file_ref(path, kind):
    ref = add('ref:' + path, isa='PBXFileReference', lastKnownFileType=kind, path=path, sourceTree='SOURCE_ROOT')
    files.append(ref)
    return ref

def build_file(path, kind):
    return add('build:' + path, isa='PBXBuildFile', fileRef=file_ref(path, kind))

sources = [build_file(str(p.relative_to(ROOT)).replace('\\', '/'), 'sourcecode.swift') for p in app_sources]
tests = [build_file(str(p.relative_to(ROOT)).replace('\\', '/'), 'sourcecode.swift') for p in test_sources]
resources = [build_file('MyAppBeta/NativeBridge.js', 'sourcecode.javascript'),
             build_file('MyAppBeta/Assets.xcassets', 'folder.assetcatalog'),
             build_file('MyAppBeta/PrivacyInfo.xcprivacy', 'text.xml')]
file_ref('MyAppBeta/Info.plist', 'text.plist.xml')
app_product = add('product:app', isa='PBXFileReference', explicitFileType='wrapper.application', path='MyAppBeta.app', sourceTree='BUILT_PRODUCTS_DIR')
test_product = add('product:test', isa='PBXFileReference', explicitFileType='wrapper.cfbundle', path='MyAppBetaTests.xctest', sourceTree='BUILT_PRODUCTS_DIR')
products = add('products', isa='PBXGroup', children=[app_product, test_product], name='Products', sourceTree='<group>')
group = add('group', isa='PBXGroup', children=files + [products], sourceTree='<group>')

def configs(name, common):
    result = []
    for mode in ['Debug', 'Release']:
        settings = dict(common)
        settings.update({'SWIFT_OPTIMIZATION_LEVEL': '-Onone' if mode == 'Debug' else '-O',
                         'DEBUG_INFORMATION_FORMAT': 'dwarf' if mode == 'Debug' else 'dwarf-with-dsym'})
        if mode == 'Debug':
            settings['SWIFT_ACTIVE_COMPILATION_CONDITIONS'] = 'DEBUG'
            settings['ENABLE_TESTABILITY'] = 'YES'
        result.append(add(name + ':' + mode, isa='XCBuildConfiguration', buildSettings=settings, name=mode))
    return add(name + ':configs', isa='XCConfigurationList', buildConfigurations=result, defaultConfigurationIsVisible='0', defaultConfigurationName='Release')

base = {'IPHONEOS_DEPLOYMENT_TARGET': '16.0', 'SDKROOT': 'iphoneos', 'SWIFT_VERSION': '5.0',
        'CLANG_ENABLE_MODULES': 'YES', 'CLANG_ENABLE_OBJC_ARC': 'YES', 'SWIFT_STRICT_CONCURRENCY': 'targeted',
        'TARGETED_DEVICE_FAMILY': '1,2', 'SUPPORTED_PLATFORMS': 'iphoneos iphonesimulator'}
project_configs = configs('project', base)
app_configs = configs('app', {'PRODUCT_BUNDLE_IDENTIFIER': 'com.xanelove.myapp.beta', 'PRODUCT_NAME': 'MyAppBeta',
    'INFOPLIST_FILE': 'MyAppBeta/Info.plist', 'GENERATE_INFOPLIST_FILE': 'NO', 'CODE_SIGN_STYLE': 'Automatic',
    'ASSETCATALOG_COMPILER_APPICON_NAME': 'AppIcon', 'CURRENT_PROJECT_VERSION': '1', 'MARKETING_VERSION': '0.1.0',
    'LD_RUNPATH_SEARCH_PATHS': ['$(inherited)', '@executable_path/Frameworks'], 'SUPPORTS_MACCATALYST': 'NO'})
test_configs = configs('tests', {'PRODUCT_BUNDLE_IDENTIFIER': 'com.xanelove.myapp.beta.tests', 'PRODUCT_NAME': 'MyAppBetaTests',
    'GENERATE_INFOPLIST_FILE': 'YES', 'TEST_HOST': '$(BUILT_PRODUCTS_DIR)/MyAppBeta.app/MyAppBeta',
    'BUNDLE_LOADER': '$(TEST_HOST)', 'CODE_SIGN_STYLE': 'Automatic'})

def phases(name, source_files, resource_files):
    return [add(name + ':sources', isa='PBXSourcesBuildPhase', buildActionMask='2147483647', files=source_files, runOnlyForDeploymentPostprocessing='0'),
            add(name + ':frameworks', isa='PBXFrameworksBuildPhase', buildActionMask='2147483647', files=[], runOnlyForDeploymentPostprocessing='0'),
            add(name + ':resources', isa='PBXResourcesBuildPhase', buildActionMask='2147483647', files=resource_files, runOnlyForDeploymentPostprocessing='0')]

app_target = add('appTarget', isa='PBXNativeTarget', name='MyAppBeta', productName='MyAppBeta',
    productType='com.apple.product-type.application', productReference=app_product, buildConfigurationList=app_configs,
    buildPhases=phases('app', sources, resources), buildRules=[], dependencies=[])
proxy = add('proxy', isa='PBXContainerItemProxy', containerPortal=uid('project'), proxyType='1', remoteGlobalIDString=app_target, remoteInfo='MyAppBeta')
dependency = add('dependency', isa='PBXTargetDependency', target=app_target, targetProxy=proxy)
test_target = add('testTarget', isa='PBXNativeTarget', name='MyAppBetaTests', productName='MyAppBetaTests',
    productType='com.apple.product-type.bundle.unit-test', productReference=test_product, buildConfigurationList=test_configs,
    buildPhases=phases('test', tests, []), buildRules=[], dependencies=[dependency])
project = add('project', isa='PBXProject', attributes={'LastUpgradeCheck': '1600',
    'TargetAttributes': {app_target: {'CreatedOnToolsVersion': '16.0'}, test_target: {'CreatedOnToolsVersion': '16.0', 'TestTargetID': app_target}}},
    buildConfigurationList=project_configs, compatibilityVersion='Xcode 14.0', developmentRegion='zh-Hans', knownRegions=['en', 'zh-Hans', 'Base'],
    mainGroup=group, productRefGroup=products, projectDirPath='', projectRoot='', targets=[app_target, test_target])
directory = ROOT / 'MyAppBeta.xcodeproj'
directory.mkdir(exist_ok=True)
(directory / 'project.pbxproj').write_text('// !$*UTF8*$!\n' + serialize({'archiveVersion': '1', 'classes': {}, 'objectVersion': '56', 'objects': objects, 'rootObject': project}) + '\n', encoding='utf-8')
scheme = f'''<?xml version="1.0" encoding="UTF-8"?>
<Scheme LastUpgradeVersion="1600" version="1.3">
 <BuildAction parallelizeBuildables="YES" buildImplicitDependencies="YES"><BuildActionEntries>
  <BuildActionEntry buildForTesting="YES" buildForRunning="YES" buildForProfiling="YES" buildForArchiving="YES" buildForAnalyzing="YES">
   <BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{app_target}" BuildableName="MyAppBeta.app" BlueprintName="MyAppBeta" ReferencedContainer="container:MyAppBeta.xcodeproj"/>
  </BuildActionEntry>
 </BuildActionEntries></BuildAction>
 <TestAction buildConfiguration="Debug" selectedDebuggerIdentifier="Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier="Xcode.IDEFoundation.Launcher.LLDB" shouldUseLaunchSchemeArgsEnv="YES">
  <Testables><TestableReference skipped="NO"><BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{test_target}" BuildableName="MyAppBetaTests.xctest" BlueprintName="MyAppBetaTests" ReferencedContainer="container:MyAppBeta.xcodeproj"/></TestableReference></Testables>
 </TestAction>
 <LaunchAction buildConfiguration="Debug" selectedDebuggerIdentifier="Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier="Xcode.IDEFoundation.Launcher.LLDB" launchStyle="0" useCustomWorkingDirectory="NO" ignoresPersistentStateOnLaunch="NO" debugDocumentVersioning="YES" debugServiceExtension="internal" allowLocationSimulation="YES">
  <BuildableProductRunnable runnableDebuggingMode="0"><BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{app_target}" BuildableName="MyAppBeta.app" BlueprintName="MyAppBeta" ReferencedContainer="container:MyAppBeta.xcodeproj"/></BuildableProductRunnable>
 </LaunchAction>
 <ProfileAction buildConfiguration="Release" shouldUseLaunchSchemeArgsEnv="YES" savedToolIdentifier="" useCustomWorkingDirectory="NO" debugDocumentVersioning="YES"/>
 <AnalyzeAction buildConfiguration="Debug"/>
 <ArchiveAction buildConfiguration="Release" revealArchiveInOrganizer="YES"/>
</Scheme>
'''
schemes = directory / 'xcshareddata/xcschemes'
schemes.mkdir(parents=True, exist_ok=True)
(schemes / 'MyAppBeta.xcscheme').write_text(scheme, encoding='utf-8')
print(f'Generated Xcode project: {len(app_sources)} app sources, {len(test_sources)} test sources')
