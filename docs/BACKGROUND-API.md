# 后台回复下载：最小 VPS 接口方案

状态：只完成 Beta 客户端结构，功能开关保持关闭。本文没有修改或部署生产 VPS。

## 为什么需要新接口

当前网页向 `POST /api/chat` 发起一个最长约三分钟的流式请求，完成时返回 `assistant_message_id`。iOS 把 App 挂到后台后，普通网页请求不能作为可靠的长期后台任务。`URLSessionConfiguration.background` 可以由系统进程继续下载，但需要一个可下载的有限结果资源，不能可靠承载现有的长连接生成流程。

## 建议的最小接口

### 1. 创建异步回复

`POST /api/native-replies`

- 接受与现有 `/api/chat` 相同的消息、附件、assistant 和 conversation_id 字段。
- 服务端先保存用户消息，并创建 UUID job；同一 `Idempotency-Key` 必须返回同一 job，防止重复扣费和重复消息。
- 返回 `202 Accepted`：

```json
{
  "job_id": "UUID",
  "conversation_id": 47,
  "result_url": "https://xanelove.com/api/native-replies/UUID/result?token=一次性高熵令牌",
  "expires_at": "RFC3339"
}
```

`result_url` 必须限定为 HTTPS、精确主机 `xanelove.com` 和固定路径。令牌只授权读取单个 job 结果，至少 128 bit 随机数，存储哈希，24 小时内过期；不要复用网页 Cookie、API Key 或模型密钥。

### 2. 下载结果

`GET /api/native-replies/{job_id}/result?token=...`

- job 未完成时保持请求等待，服务端心跳可维持代理连接；等待上限建议 15 分钟。
- 完成后返回 `200 application/json`，正文不超过 1 MB：

```json
{
  "job_id": "UUID",
  "status": "completed",
  "conversation_id": "47",
  "message_id": "902",
  "assistant": "rhys"
}
```

- Beta 只需要路由 ID；完整消息继续从现有 `/api/history` 读取，避免把私密内容写入系统后台下载临时文件。
- 失败返回确定的 4xx/5xx JSON，不能自动重新调用模型；结果端点重连只能读取原 job。
- 成功结果在 24 小时内可幂等读取。服务端保存 `queued/running/completed/failed`、用户消息 ID、助手消息 ID和完成时间。

## 并发、部署与兼容

- 生成任务必须脱离原 HTTP 请求上下文运行，但沿用现有模型通道、工具、记忆入队、TTS、消息保存和去重逻辑。
- 每个会话串行，沿用当前上游限流；客户端最多同时保留 8 个后台结果下载。
- 原 `/api/chat` 保持不变，正式版完全不受影响。Beta 前台仍可继续使用现有流式接口。
- 需要生产改动的预期范围：新增 `internal/api/native_replies.go` 与测试；在路由注册处新增两个端点；`internal/db` 增加 job 表迁移；抽取现有 `handleChat` 的一次生成函数供同步和异步路径共用；更新架构和 API 文档。
- 需要数据库迁移和服务部署，因此必须在用户审阅并明确批准后单独实施；实施前按现有规则备份生产数据库并验证备份。

## iOS 行为

用户发消息后，网页拿到 ticket 并调用 `MyAppNative.waitForReply(ticket)`。Beta 创建背景下载；下载完成后验证状态、job、conversation/message ID，预约带默认声音和角标的本地通知。点击通知打开：

`https://xanelove.com/?conversation_id=47&message_id=902&notification_source=myapp-beta`

若用户从多任务界面强制结束 Beta，iOS 不会自动重新启动 App 处理后台下载。此方案也不能替代 APNs 传递任意时刻产生的服务器消息。
