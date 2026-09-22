# Context 详细设计

## 1. 目标

Context 详细设计定义“本轮请求发给模型的内容”如何构建、路由、过滤、调试和降级。

核心目标：

- 降低大段混合系统提示词带来的不可审计风险。
- 明确区分系统规则、用户画像、记忆索引、检索证据、文档目录、文档正文、最近消息和当前用户消息。
- 对旧事实、文档正文、当前时间、服务器状态使用确定性工具或后端预取。
- 对高敏内容做渐进披露。

## 2. 两条上下文路径

### 2.1 Legacy Prompt

默认路径，开关：

```text
ENABLE_JSON_CONTEXT_BUILDER=false
```

消息形态：

```json
[
  {"role": "system", "content": "混合系统提示词"},
  {"role": "user", "content": "历史用户消息"},
  {"role": "assistant", "content": "历史助手消息"},
  {"role": "user", "content": "当前用户消息"}
]
```

系统提示词来源：

- 激活人设。
- `config/system_prompt.md`。
- 偏好 `system_prompt`。
- 分类偏好：助手名、用户名、关系、语气、喜欢、不喜欢、边界、记忆规则。
- 上传文档目录。
- 长期记忆索引。
- 当前会话上下文修正 `context_note.{conversation_id}`。
- 相关长期记忆搜索结果。
- 当前时间快照。

### 2.2 JSON Context

新路径，开关：

```text
ENABLE_JSON_CONTEXT_BUILDER=true
```

消息形态：

```json
[
  {"role": "system", "content": "短系统提示词"},
  {"role": "user", "content": "请根据以下结构化上下文回答用户...<context_json>...</context_json><current_user_message>...</current_user_message>"}
]
```

图片场景：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "结构化上下文文本"},
    {"type": "image_url", "image_url": {"url": "data:image/..."}}
  ]
}
```

## 3. 输入数据

`contextbuilder.Input` 由 `internal/api/buildJSONContextMessages` 组装。

| 输入 | 来源 |
| --- | --- |
| `ConversationID` | `/api/chat` 或 `/api/context-preview` 请求 |
| `CurrentUserMessage` | 当前输入文本 |
| `Now` | 服务器当前时间 |
| `Locale` | 当前固定为 `zh-CN` |
| `BasePrompt` | `config/system_prompt.md` 或默认提示词 |
| `FrontendPrompt` | `preferences.system_prompt` |
| `PersonaContent` | `GetActivePersona` |
| `ProfilePrefs` | preferences 中的用户画像字段 |
| `MemoryChunks` | `ListChunksByScope(conversationID, "all", memoryIndexLimit())` |
| `RetrievedMemory` | MemoryRouter 预取结果 |
| `KnowledgeDocs` | `ListKnowledgeDocs()` |
| `RetrievedDocChunks` | DocRouter 预取结果 |
| `RecentMessages` | `GetHistory(conversationID, maxHistory)` |
| `MaxRecentMessages` | `MAX_RECENT_MESSAGES` |
| `DisableSensitivityFilter` | `!ENABLE_SENSITIVITY_FILTER` |

## 4. 输出结构

顶层 JSON：

```json
{
  "context_version": "1.0.0",
  "request_id": "hex",
  "generated_at": "RFC3339",
  "locale": "zh-CN",
  "assistant_role": {},
  "runtime_rules": {},
  "tool_policy": {},
  "user_profile": {},
  "memory_policy": {},
  "memory_index": [],
  "retrieved_memory": [],
  "uploaded_documents": [],
  "retrieved_doc_chunks": [],
  "conversation_state": {},
  "recent_messages": [],
  "current_user_message": ""
}
```

字段边界：

| 字段 | 边界 |
| --- | --- |
| `memory_index` | 路由索引，不是完整事实 |
| `retrieved_memory` | 可作为旧事实证据 |
| `uploaded_documents` | 文档目录，不是正文 |
| `retrieved_doc_chunks` | 可作为上传文档正文证据 |
| `recent_messages` | 当前会话最近原始消息，可能截断 |
| `current_user_message` | 本轮用户问题，是路由和回答的主意图 |

## 5. 构建流程

```text
buildJSONContextMessages
  -> ListChunksByScope(conversationID, "all", memoryIndexLimit)
  -> ListKnowledgeDocs
  -> BuildMemoryIndex
  -> BuildUploadedDocIndex
  -> RouteMemory / RouteDoc
  -> retrieveMemoryForJSONContext
  -> retrieveDocsForJSONContext
  -> 读取 prefs 和 active persona
  -> contextbuilder.Build
       -> BuildMemoryIndex
       -> BuildUploadedDocIndex
       -> BuildRecentMessages
       -> BuildAssistantRole
       -> BuildRuntimeRules
       -> BuildToolPolicy
       -> BuildUserProfile
       -> BuildMemoryPolicy
       -> Validate
       -> DebugInfo
  -> AssembleUserContext
  -> 生成 ChatMessage
```

注意：当前实现会在 `buildJSONContextMessages` 中先构建一次索引用于路由，随后 `contextbuilder.Build` 再构建最终索引。这保证路由和最终上下文使用相同分类逻辑，但有重复计算。

## 6. 路由设计

### 6.1 MemoryRoute

触发条件：

- 用户询问“还记得”“以前”“之前”“上次”“昨天”“刚才”“我说过”等。
- 用户询问记忆清单。
- 当前问题命中 `memory_index` 的 `memory_id`、标题或关键词。

输出：

```json
{
  "need_search_memory": true,
  "target_memory_ids": [1],
  "query": "用户原问题",
  "reason": "用户引用旧对话或旧记忆"
}
```

预取规则：

- 有 `target_memory_ids` 时按 ID 读取，但必须校验 scope/conversation：
  - `scope=global` 允许。
  - 非 global 必须 `c.ConversationID == conversationID`。
- 用户询问记忆列表时读取可见记忆列表。
- 其他情况用 `SearchChunks(conversationID, query, limit)`。

### 6.2 DocRoute

触发条件分两层：

1. 句子含文档意图：文档、文件、PDF、doc、file 等。
2. 同时含正文处理意图：写了什么、正文、内容、读取、根据、分析、总结、play game、游戏、扮演等。

预取规则：

- 如果问题命中文档标题，选中对应文档。
- 如果只有一个文档，默认选中该文档。
- 如果是“那份文档/这个 PDF”这类泛指，按可读文档兜底选择最多 2 个。
- 每个文档正文按 `docExcerpt` 截取，最大由 `MAX_RETRIEVED_DOC_TOKENS` 控制。

### 6.3 TimeRoute

触发实时日期时间问题：

- 现在几点
- 当前时间
- 今天几号
- 星期几/周几
- current time/current date

JSON Context 只给出路由和工具策略；实际强制工具仍由 `chatToolChoice` 配合工具定义完成。

### 6.4 ServerRoute

触发服务器或命令问题：

- linux shell
- 执行命令
- 读取文件
- 部署目录
- `/opt/myapp`
- 删除上传文档/长期记忆/表情包/人设
- 常见 shell 命令前缀

## 7. 敏感过滤设计

### 7.1 敏感等级

| 等级 | 说明 |
| --- | --- |
| `low` | 普通偏好、技术文档、非私密配置 |
| `medium` | 关系风格、情绪偏好、长期项目、重要个人事件 |
| `high` | 私密内容、私人文档、身份敏感数据、强私人化细节 |

### 7.2 记忆过滤

`BuildMemoryIndex` 对每个 `memory.Chunk`：

1. `ClassifyMemory` 分类。
2. 若过滤关闭，强制视为 `low`，但对对话逐字稿或高敏内容做提炼摘要。
3. 生成 `summary`、`title`、`keywords`、`default_inject`、`retrieve_when`。
4. 高敏内容进入 `filtered_sensitive_items`。

默认策略：

- `low`：摘要最多约 220 字符。
- `medium`：摘要最多约 120 字符。
- `high`：只给安全索引文案，不给原文关键词。

### 7.3 文档过滤

`BuildUploadedDocIndex` 对每个 `KnowledgeDoc`：

1. `ClassifyDoc` 分类。
2. 高敏文档只给安全摘要。
3. 所有文档都标记 `requires_read_uploaded_doc=true`。

## 8. 检索注入设计

### 8.1 retrieved_memory

字段：

| 字段 | 说明 |
| --- | --- |
| `memory_id` | 记忆 ID |
| `source_type` | 来源类型 |
| `confidence` | 当前固定为 high |
| `content` | 注入证据 |
| `evidence_summary` | 证据来源说明 |
| `sensitivity` | 敏感等级 |
| `allowed_usage` | 使用限制 |

内容截断：

- `high`：最多约 140 字符安全摘要。
- `medium`：最多 `min(260, maxRetrievedMemoryChars)`。
- `low`：最多 `min(500, maxRetrievedMemoryChars)`。
- `knowledge_doc` 且原始高敏时，只注入禁用过滤器生成的安全摘要。

### 8.2 retrieved_doc_chunks

字段：

| 字段 | 说明 |
| --- | --- |
| `doc_id` | 文档 ID |
| `title` | 标题 |
| `chunk_id` | 当前格式 `{docID}:prefetch` |
| `content` | 文档正文片段 |
| `reason` | 预取原因 |
| `sensitivity` | 敏感等级 |

如果文档无可读正文，`content` 写入明确说明。

## 9. 校验规则

`Validate(ctx)` 当前校验：

- `current_user_message` 不为空。
- 每个 `memory_index` item 必须有 `summary`。
- memory sensitivity 必须为 `low`、`medium`、`high`。
- 每个上传文档必须有 `title`。
- doc sensitivity 必须为 `low`、`medium`、`high`。

校验失败时 JSON Context 构建失败，调用方记录事件并回退到 legacy prompt。

## 10. DebugInfo

`contextbuilder.DebugInfo`：

| 字段 | 说明 |
| --- | --- |
| `request_id` | 本次上下文 ID |
| `token_estimate` | 粗略 token 估算 |
| `memory_route` | 记忆路由结果 |
| `doc_route` | 文档路由结果 |
| `time_route` | 时间路由结果 |
| `server_route` | 服务器路由结果 |
| `injected_memory_ids` | 注入的记忆 ID |
| `injected_doc_chunks` | 注入的文档 chunk ID |
| `filtered_sensitive_items` | 被过滤的敏感项 |
| `final_context_json` | 最终 JSON Context |

对外可见性：

- 只有 `ENABLE_CONTEXT_DEBUG=true` 时返回给前端。
- `ENABLE_MEMORY_ROUTER=false` 时，debug 中 MemoryRoute 会改写为关闭原因。
- `ENABLE_SENSITIVITY_FILTER=false` 时，`filtered_sensitive_items` 置空。

## 11. 上下文预览

`/api/context-preview` 在 JSON Context 开启时：

1. 使用 `contextPreviewDraftForBuilder` 处理空输入。
2. 调用 `buildJSONContextMessages`。
3. 用 `formatContextPreviewFromMessages` 展示实际 messages。
4. 用 `estimateMessagesUsage` 估算结构化消息 token。

这条链路用于人工检查“下一次请求到底会发什么给模型”。

## 12. 配置项

| 配置项 | 作用 | 边界 |
| --- | --- | --- |
| `ENABLE_JSON_CONTEXT_BUILDER` | 启用 JSON Context | false 时走 legacy |
| `ENABLE_MEMORY_ROUTER` | 启用后端记忆预取 | false 时不注入 retrieved_memory |
| `ENABLE_SENSITIVITY_FILTER` | 启用敏感过滤 | false 时重视陪伴连续感但仍避免原始逐字稿 |
| `ENABLE_CONTEXT_DEBUG` | 返回 debug | 生产环境谨慎开启 |
| `MAX_RECENT_MESSAGES` | JSON 最近消息数量 | 不超过 `maxHistory` |
| `MAX_RETRIEVED_MEMORY_ITEMS` | 预取记忆条数 | 最大 8 |
| `MAX_RETRIEVED_MEMORY_TOKENS` | 记忆证据字符预算 | 约 token*2，范围 400 到 6000 字符 |
| `MAX_RETRIEVED_DOC_TOKENS` | 文档证据字符预算 | 约 token*2，范围 800 到 12000 字符 |

## 13. 已知限制

- 敏感分类当前是关键词规则，不是模型分类或人工标注。
- 检索是 LIKE/tokenize，不是 embedding 语义检索。
- JSON Context 路由和最终构建有重复索引构建。
- `ConversationState` 当前主要基于当前消息生成，尚未形成稳定的任务状态管理。
- 文档正文预取最多选择 2 个文档，复杂多文档问题可能需要工具二次确认。

## 2026-09-17 分层、检索与预算

固定 system 前缀只含稳定规则/人设/资料/偏好；当日任务等动态规则进入用户 JSON。ContextJSON 顺序为固定资料、近期原文、相关检索证据、历史摘要/目录；现有敏感度过滤继续作用于 content 和原文片段，失效版本与其他角色记忆不得进入模型上下文。world-book 注入后保持固定 system 前缀在首位，保留角色后缀与现有工具能力。

三个 SearchChunks 调用统一到 FTS5。>=3 字符词使用 trigram MATCH、BM25 与 snippet；角色、会话、active/status、source_type 在 LIMIT 前过滤。短于 3 字符的查询使用同索引内容的转义子串匹配，明确 rank_method=short_substring，不冒充 BM25。证据返回 source_date、original_snippet、match_score、rank_method。管理导出可读取历史版本；模型 read_memory_by_id 同样验证 active/status、角色和会话。

CONTEXT_TOKEN_BUDGET 默认 32768，可选环境配置但本次不修改生产环境文件。预算保守估计 ASCII 每 3 字符一个 token、非 ASCII 每字符 2 个，图片预留 4096；它不是 provider 计费 token。超预算先去历史摘要/目录，再去低排名证据，最后去最旧近期原文；固定资料和本轮输入不截断。预留工具 schema、文件正文、图片、world-book 和角色后缀；每次实际工具后续模型调用再次检查完整输入，超限明确报错，不静默退回无预算 legacy 构建。
