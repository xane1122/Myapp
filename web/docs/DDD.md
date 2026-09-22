# 领域驱动设计文档

## 1. 领域定位

本系统的核心领域是“有记忆的私人聊天助手”。它不是单纯的聊天 UI，也不是文档管理系统；它的核心能力是把会话、人设、长期记忆、上传资料和工具结果组织成模型可使用、可审计、可控制的上下文。

## 2. 子域划分

| 子域 | 类型 | 说明 |
| --- | --- | --- |
| 聊天会话 | 核心子域 | 组织消息、附件、模型回复和会话生命周期 |
| 上下文构建 | 核心子域 | 决定哪些信息进入模型、以什么形式进入、哪些必须用工具确认 |
| 长期记忆 | 核心子域 | 管理全局/会话记忆、压缩、检索和证据边界 |
| 上传文档 | 支撑子域 | 文档解析、正文保存、索引构建、按需读取 |
| 人设偏好 | 支撑子域 | 常驻角色、关系、风格和边界配置 |
| 表情包 | 通用/增强子域 | 图片资产管理和情绪表达工具 |
| 受控 shell | 支撑子域 | 在授权范围内读取或维护服务器资源 |
| 可观测性 | 通用子域 | 事件日志、健康检查、上下文调试 |

## 3. 限界上下文

### 3.1 Conversation Context

负责用户与助手之间的聊天线程。

聚合根：

- `Conversation`

实体：

- `Message`
- `Attachment`

领域规则：

- `Message` 必须属于一个 `Conversation`。
- `Attachment` 上传时必须绑定 `conversation_id`。
- 发送图片时，附件所属会话必须等于当前会话。
- 删除 `Conversation` 删除会话内消息、附件关联、会话内记忆和 JSONL，不删除全局记忆。

代码位置：

- `internal/memory`
- `internal/api/handleChat`
- `internal/api/handleHistory`
- `internal/api/handleConversations`

### 3.2 Memory Context

负责长期记忆。

聚合根：

- `MemoryChunk`

值对象：

- `MemoryScope`
- `SourceType`
- `Summary`
- `Keywords`
- `Sensitivity`

领域规则：

- `scope=global` 是跨会话可见记忆。
- `scope=conversation` 只对当前 `conversation_id` 可见。
- 旧事实回答必须基于 `search_memory`、`retrieved_memory` 或原始消息证据。
- 记忆索引只用于路由，不代表完整事实。

代码位置：

- `internal/memory`
- `internal/contextbuilder`
- `internal/api/runSearchMemoryTool`

### 3.3 Knowledge Document Context

负责上传文档。

聚合根：

- `KnowledgeDoc`

实体：

- `KnowledgeDocChunk`，当前持久化为 `memory_chunks(source_type=knowledge_doc)`

领域规则：

- 文档目录不是正文。
- 文档正文只有用户明确询问正文、总结、分析、扮演、继续游戏等场景才读取。
- 文本型 PDF 可解析；扫描版 PDF 可能没有可读正文。
- 文档索引是全局可见记忆。

代码位置：

- `internal/api/handleKnowledgeDocs`
- `internal/api/ingestKnowledgeDoc`
- `internal/api/runReadUploadedDocTool`
- `internal/memory`

### 3.4 Persona Context

负责人设与偏好。

聚合根：

- `PersonaProfile`

值对象：

- `Preference`

领域规则：

- 同一时间最多一个激活人设。
- 人设和偏好是常驻上下文，不参与消息压缩。
- 前端追加系统提示词和分类偏好都要进入上下文构建。

代码位置：

- `internal/memory`
- `internal/api/handlePersonas`
- `internal/api/handlePrefs`
- `internal/contextbuilder/BuildUserProfile`

### 3.5 Context Assembly Context

负责将所有领域信息装配成模型输入。

聚合根：

- `ContextJSON`

实体/值对象：

- `AssistantRole`
- `RuntimeRules`
- `ToolPolicy`
- `UserProfile`
- `MemoryPolicy`
- `MemoryIndexItem`
- `RetrievedMemory`
- `UploadedDocumentIndexItem`
- `RetrievedDocChunk`
- `ConversationState`
- `RecentMessage`

领域规则：

- `current_user_message` 必填。
- `memory_index.summary` 必填。
- `uploaded_documents.title` 必填。
- sensitivity 只能是 `low`、`medium`、`high`。
- `retrieved_memory` 和 `retrieved_doc_chunks` 才是旧事实/文档正文证据。

代码位置：

- `internal/contextbuilder`
- `internal/api/buildJSONContextMessages`

### 3.6 Sticker Context

负责表情包资产。

聚合根：

- `Sticker`

领域规则：

- 只有 `enabled=1` 的表情包可被工具使用。
- 删除表情包是软删除。
- 识别失败或无 API Key 时需要人工复核。

代码位置：

- `internal/memory`
- `internal/api/handleStickers`
- `internal/api/runStickerTool`

### 3.7 Tool Execution Context

负责模型可调用工具的执行和边界。

聚合根：

- `ToolCall`

领域规则：

- 工具定义来自 `config/tools.json`。
- 旧记录、文档正文、当前时间、服务器状态必须使用对应工具确认。
- shell 写入必须受 `AGENT_SHELL_WRITE_ROOT` 约束。

代码位置：

- `internal/api/chatTools`
- `internal/api/chatToolChoice`
- `internal/api/resolveToolCalls`
- `internal/api/runChatTool`

## 4. 聚合关系

```text
Conversation
  ├── Message
  ├── Attachment
  └── MemoryChunk(scope=conversation)

KnowledgeDoc
  └── MemoryChunk(source_type=knowledge_doc, scope=global)

PersonaProfile
Preference
Sticker
ContextJSON
```

注意：`MemoryChunk(scope=global)` 不属于某个会话聚合，虽然当前表结构中仍有 `conversation_id` 字段。它的可见性由 `scope` 决定。

## 5. 统一语言

| 术语 | 含义 |
| --- | --- |
| 会话 | 一个聊天线程，对应 `conversations` |
| 消息 | 用户或助手的一条聊天文本，对应 `messages` |
| 附件 | 聊天图片，对应 `message_attachments` |
| 长期记忆 | 可被后续检索的文本块，对应 `memory_chunks` |
| 全局记忆 | 跨会话可见的长期记忆，`scope=global` |
| 当前会话记忆 | 只在当前会话可见的长期记忆，`scope=conversation` |
| 文档目录 | 上传文档元数据和摘要，不是正文 |
| 文档正文 | `knowledge_docs.content_text` 或按块预取的正文 |
| 记忆索引 | 给模型判断是否需要检索的摘要/关键词 |
| 检索证据 | `search_memory`、`read_uploaded_doc` 或 JSON Context 预取结果 |
| 高敏内容 | 默认不注入正文的私密或身份敏感内容 |
| 上下文预览 | 下一次请求发给模型的消息和工具预览 |

## 6. 领域服务

| 领域服务 | 职责 | 当前实现 |
| --- | --- | --- |
| ConversationService | 确保会话存在、同步 JSONL、标题生成、删除会话 | `memory.EnsureConversation` 等函数 |
| MemoryService | 保存、搜索、列出、编辑、删除长期记忆 | `internal/memory` 函数 |
| DocumentIngestionService | 文档解析、切块、摘要、索引写入 | `ingestKnowledgeDoc` |
| ContextAssemblyService | 构建 legacy prompt 或 JSON Context | `buildSystemPrompt`、`contextbuilder.Build` |
| ToolExecutionService | 工具分发、执行和二次模型调用 | `resolveToolCalls`、`runChatTool` |
| SensitivityPolicyService | 敏感分类和摘要过滤 | `contextbuilder/filter.go` |

这些服务当前以函数方式存在，没有额外接口层。短期内保持这种简单结构更符合项目规模。

## 7. 领域事件

当前领域事件以日志事件形式存在，不是持久化事件流。

| 事件 | 触发点 |
| --- | --- |
| `chat.request` | 收到聊天请求 |
| `chat.user_saved` | 用户消息保存成功 |
| `chat.assistant_saved` | 助手消息保存成功 |
| `chat.attachment_wrong_conversation` | 附件串会话 |
| `context_builder.built` | JSON Context 构建成功 |
| `context_builder.failed` | JSON Context 构建失败 |
| `knowledge_doc.index_repaired` | 文档索引回填成功 |
| `sticker.updated` | 表情包元数据更新 |

## 8. 不变量

1. `conversation_id` 是会话内数据隔离边界。
2. 会话删除不得删除 `scope=global` 的长期记忆。
3. 旧事实不能只凭 `memory_index` 回答。
4. 上传文档目录不能当作正文。
5. 高敏正文默认不进入模型上下文。
6. shell 写入不能越过授权根目录。
7. 工具结果必须来自实际后端执行，不能伪造。

## 9. 当前模型与未来演进

当前 DDD 落地方式偏轻量：聚合和领域服务主要通过 Go 包和函数边界体现，而不是引入复杂的 repository/interface/application-service 分层。未来只有在出现多存储后端、多用户权限、多模型策略或复杂异步任务时，才建议进一步拆分接口和应用服务层。

## 2026-09-17 版本化记忆与管理边界

新记忆写入事务先取得 SQLite 写锁，同 assistant、scope、source_type、memory_type 的事实/偏好，至少两个关键词重叠且 Jaccard>=0.75、主题标签一致时，写入新行并把旧行设 superseded，superseded_by 指向新 ID。精确重复复用旧 ID。关键词只是保守启发式，并非语义矛盾证明；不能保证识别所有事实冲突。文档分块、历史摘要、日记与情绪记录不走关键词替换。全局记忆仍按角色隔离，会话记忆只对所属会话可见。

管理删除默认软撤销为 revoked，可通过 JSON 导出审计版本；只有 /api/memory/clear 的明确 DELETE_ALL_MEMORIES 确认执行物理清空。此接口不在维护验证中对生产调用。scope=conversation 是私有记录；scope=all 配 conversation_id 才包含同角色全局记录，非法范围拒绝。保持现有永久免登录页面和精确 Origin 防护。
