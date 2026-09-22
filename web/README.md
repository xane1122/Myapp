# MyApp Web

Go 服务端和网页前端源码，与 `ios-swift/` 原生壳分开维护。

- `cmd/`：服务入口与维护工具
- `internal/`：聊天、记忆、通知、文件和转账逻辑
- `static/`：网页前端与 Service Worker
- `config/`：系统提示词模板
- `docs/`：架构与维护文档

```bash
go test ./...
go build ./cmd/server
```

生产 `.env`、数据库、附件、备份和密钥不得提交。部署前以 VPS 工作区当前 diff 为准。
