package contextbuilder

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"myapp/internal/memory"
)

func TestBuildRejectsEmptyCurrentUserMessage(t *testing.T) {
	_, err := Build(Input{CurrentUserMessage: "  "})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestBuildAddsCoreAndActiveMemoryWithoutRetrievedDuplicates(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage: "你又这样，我不是说过别反问了吗",
		MemoryChunks: []memory.Chunk{
			{ID: 1, Scope: memory.ScopeGlobal, SourceType: "manual", Summary: "用户生气时不要反问，要具体道歉", Keywords: "生气,反问,道歉,雷区"},
			{ID: 2, Scope: memory.ScopeGlobal, SourceType: "manual", Summary: "睡前要主动陪伴", Keywords: "睡前,陪伴,仪式"},
		},
		RetrievedMemory: []RetrievedMemory{{MemoryID: 1, Content: "用户生气时不要反问，要具体道歉"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range res.Context.ActiveMemory {
		if item.MemoryID == 1 {
			t.Fatalf("retrieved memory duplicated in active memory: %#v", item)
		}
	}
	for _, core := range res.Context.CoreMemory {
		for _, active := range res.Context.ActiveMemory {
			if core.MemoryID == active.MemoryID {
				t.Fatalf("active memory duplicated in core memory: %#v", core)
			}
		}
		for _, retrieved := range res.Context.RetrievedMemory {
			if core.MemoryID == retrieved.MemoryID {
				t.Fatalf("retrieved memory duplicated in core memory: %#v", core)
			}
		}
	}
}

func TestBuildCoreMemorySelectsStableBoundary(t *testing.T) {
	items := BuildCoreMemory([]memory.Chunk{{ID: 8, Scope: memory.ScopeGlobal, SourceType: "manual", Summary: "安全词和边界必须优先", Keywords: "安全词,边界"}}, NewSensitivityFilter(), 5)
	if len(items) != 1 || items[0].MemoryID != 8 {
		t.Fatalf("unexpected core memory: %#v", items)
	}
}

func TestBuildActiveMemoryUsesEmotionalContinuitySignal(t *testing.T) {
	items := BuildActiveMemory("随便你，你又这样", "", []memory.Chunk{{ID: 9, Scope: memory.ScopeConversation, SourceType: "manual", Summary: "用户失望时先安抚并具体道歉", Keywords: "失望,安抚,道歉,关系"}}, NewSensitivityFilter(), 5)
	if len(items) != 1 || items[0].MemoryID != 9 {
		t.Fatalf("unexpected active memory: %#v", items)
	}
}

func TestBuildMemoryIndexDeduplicatesEquivalentSummaries(t *testing.T) {
	items, _ := BuildMemoryIndex([]memory.Chunk{{ID: 1, Summary: "用户喜欢绿茶。"}, {ID: 2, Summary: "  用户喜欢：绿茶  "}}, NewSensitivityFilter())
	if len(items) != 1 {
		t.Fatalf("expected equivalent summaries to be deduplicated, got %#v", items)
	}
}

func TestBuildFiltersHighSensitivityMemoryAndDocs(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage: "那份 PDF 里写了什么？",
		Now:                time.Date(2026, 6, 21, 2, 18, 0, 0, time.FixedZone("CST", 8*3600)),
		MemoryChunks: []memory.Chunk{
			{ID: 1, Scope: memory.ScopeGlobal, SourceType: "manual", Summary: "Go 项目偏好", Content: "喜欢 go test ./...", Keywords: "Go,测试"},
			{ID: 2, Scope: memory.ScopeGlobal, SourceType: "knowledge_doc", SourceDocID: sql.NullInt64{Int64: 9, Valid: true}, Summary: "私人亲密文档", Content: "亲密正文不应默认出现", Keywords: "私人,爸爸"},
		},
		KnowledgeDocs: []memory.KnowledgeDoc{
			{ID: 9, Title: "私人 PDF", ContentText: "爸爸 私人 亲密正文", Status: "ready"},
			{ID: 10, Title: "Karpathy-CLAUDE", ContentText: "AI 编程 agent guideline", Status: "ready"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Context.MemoryIndex) != 2 {
		t.Fatalf("memory index len = %d", len(res.Context.MemoryIndex))
	}
	high := res.Context.MemoryIndex[1]
	if high.Sensitivity != SensitivityHigh || high.DefaultInject {
		t.Fatalf("high sensitivity memory not filtered: %#v", high)
	}
	if strings.Contains(high.Summary, "亲密正文") {
		t.Fatalf("high sensitivity summary leaked content: %q", high.Summary)
	}
	if res.Context.UploadedDocuments[0].Sensitivity != SensitivityHigh {
		t.Fatalf("expected high sensitivity doc, got %#v", res.Context.UploadedDocuments[0])
	}
	if strings.Contains(res.Context.UploadedDocuments[0].Summary, "亲密正文") {
		t.Fatalf("high sensitivity doc summary leaked content: %q", res.Context.UploadedDocuments[0].Summary)
	}
	if len(res.Debug.FilteredSensitiveItems) == 0 {
		t.Fatal("expected filtered sensitive debug entries")
	}
}

func TestBuildRedactsHighSensitivityConversationIndex(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage: "普通聊天",
		MemoryChunks: []memory.Chunk{
			{
				ID:         11,
				Scope:      memory.ScopeConversation,
				SourceType: "conversation",
				Summary:    "user: 可以嗦奶头吗\nassistant: 先过来。",
				Content:    "亲密会话正文不应进入索引",
				Keywords:   "奶头,嗦,跨坐,亲密正文",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Context.MemoryIndex) != 1 {
		t.Fatalf("memory index len = %d", len(res.Context.MemoryIndex))
	}
	item := res.Context.MemoryIndex[0]
	if item.Sensitivity != SensitivityHigh || item.DefaultInject {
		t.Fatalf("expected high sensitivity redacted item, got %#v", item)
	}
	for _, leaked := range []string{"奶头", "嗦", "跨坐", "亲密会话正文"} {
		if strings.Contains(item.Title, leaked) || strings.Contains(item.Summary, leaked) || strings.Contains(strings.Join(item.Keywords, ","), leaked) {
			t.Fatalf("high sensitivity index leaked %q: %#v", leaked, item)
		}
	}
}

func TestBuildCanDisableSensitivityFilterForPrivateRoleplayContinuity(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage:       "继续刚才的男朋友聊天",
		DisableSensitivityFilter: true,
		MemoryChunks: []memory.Chunk{
			{
				ID:         21,
				Scope:      memory.ScopeConversation,
				SourceType: "conversation",
				Summary:    "user: 我想让 Claude 扮演男朋友，也要记得四级考试和抽象表情包\nassistant: 我会记住。",
				Content:    "亲密会话正文可以保留为私人助手上下文",
				Keywords:   "奶头,嗦,跨坐,男朋友,四级,表情包",
			},
		},
		KnowledgeDocs: []memory.KnowledgeDoc{
			{ID: 22, Title: "私人 PDF", ContentText: "爸爸 私人 亲密正文", Status: "ready"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Context.MemoryIndex) != 1 {
		t.Fatalf("memory index len = %d", len(res.Context.MemoryIndex))
	}
	item := res.Context.MemoryIndex[0]
	if item.Sensitivity != SensitivityLow || !item.DefaultInject {
		t.Fatalf("expected unguarded memory to be default-injected low sensitivity, got %#v", item)
	}
	for _, want := range []string{"关系/角色称呼偏好", "表情包和表达偏好", "生活牵挂点", "亲密互动偏好"} {
		if !strings.Contains(item.Summary+strings.Join(item.Keywords, ","), want) {
			t.Fatalf("unguarded memory lost distilled cue %q: %#v", want, item)
		}
	}
	for _, want := range []string{"Claude 扮演男朋友", "四级考试", "抽象表情包"} {
		if !strings.Contains(item.Summary, want) {
			t.Fatalf("unguarded memory lost topic hint %q: %#v", want, item)
		}
	}
	for _, leaked := range []string{"user:", "assistant:", "奶头", "嗦", "跨坐", "亲密会话正文"} {
		if strings.Contains(item.Title, leaked) || strings.Contains(item.Summary, leaked) || strings.Contains(strings.Join(item.Keywords, ","), leaked) {
			t.Fatalf("unguarded memory index leaked raw transcript detail %q: %#v", leaked, item)
		}
	}
	if len(res.Debug.FilteredSensitiveItems) != 0 {
		t.Fatalf("disabled sensitivity filter should not report filtered items: %#v", res.Debug.FilteredSensitiveItems)
	}
	if res.Context.RuntimeRules.DoNotExposeHighSensitivityByDefault {
		t.Fatalf("runtime rules should reflect disabled sensitivity filtering: %#v", res.Context.RuntimeRules)
	}
	if !strings.Contains(res.Context.MemoryPolicy.DefaultBehavior, "敏感过滤已关闭") {
		t.Fatalf("memory policy should disclose disabled filtering: %#v", res.Context.MemoryPolicy)
	}
	if !res.Context.MemoryPolicy.DefaultUseIntimateMemory ||
		res.Context.MemoryPolicy.DefaultInjectRawIntimateTranscript ||
		!res.Context.MemoryPolicy.DefaultInjectDistilledIntimatePreferences ||
		!res.Context.MemoryPolicy.RespectSafetyWord {
		t.Fatalf("unexpected unguarded memory policy: %#v", res.Context.MemoryPolicy)
	}
	if got := res.Context.UploadedDocuments[0]; got.Sensitivity != SensitivityLow || strings.Contains(got.Summary, "亲密正文") {
		t.Fatalf("unguarded doc summary should be an index, not raw content: %#v", got)
	}
}

func TestBuildSystemPromptIncludesCategorizedPrefsAsPermanentRules(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage: "普通聊天",
		BasePrompt:         "基础工具规则",
		FrontendPrompt:     "linux_shell 有写入权限，只限 /opt/myapp",
		PersonaContent:     "主动承接用户情绪",
		ProfilePrefs: map[string]string{
			"assistant_name": "克",
			"user_name":      "小宝",
			"relationship":   "私人恋爱陪伴关系",
			"tone":           "自然稳定认真",
			"likes":          "被确认唯一",
			"dislikes":       "敷衍短句",
			"boundaries":     "安全词是好爸爸",
			"memory_policy":  "重要亲密偏好不压缩",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"【基础系统提示词】",
		"基础工具规则",
		"【用户追加系统提示词（常驻不压缩）】",
		"linux_shell 有写入权限，只限 /opt/myapp",
		"【最高优先级人物设定（常驻不压缩）】",
		"主动承接用户情绪",
		"【分类人物设定（用户填写，常驻不压缩）】",
		"【关系定位】：私人恋爱陪伴关系",
		"【长期记忆保留规则】：重要亲密偏好不压缩",
	} {
		if !strings.Contains(res.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, res.SystemPrompt)
		}
	}
}

func TestUnguardedConversationTopicHintKeepsIntimatePreferenceWords(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage:       "普通聊天",
		DisableSensitivityFilter: true,
		MemoryChunks: []memory.Chunk{
			{
				ID:         23,
				Scope:      memory.ScopeConversation,
				SourceType: "conversation",
				Summary:    "user: 喜欢抓胸和传教士，也聊过 daddy 多说 sweet talk\nassistant: 记住了。",
				Keywords:   "抓胸,传教士,daddy,sweet talk",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := res.Context.MemoryIndex[0]
	for _, want := range []string{"抓胸", "传教士", "daddy", "sweet talk"} {
		if !strings.Contains(item.Summary, want) {
			t.Fatalf("topic hint should keep intimate preference word %q: %#v", want, item)
		}
	}
	for _, leaked := range []string{"user:", "assistant:"} {
		if strings.Contains(item.Summary, leaked) {
			t.Fatalf("topic hint should strip transcript role marker %q: %#v", leaked, item)
		}
	}
}

func TestBuildCapsMemoryKeywords(t *testing.T) {
	res, err := Build(Input{
		CurrentUserMessage: "普通聊天",
		MemoryChunks: []memory.Chunk{
			{
				ID:         12,
				Scope:      memory.ScopeGlobal,
				SourceType: "manual",
				Summary:    "普通 Go 项目偏好",
				Keywords:   "one,two,three,four,five,six,seven,eight,nine,ten,eleven,twelve,thirteen,abcdefghijklmnopqrstuvwxyz",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	keywords := res.Context.MemoryIndex[0].Keywords
	if len(keywords) != 12 {
		t.Fatalf("keyword count = %d, want 12: %#v", len(keywords), keywords)
	}
	for _, kw := range keywords {
		if len([]rune(kw)) > 27 {
			t.Fatalf("keyword was not capped: %q", kw)
		}
	}
}

func TestBuildRecentMessagesCoalescesConsecutiveUserMessagesBeforeLimit(t *testing.T) {
	messages := []memory.Message{
		{ID: 1, Role: "assistant", Content: "上一轮回复"},
		{ID: 2, Role: "user", Content: "设定第一条"},
		{ID: 3, Role: "user", Content: "设定第二条"},
		{ID: 4, Role: "user", Content: "设定第三条"},
		{ID: 5, Role: "assistant", Content: "收到设定"},
	}

	recent := BuildRecentMessages(messages, 3)
	if len(recent) != 3 {
		t.Fatalf("logical recent messages=%d, want 3: %#v", len(recent), recent)
	}
	if recent[1].Role != "user" || recent[1].MessageID != 4 || recent[1].Content != "设定第一条\n设定第二条\n设定第三条" {
		t.Fatalf("consecutive user messages were not coalesced: %#v", recent[1])
	}
}

func TestBuildRecentMessagesLimitsLogicalMessagesAfterCoalescing(t *testing.T) {
	messages := []memory.Message{
		{ID: 1, Role: "assistant", Content: "应被截掉"},
		{ID: 2, Role: "user", Content: "分条一"},
		{ID: 3, Role: "user", Content: "分条二"},
		{ID: 4, Role: "assistant", Content: "最近回复"},
	}

	recent := BuildRecentMessages(messages, 2)
	if len(recent) != 2 || recent[0].Content != "分条一\n分条二" || recent[1].Content != "最近回复" {
		t.Fatalf("logical limit applied before coalescing: %#v", recent)
	}
}

func TestBuildRecentMessagesKeepsTailOfLongUserBatch(t *testing.T) {
	messages := []memory.Message{
		{ID: 1, Role: "user", Content: strings.Repeat("前", 4000)},
		{ID: 2, Role: "user", Content: strings.Repeat("后", 4000)},
	}
	recent := BuildRecentMessages(messages, 20)
	if len(recent) != 1 || len([]rune(recent[0].Content)) != 6000 {
		t.Fatalf("long user batch was not bounded: %#v", recent)
	}
	if !strings.HasPrefix(recent[0].Content, "前") || !strings.HasSuffix(recent[0].Content, "后") || !strings.Contains(recent[0].Content, "中间已截断") {
		t.Fatalf("long user batch did not preserve both ends")
	}
}

func TestAssembleUserContextIsCompactAndDoesNotDuplicateCurrentMessage(t *testing.T) {
	res, err := Build(Input{CurrentUserMessage: "Go interface nil 是什么？"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := AssembleUserContext(res.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<context_json>", `"current_user_message":"Go interface nil 是什么？"`, "runtime_rules、tool_policy 和 memory_policy"} {
		if !strings.Contains(out, want) {
			t.Fatalf("assembled context missing %q: %s", want, out)
		}
	}
	if strings.Count(out, "Go interface nil 是什么？") != 1 {
		t.Fatalf("current user message was duplicated: %s", out)
	}
	if strings.Contains(out, "<current_user_message>") || strings.Contains(out, "\n  \"") {
		t.Fatalf("assembled context was not compact: %s", out)
	}
}

func TestToolPolicyMentionsDeletionViaLinuxShell(t *testing.T) {
	policy := BuildToolPolicy()
	if policy.ServerQuery.RequiredTool != "linux_shell" {
		t.Fatalf("expected linux_shell server tool, got %#v", policy.ServerQuery)
	}
	joined := strings.Join(policy.ServerQuery.When, ",") + policy.ServerQuery.Rule
	for _, want := range []string{"删除上传文档", "删除长期记忆", "核验结果"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("server tool policy missing %q: %#v", want, policy.ServerQuery)
		}
	}
}

func TestValidateRequiresMemorySummary(t *testing.T) {
	ctx := ContextJSON{
		CurrentUserMessage: "hello",
		MemoryIndex:        []MemoryIndexItem{{MemoryID: 1, Sensitivity: SensitivityLow}},
	}
	if err := Validate(ctx); err == nil {
		t.Fatal("expected validation error for missing memory summary")
	}
}
