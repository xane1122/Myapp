# 架构文档

## 1. 总览

系统采用单体 Go 服务加静态前端的结构：

```text
浏览器
  |
  | HTTP/JSON、multipart upload
  v
cmd/server
  |
  v
internal/api
  |       |          |             |
  |       |          |             +--> OpenAI/OpenRouter 兼容接口
  |       |          +--> contextbuilder
  |       +--> memory
  |                  |
  +--> observability |
                     v
                  SQLite

本地文件：static/、uploads/、data/conversations/、logs/
配置文件：.env、config/system_prompt.md、config/tools.json
```

## 2. 分层职责

| 层 | 目录 | 职责 |
| --- | --- | --- |
| 启动层 | `cmd/server` | 加载 `.env`、初始化日志、初始化 SQLite、修复文档索引、启动 HTTP 服务 |
| API 编排层 | `internal/api` | HTTP 路由、请求校验、模型请求组装、工具调用、文件上传、上下文预览 |
| 上下文构建层 | `internal/contextbuilder` | JSON Context 类型、构建、校验、路由、敏感过滤、token 估算 |
| 记忆仓储层 | `internal/memory` | 会话、消息、长期记忆、文档、人设、附件、表情包、偏好读写 |
| 数据库层 | `internal/db` | SQLite 连接和表结构迁移 |
| 可观测层 | `internal/observability` | 事件日志和日志轮转 |
| 前端 | `static` | 聊天页、设置页、上下文工作台、Service Worker、Manifest |

## 3. 运行时启动流程

1. `cmd/server/main.go` 调用 `godotenv.Load()` 加载本地 `.env`。
2. 初始化日志目录，默认 `logs`。
3. 读取 `DB_PATH`，默认 `/opt/myapp/memories.db`，调用 `db.Init`。
4. `db.migrate` 创建或补齐 SQLite 表字段。
5. 调用 `api.EnsureKnowledgeDocIndexes()` 修复历史文档缺失的记忆索引。
6. 使用 `HOST` 和 `PORT` 监听 HTTP。

## 4. HTTP 路由

| 路由 | 说明 |
| --- | --- |
| `/` | 静态前端入口 |
| `/uploads/` | 上传文件静态访问 |
| `/api/chat` | 聊天主链路 |
| `/api/history` | 读取当前会话历史 |
| `/api/clear` | 清空当前会话消息 |
| `/api/prefs` | 用户偏好读写 |
| `/api/conversations` | 会话列表、创建、重命名、删除 |
| `/api/conversation-jsonl` | 当前会话 JSONL |
| `/api/personas` | 人设列表、创建、删除 |
| `/api/personas/activate` | 启用或停用人设 |
| `/api/knowledge-docs` | 历史文档上传和列表 |
| `/api/chat-images` | 聊天图片上传 |
| `/api/stickers` | 表情包上传、列表、更新、禁用 |
| `/api/context-usage` | 上下文 token 估算 |
| `/api/context-preview` | 模型上下文预览和当前会话修正 |
| `/api/memory/manual` | 手动写入长期记忆 |
| `/api/memory/compress` | 压缩消息为长期记忆 |
| `/api/memory/chunks` | 长期记忆列表、更新、删除 |
| `/api/client-events` | 前端事件上报 |
| `/api/health` | 健康检查 |

## 5. 聊天主链路

`/api/chat` 的模型最终文本按 `thought`、`action`、`reply` JSON 对象解析并以规范 JSON 保存到现有 `messages.content`。API 返回独立展示字段；`/api/history` 读取时执行相同解析。解析失败或旧消息为纯文本时，完整内容回退到 `reply`，无需数据库迁移。

### 5.1 传统上下文路径

当 `ENABLE_JSON_CONTEXT_BUILDER=false` 时：

1. `handleChat` 校验消息、会话、API Key、模型和附件。
2. 通过 `memory.SearchChunks(conversationID, req.Message, 6)` 搜索相关长期记忆。
3. `buildSystemPrompt` 拼接人设、基础系统提示词、前端系统提示词、偏好、文档目录、长期记忆索引、会话修正、相关长期记忆和当前时间快照。
4. `memory.GetHistory(conversationID, maxHistory)` 读取最近消息。
5. `buildModelMessages` 生成 OpenAI 兼容消息。
6. `chatTools` 从 `config/tools.json` 加载工具定义。
7. `chatToolChoice` 根据显式意图决定是否强制单个工具。
8. 调用模型；如果返回工具调用，则 `resolveToolCalls` 执行工具并再次调用模型。
9. 保存用户消息和助手消息，更新 JSONL 和会话标题。
10. 根据上下文比例异步触发压缩。

### 5.2 JSON Context 路径

当 `ENABLE_JSON_CONTEXT_BUILDER=true` 时：

1. 仍先读取会话、附件和最近历史。
2. `buildJSONContextMessages` 读取全部可见长期记忆索引和上传文档目录。
3. `RouteMemory`、`RouteDoc`、`RouteTime`、`RouteServer` 判断本轮问题需要哪些证据或工具。
4. `retrieveMemoryForJSONContext` 按路由预取旧记忆证据。
5. `retrieveDocsForJSONContext` 按路由预取文档片段。
6. `contextbuilder.Build` 生成 `ContextJSON`、短系统提示词和调试信息。
7. `AssembleUserContext` 把 JSON Context 包成一个当前用户消息。
8. 若有图片附件，图片和结构化上下文放在同一条 user message 的多模态 parts 中。

## 6. 数据存储架构

SQLite 是主存储，文件系统用于大文件和导出：

| 数据 | 存储位置 |
| --- | --- |
| 会话、消息、记忆、文档元数据、人设、附件元数据、表情包元数据、偏好 | SQLite |
| 聊天图片、表情包图片、上传文档原文件 | `uploads/` |
| 当前会话 JSONL 导出 | `data/conversations/{conversation_id}.jsonl` |
| 事件日志 | `logs/` |

SQLite 使用 WAL 和单连接写入：

```go
sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
DB.SetMaxOpenConns(1)
```

## 7. 会话隔离原则

这是本系统的硬约束：

- `messages`、`message_attachments`、会话内 `memory_chunks` 必须按 `conversation_id` 读写。
- 全局记忆 `scope=global` 可以跨会话读取。
- 当前会话记忆 `scope=conversation` 只能被同一个 `conversation_id` 读取、更新、删除。
- 附件发送前必须校验附件 `conversation_id` 等于当前会话。
- 删除会话不能删除全局记忆。

## 8. 工具架构

工具定义位于 `config/tools.json`，启动后由 `chatTools()` 加载。工具执行在 `internal/api` 内部实现：

- 表情包工具读取 `stickers`。
- 记忆工具读取 `memory_chunks`、`messages`、`knowledge_docs`。
- 文档工具读取 `knowledge_docs.content_text`。
- 时间工具读取服务器时间。
- shell 工具经过命令分类、危险命令拦截、写入根目录限制和超时/输出大小限制。

## 9. 可观测与调试

- `observability.Event` 记录关键事件。
- `/api/context-preview` 提供模型上下文预览。
- `ENABLE_CONTEXT_DEBUG=true` 时，响应中的 `context_debug` 包含：
  - `request_id`
  - `token_estimate`
  - memory/doc/time/server route
  - injected memory ids
  - injected doc chunks
  - filtered sensitive items
  - final context json

## 10. 主要风险

| 风险 | 现有控制 |
| --- | --- |
| 多会话串数据 | 查询和写入使用 `conversation_id`；测试覆盖搜索、记忆读取、会话删除 |
| 上传文档摘要被误当正文 | 文档目录和正文工具分离；JSON Context 明确 metadata is not body |
| 高敏内容默认泄露 | 敏感等级、过滤器、渐进披露策略 |
| shell 写入越界 | `AGENT_SHELL_WRITE_ROOT`、危险命令拦截、读写分类 |
| 上下文不可审计 | Context Preview、Context Debug、结构化 JSON Context |

## 2026-09-17 后端维护接口与统计

统一管理入口复用 Handler 现有安全中间件，不增加页面登录。GET /api/memory 支持 conversation_id、scope、memory_type、status；GET /api/memory/export 返回 JSON 下载并包含审计版本；GET /api/memory/search?conversation_id=&query= 返回检索证据；DELETE /api/memory/{id} 软撤销单条，DELETE /api/memory?memory_type=&conversation_id=&scope= 按类型撤销；POST /api/memory/clear 的 body 必须为 {"confirm":"DELETE_ALL_MEMORIES"}。未创建前端管理页面，也没有调用生产删除或清空。

openrouter 最低层记录 provider usage 和调用延迟，handleChat context 携带统计收集器，工具决策与后续生成共享同 flow_id。SSE 最终空 choices 的 usage 也读取。未报告缓存保持 NULL，部分调用缺缓存时 chat.cache_complete=false。不修改供应商地址、密钥、模型配置或路由。Bark、Web Push、Swift 原生结果任务通知路径保留。

Dockerfile 与固定部署脚本添加 sqlite_fts5 构建标签。数据库迁移之前对 /opt/myapp/memories.db 做 SQLite 在线备份并在副本演练，禁止将旧 data/memories.db 当生产库。
