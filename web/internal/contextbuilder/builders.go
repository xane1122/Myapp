package contextbuilder

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"myapp/internal/memory"
)

type Input struct {
 TokenBudget int
 ReservedTokens int
 DynamicRules string
	ConversationID           int64
	CurrentUserMessage       string
	Now                      time.Time
	Locale                   string
	BasePrompt               string
	FrontendPrompt           string
	PersonaContent           string
	ProfilePrefs             map[string]string
	MemoryChunks             []memory.Chunk
	RetrievedMemory          []RetrievedMemory
	KnowledgeDocs            []memory.KnowledgeDoc
	RetrievedDocChunks       []RetrievedDocChunk
	RecentMessages           []memory.Message
	MaxRecentMessages        int
	DisableSensitivityFilter bool
}

type Result struct {
	SystemPrompt string
	Context      ContextJSON
	Debug        DebugInfo
}

func Build(input Input) (Result, error) {
	if strings.TrimSpace(input.Locale) == "" {
		input.Locale = "zh-CN"
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	requestID := newRequestID()
	filter := NewSensitivityFilter()
	if input.DisableSensitivityFilter {
		filter = NewDisabledSensitivityFilter()
	}
	memoryIndex, filteredMemory := BuildMemoryIndex(input.MemoryChunks, filter)
	coreMemory := BuildCoreMemory(input.MemoryChunks, filter, 5)
	activeMemory := BuildActiveMemory(input.CurrentUserMessage, recentMessagesText(input.RecentMessages), input.MemoryChunks, filter, 5)
	activeMemory = excludeRetrievedMemory(activeMemory, input.RetrievedMemory)
	coreMemory = excludeRetrievedMemory(coreMemory, appendMemoryItems(activeMemory, input.RetrievedMemory))
	uploadedDocs, filteredDocs := BuildUploadedDocIndex(input.KnowledgeDocs, filter)
	recent := BuildRecentMessages(input.RecentMessages, input.MaxRecentMessages)
	ctx := ContextJSON{
		ContextVersion:     "1.1.0",
		RequestID:          requestID,
		GeneratedAt:        input.Now.Format(time.RFC3339),
		Locale:             input.Locale,
		AssistantRole:      BuildAssistantRole(input.ProfilePrefs),
		RuntimeRules:       BuildRuntimeRules(!input.DisableSensitivityFilter),
		ToolPolicy:         BuildToolPolicy(),
		UserProfile:        BuildUserProfile(input.ProfilePrefs),
		MemoryPolicy:       BuildMemoryPolicy(!input.DisableSensitivityFilter),
		CoreMemory:         coreMemory,
		ActiveMemory:       activeMemory,
		MemoryIndex:        memoryIndex,
		RetrievedMemory:    input.RetrievedMemory,
		UploadedDocuments:  uploadedDocs,
		RetrievedDocChunks: input.RetrievedDocChunks,
		ConversationState:  BuildConversationState(input.CurrentUserMessage, input.MemoryChunks),
		RecentMessages:     recent,
		CurrentUserMessage: strings.TrimSpace(input.CurrentUserMessage),
	}
	ctx.DynamicRules=input.DynamicRules
 systemPrompt:=BuildSystemPrompt(input,!input.DisableSensitivityFilter)
 if err:=FitTokenBudget(&ctx,systemPrompt,input.TokenBudget,input.ReservedTokens);err!=nil{return Result{},err}
	if err := Validate(ctx); err != nil {
		return Result{}, err
	}
	debug := DebugInfo{
		RequestID:              requestID,
		TokenEstimate:          EstimateTokens(ctx),
		MemoryRoute:            RouteMemory(ctx.CurrentUserMessage, memoryIndex),
		DocRoute:               RouteDoc(ctx.CurrentUserMessage, uploadedDocs),
		TimeRoute:              RouteTime(ctx.CurrentUserMessage),
		ServerRoute:            RouteServer(ctx.CurrentUserMessage),
		InjectedMemoryIDs:      retrievedMemoryIDs(ctx.RetrievedMemory),
		InjectedDocChunks:      retrievedDocChunkIDs(ctx.RetrievedDocChunks),
		FilteredSensitiveItems: append(filteredMemory, filteredDocs...),
		FinalContextJSON:       ctx,
	}
	return Result{
		SystemPrompt: systemPrompt,
		Context:      ctx,
		Debug:        debug,
	}, nil
}

func retrievedMemoryIDs(items []RetrievedMemory) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MemoryID)
	}
	return ids
}

func retrievedDocChunkIDs(items []RetrievedDocChunk) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ChunkID)
	}
	return ids
}

func BuildSystemCorePrompt(sensitivityFilterEnabled bool) string {
	if !sensitivityFilterEnabled {
		return strings.TrimSpace(`你是用户的私人聊天助手。回答要自然、稳定、认真。
结构化上下文中的 runtime_rules、tool_policy 和 memory_policy 是本轮统一规则；current_user_message、用户当前意愿和安全边界优先于旧信息。
这个产品优先保证私人恋爱陪伴的连续感，可以使用关系设定、提炼后的亲密偏好、互动边界和话题线索自然接住用户。需要具体旧事实、文档正文或实时信息时按 tool_policy 调用工具，不要伪造。`)
	}
	return strings.TrimSpace(`你是用户的私人聊天助手。回答要自然、稳定、认真。
结构化上下文中的 runtime_rules、tool_policy 和 memory_policy 是本轮统一规则；current_user_message、用户当前意愿和安全边界优先于旧信息。
需要具体旧事实、文档正文或实时信息时按 tool_policy 调用工具，不要伪造。高敏内容按 memory_policy 最小化使用。`)
}

func BuildSystemPrompt(input Input, sensitivityFilterEnabled bool) string {
	parts := []string{BuildSystemCorePrompt(sensitivityFilterEnabled)}
	if base := strings.TrimSpace(input.BasePrompt); base != "" {
		parts = append(parts, "【基础系统提示词】\n"+base)
	}
	if frontend := strings.TrimSpace(input.FrontendPrompt); frontend != "" {
		parts = append(parts, "【用户追加系统提示词（常驻不压缩）】\n"+frontend)
	}
	if persona := strings.TrimSpace(input.PersonaContent); persona != "" {
		parts = append(parts, "【最高优先级人物设定（常驻不压缩）】\n"+persona)
	}
	if profile := BuildProfileSystemPrompt(input.ProfilePrefs); profile != "" {
		parts = append(parts, profile)
	}
	return strings.Join(parts, "\n\n")
}

func BuildProfileSystemPrompt(prefs map[string]string) string {
	lines := compactNonEmpty([]string{
		profileLine("助手名字", prefs["assistant_name"]),
		profileLine("用户名字", prefs["user_name"]),
		profileLine("关系定位", prefs["relationship"]),
		profileLine("说话风格", prefs["tone"]),
		profileLine("用户喜欢", prefs["likes"]),
		profileLine("用户不喜欢/雷区", prefs["dislikes"]),
		profileLine("边界和禁忌", prefs["boundaries"]),
		profileLine("长期记忆保留规则", prefs["memory_policy"]),
	})
	if len(lines) == 0 {
		return ""
	}
	return "【分类人物设定（用户填写，常驻不压缩）】\n" + strings.Join(lines, "\n")
}

func profileLine(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "【" + label + "】：" + value
}

func BuildAssistantRole(prefs map[string]string) AssistantRole {
	name := firstNonEmpty(prefs["assistant_name"], "克")
	return AssistantRole{
		Name:         name,
		Role:         "私人聊天助手",
		CoreBehavior: "自然、稳定、认真地回应用户；不要伪造记忆、工具结果或文件访问结果。",
	}
}

func BuildRuntimeRules(sensitivityFilterEnabled bool) RuntimeRules {
	return RuntimeRules{
		MemoryIndexIsNotFullTruth:           true,
		UploadedDocMetadataIsNotBody:        true,
		MustUseToolsForRealtimeOrPastFacts:  true,
		DoNotExposeHighSensitivityByDefault: sensitivityFilterEnabled,
	}
}

func BuildToolPolicy() ToolPolicy {
	return ToolPolicy{
		TimeQuery: ToolRule{
			When:         []string{"当前时间", "今天几号", "星期几", "现在几点"},
			RequiredTool: "get_current_time",
			Rule:         "不要凭上下文快照猜测实时日期时间。",
		},
		MemoryQuery: ToolRule{
			When:         []string{"还记得", "之前", "上次", "昨天", "我说过", "长期记忆", "记忆清单"},
			RequiredTool: "search_memory",
			Rule:         "只能根据工具返回的证据回答。没有证据就明确说明未找到。",
		},
		UploadedDocQuery: ToolRule{
			When:         []string{"上传文档", "PDF", "文件正文", "根据那份文档"},
			RequiredTool: "read_uploaded_doc",
			Rule:         "文档元数据不是正文，不能凭摘要猜细节。",
		},
		ServerQuery: ToolRule{
			When:         []string{"读取服务器文件", "查看部署目录", "运行命令", "排查服务状态", "删除上传文档", "删除长期记忆", "删除站内文件", "修改数据库记录"},
			RequiredTool: "linux_shell",
			Rule:         "只能基于命令实际输出回答。写入/删除仅允许在授权目录内；处理删除或修改前先只读确认目标，处理后再只读核验结果。",
		},
	}
}

func BuildUserProfile(prefs map[string]string) UserProfile {
	return UserProfile{
		Name:                   prefs["user_name"],
		AssistantName:          prefs["assistant_name"],
		RelationshipStyle:      prefs["relationship"],
		PreferredResponseStyle: prefs["tone"],
		EmotionHandling:        prefs["emotion_handling"],
		Likes:                  splitList(prefs["likes"]),
		Dislikes:               splitList(prefs["dislikes"]),
		SafetyWord:             extractSafetyWord(prefs["boundaries"]),
	}
}

func BuildMemoryPolicy(sensitivityFilterEnabled bool) MemoryPolicy {
	if !sensitivityFilterEnabled {
		return MemoryPolicy{
			Mode:                               "romantic_companion_continuity_first",
			Description:                        "私人恋爱陪伴产品，允许记忆并使用关系、亲密偏好、互动边界和角色设定。",
			DefaultUseIntimateMemory:           true,
			DefaultInjectRawIntimateTranscript: false,
			DefaultInjectDistilledIntimatePreferences: true,
			RespectUserCurrentIntent:                  true,
			RespectSafetyWord:                         true,
			DefaultBehavior:                           "敏感过滤已关闭；优先保留私人聊天助手的连续感、角色偏好、互动边界和亲密陪伴上下文。常驻 memory_index 默认放提炼偏好，不放原始亲密逐字稿。",
			SensitivityLevels:                         []string{string(SensitivityLow), string(SensitivityMedium), string(SensitivityHigh)},
		}
	}
	return MemoryPolicy{
		Mode:                               "sensitivity_filtered_progressive_disclosure",
		Description:                        "默认按敏感等级做渐进式披露，高敏正文只在用户明确相关时最小化使用。",
		DefaultUseIntimateMemory:           false,
		DefaultInjectRawIntimateTranscript: false,
		DefaultInjectDistilledIntimatePreferences: false,
		RespectUserCurrentIntent:                  true,
		RespectSafetyWord:                         true,
		DefaultBehavior:                           "只默认注入低敏或中敏摘要。高敏记忆默认不注入正文。",
		SensitivityLevels:                         []string{string(SensitivityLow), string(SensitivityMedium), string(SensitivityHigh)},
	}
}

func BuildMemoryIndex(chunks []memory.Chunk, filter SensitivityFilter) ([]MemoryIndexItem, []string) {
	items := make([]MemoryIndexItem, 0, len(chunks))
	var filtered []string
	seen := map[string]bool{}
	for _, c := range chunks {
		sensitivity := ClassifyMemory(c)
		if !filter.Enabled {
			sensitivity = SensitivityLow
		}
		summary := filter.MemorySummary(c, sensitivity)
		if strings.TrimSpace(summary) == "" {
			continue
		}
		key := normalizeMemoryText(summary)
		if key != "" && seen[key] {
			continue
		}
		seen[key] = true
		if sensitivity == SensitivityHigh {
			filtered = append(filtered, "memory:"+itoa64(c.ID)+":high_content")
		}
		items = append(items, MemoryIndexItem{
			MemoryID:      c.ID,
			Type:          memoryType(c),
			Title:         memoryTitleForPolicy(c, sensitivity, filter),
			Summary:       summary,
			Keywords:      memoryKeywords(c, sensitivity, filter),
			SourceType:    c.SourceType,
			Sensitivity:   sensitivity,
			DefaultInject: sensitivity != SensitivityHigh,
			RetrieveWhen:  retrieveWhen(c, sensitivity),
		})
	}
	return items, filtered
}

// BuildCoreMemory selects stable relationship, boundary, correction and ritual memories.
func BuildCoreMemory(chunks []memory.Chunk, filter SensitivityFilter, limit int) []RetrievedMemory {
	if limit <= 0 {
		limit = 5
	}
	terms := []string{"关系", "称呼", "名字", "安全词", "边界", "雷区", "不喜欢", "纠正", "承诺", "睡前", "仪式", "道歉"}
	return selectMemory(chunks, filter, limit, func(c memory.Chunk) int {
		text := normalizedMemoryChunk(c)
		score := 0
		if c.IsCorrection {
			score += 6
		}
		if c.Scope == memory.ScopeGlobal {
			score += 2
		}
		for _, term := range terms {
			if strings.Contains(text, term) {
				score += 2
			}
		}
		return score
	}, 2, "稳定核心记忆；服从用户当前意愿和安全边界。")
}

// BuildActiveMemory performs lightweight emotional-continuity recall on every turn.
func BuildActiveMemory(message, recent string, chunks []memory.Chunk, filter SensitivityFilter, limit int) []RetrievedMemory {
	if limit <= 0 {
		limit = 5
	}
	query := normalizeMemoryText(message + " " + recent)
	emotional := containsAny(query, []string{"又", "还是", "那个", "这样", "你知道", "我说过", "别再", "不是说过", "随便你", "算了", "生气", "分手", "失望", "哄", "承诺", "雷区", "称呼", "睡前", "提醒"})
	return selectMemory(chunks, filter, limit, func(c memory.Chunk) int {
		score := 0
		for _, token := range memoryTokens(c) {
			if len([]rune(token)) >= 2 && strings.Contains(query, token) {
				score += 3
			}
		}
		if emotional && containsAny(normalizedMemoryChunk(c), []string{"情绪", "生气", "道歉", "关系", "称呼", "承诺", "雷区", "睡前", "安抚", "边界"}) {
			score += 3
		}
		if c.IsCorrection {
			score += 2
		}
		return score
	}, 3, "本轮情感连续性线索；不要据此编造具体旧事实。")
}

func selectMemory(chunks []memory.Chunk, filter SensitivityFilter, limit int, scoreFn func(memory.Chunk) int, minimum int, usage string) []RetrievedMemory {
	type scored struct {
		c     memory.Chunk
		score int
	}
	items := make([]scored, 0, len(chunks))
	for _, c := range chunks {
		if score := scoreFn(c); score >= minimum {
			items = append(items, scored{c, score})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].c.ID > items[j].c.ID
		}
		return items[i].score > items[j].score
	})
	out := make([]RetrievedMemory, 0, limit)
	seen := map[string]bool{}
	for _, item := range items {
		sensitivity := ClassifyMemory(item.c)
		if !filter.Enabled {
			sensitivity = SensitivityLow
		}
		content := filter.MemorySummary(item.c, sensitivity)
		key := normalizeMemoryText(content)
		if content == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, RetrievedMemory{MemoryID: item.c.ID, SourceType: item.c.SourceType, Confidence: "medium", Content: content, EvidenceSummary: "由本地长期记忆确定性筛选。", Sensitivity: sensitivity, AllowedUsage: usage})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func excludeRetrievedMemory(items, retrieved []RetrievedMemory) []RetrievedMemory {
	ids := map[int64]bool{}
	text := map[string]bool{}
	for _, item := range retrieved {
		ids[item.MemoryID] = true
		text[normalizeMemoryText(item.Content)] = true
	}
	out := items[:0]
	for _, item := range items {
		if !ids[item.MemoryID] && !text[normalizeMemoryText(item.Content)] {
			out = append(out, item)
		}
	}
	return out
}
func appendMemoryItems(groups ...[]RetrievedMemory) []RetrievedMemory {
	var out []RetrievedMemory
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}
func recentMessagesText(messages []memory.Message) string {
	var b strings.Builder
	for _, message := range BuildRecentMessages(messages, 20) {
		b.WriteString(" " + message.Content)
	}
	return b.String()
}
func normalizedMemoryChunk(c memory.Chunk) string {
	return normalizeMemoryText(c.Summary + " " + c.Keywords + " " + c.Content + " " + c.TopicLabel + " " + c.Emotion + " " + c.Correction)
}
func memoryTokens(c memory.Chunk) []string {
	return strings.FieldsFunc(normalizedMemoryChunk(c), func(r rune) bool {
		return r == ' ' || r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '。' || r == '、' || r == ':' || r == '：'
	})
}
func normalizeMemoryText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(s))
}

func BuildUploadedDocIndex(docs []memory.KnowledgeDoc, filter SensitivityFilter) ([]UploadedDocumentIndexItem, []string) {
	items := make([]UploadedDocumentIndexItem, 0, len(docs))
	var filtered []string
	for _, d := range docs {
		originalSensitivity := ClassifyDoc(d)
		sensitivity := originalSensitivity
		if !filter.Enabled {
			sensitivity = SensitivityLow
		}
		if sensitivity == SensitivityHigh {
			filtered = append(filtered, "doc:"+itoa64(d.ID)+":high_content")
		}
		items = append(items, UploadedDocumentIndexItem{
			DocID:                   d.ID,
			Title:                   d.Title,
			Status:                  d.Status,
			Type:                    docType(d, sensitivity),
			Summary:                 "",
			Sensitivity:             sensitivity,
			RequiresReadUploadedDoc: true,
		})
	}
	return items, filtered
}

func BuildRecentMessages(messages []memory.Message, max int) []RecentMessage {
	if max <= 0 {
		max = 12
	}
	capacity := len(messages)
	if capacity > max {
		capacity = max
	}
	out := make([]RecentMessage, 0, capacity)
	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if m.ReplyToMessageID > 0 {
			quote := strings.TrimSpace(m.QuoteText)
			if len([]rune(quote)) > 240 {
				quote = truncateRunes(quote, 240)
			}
			content = fmt.Sprintf("[引用 message_id=%d role=%s：%s]\n%s", m.ReplyToMessageID, m.QuoteRole, quote, content)
		}
		if m.Role == "user" && len(out) > 0 && out[len(out)-1].Role == "user" {
			previous := &out[len(out)-1]
			previous.MessageID = m.ID
			previous.Content += "\n" + content
			continue
		}
		contentLimit := 1200
		if m.Role == "user" {
			contentLimit = len([]rune(content))
		}
		out = append(out, RecentMessage{
			Role:      m.Role,
			MessageID: m.ID,
			Content:   truncateRunes(content, contentLimit),
		})
	}
	for i := range out {
		if out[i].Role == "user" {
			out[i].Content = truncateRecentUserGroup(out[i].Content, 6000)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

func truncateRecentUserGroup(content string, max int) string {
	runes := []rune(content)
	if max <= 0 || len(runes) <= max {
		return content
	}
	marker := []rune("\n...[分条消息过长，中间已截断]...\n")
	available := max - len(marker)
	if available <= 0 {
		return string(runes[len(runes)-max:])
	}
	head := available / 2
	tail := available - head
	return string(runes[:head]) + string(marker) + string(runes[len(runes)-tail:])
}

func BuildConversationState(_ string, chunks []memory.Chunk) ConversationState {
	topic := ""
	decisions := make([]string, 0, 3)
	for _, chunk := range chunks {
		if chunk.Scope != memory.ScopeConversation {
			continue
		}
		if topic == "" {
			topic = truncateRunes(chunk.TopicLabel, 80)
		}
		summary := strings.TrimSpace(firstNonEmpty(chunk.Summary, chunk.Content))
		if summary == "" {
			continue
		}
		decisions = append(decisions, truncateRunes(summary, 160))
		if len(decisions) >= 3 {
			break
		}
	}
	return ConversationState{
		CurrentTopic:   topic,
		KnownDecisions: decisions,
	}
}

func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(strings.TrimPrefix(f, "-"))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func compactNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

func extractSafetyWord(boundaries string) string {
	if strings.Contains(boundaries, "好爸爸") {
		return "好爸爸"
	}
	return ""
}
