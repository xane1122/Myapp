# MyApp

MyApp 源码按运行端分为两个目录：

- [`ios-swift/`](ios-swift/)：SwiftUI + WKWebView 原生壳，可由 GitHub Actions 构建未签名 IPA。
- [`web/`](web/)：`xanelove.com` 使用的 Go 服务端和网页前端源码。

仓库不包含生产数据库、附件、API Key、SSH 私钥、签名证书或 IPA。

```bash
cd web && go test ./... && go build ./cmd/server
cd ../ios-swift && python3 tools/generate_project.py && bash tools/test_ios.sh
```

生产部署仍以 VPS 的 `/root/myapp-workspace/current` 为准；提交不会自动覆盖正式版。
