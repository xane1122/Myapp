# MyApp Beta

可长期维护的 SwiftUI + WKWebView iOS 壳，加载 `https://xanelove.com`，与现有正式版同时安装。

- 显示名称：MyApp Beta
- Bundle ID：`com.xanelove.myapp.beta`
- 已检查的旧 IPA Bundle ID：`com.example.MyWebApp`
- 最低系统：iOS 16
- 签名方式：GitHub Actions 生成未签名 IPA，再由 SideStore 签名安装

## 第一阶段能力

- SwiftUI 生命周期和持久的 `WKWebsiteDataStore.default()`；补充保存本站 session Cookie。
- 安全区、状态栏、交互式键盘收起、WebKit 返回手势、断网恢复与 WebContent 进程恢复。
- 只对主框架、HTTPS 精确来源 `xanelove.com` 开放双向 JavaScript Bridge。
- 原生相机、PhotosPicker 和文件选择；图片按 EXIF 方向解码并重新编码，固化旋转/镜像方向，最长边 2048。
- 本地通知、默认声音和角标；通知携带 conversation/message ID，点击后复用网站现有消息定位逻辑。
- 后台 URLSession 客户端骨架；生产接口未获批准前由功能开关关闭。

Beta 与正式版的设备沙盒、Cookie 和网页存储独立；两者访问相同网站和服务端数据库，所以聊天、会话和服务器设置是共用的。工程不包含 API Key、模型配置、签名证书或 provisioning profile。

## Xcode 构建

```bash
python3 tools/generate_project.py
bash tools/test_ios.sh
bash tools/build_ipa.sh
```

产物是 `build/MyApp-Beta-unsigned.ipa`。脚本在打包前验证 Bundle ID、显示名称、iPhoneOS 平台、arm64 Mach-O 和未嵌入 profile，避免把模拟器包或正式版误当成 Beta。

## GitHub Actions

Actions 在 `macos-15` 上运行源码检查、Bridge 测试、iOS Simulator 单元测试及无签名 device build，最后上传未签名 IPA、元数据和 SHA-256。下载 Actions artifact，解压后把 `.ipa` 导入 SideStore。

后台回复所需的最小服务器接口见 [docs/BACKGROUND-API.md](docs/BACKGROUND-API.md)。
