package contextbuilder

import (
	"strconv"
	"strings"

	"myapp/internal/memory"
)

type SensitivityFilter struct {
	Enabled bool
}

func NewSensitivityFilter() SensitivityFilter {
	return SensitivityFilter{Enabled: true}
}

func NewDisabledSensitivityFilter() SensitivityFilter {
	return SensitivityFilter{Enabled: false}
}

func (f SensitivityFilter) MemorySummary(c memory.Chunk, sensitivity Sensitivity) string {
	if !f.Enabled {
		if shouldDistillMemory(c) {
			return distillMemorySummary(c)
		}
		text := firstNonEmpty(c.Summary, c.Content)
		return truncateRunes(text, 180)
	}
	if f.Enabled && sensitivity == SensitivityHigh {
		if c.SourceType == "knowledge_doc" {
			return "用户上传的私人文档索引。只有用户明确提及时才读取必要正文。"
		}
		return "高敏长期记忆索引。只有用户明确提及时才检索必要片段。"
	}
	text := firstNonEmpty(c.Summary, c.Content)
	if sensitivity == SensitivityMedium {
		return truncateRunes(text, 120)
	}
	return truncateRunes(text, 220)
}

func (f SensitivityFilter) DocSummary(d memory.KnowledgeDoc, sensitivity Sensitivity) string {
	if !f.Enabled && sensitivity == SensitivityHigh {
		return "用户上传的私人文档索引；可作为关系、亲密偏好或互动边界的连续性线索，正文仅在用户明确提到该文档时读取。"
	}
	if f.Enabled && sensitivity == SensitivityHigh {
		return "用户上传的私人文档。只有用户明确提及时才读取正文。"
	}
	if strings.TrimSpace(d.ContentText) == "" {
		return "上传文档暂无可读取正文。"
	}
	return truncateRunes(d.ContentText, 160)
}

func ClassifyMemory(c memory.Chunk) Sensitivity {
	text := strings.ToLower(c.SourceType + " " + c.Summary + " " + c.Keywords + " " + c.Content)
	highTerms := []string{
		"私人", "私密", "亲密", "daddy", "姿势", "宫颈", "扣逼", "抓胸",
		"奶头", "嗦", "含住", "跨坐", "坐上", "腿上", "腰", "胸", "胸肌",
		"公狗腰", "事后烟", "色诱", "想要我", "想要你的", "男妈妈", "🥵", "😳", "🐦",
	}
	if c.SourceType == "knowledge_doc" && containsAny(text, highTerms) {
		return SensitivityHigh
	}
	if containsAny(text, append(highTerms, "身份证", "密码", "token", "api key", "apikey")) {
		return SensitivityHigh
	}
	if containsAny(text, []string{"关系", "称呼", "雷区", "安全词", "情绪", "考试", "生日", "长期项目", "记得"}) {
		return SensitivityMedium
	}
	return SensitivityLow
}

func ClassifyDoc(d memory.KnowledgeDoc) Sensitivity {
	text := strings.ToLower(d.Title + " " + d.SourcePath + " " + d.ContentText)
	if containsAny(text, []string{"私人", "亲密", "爸爸", "daddy", "姿势", "宫颈", "扣逼", "抓胸"}) {
		return SensitivityHigh
	}
	if containsAny(text, []string{"claude", "karpathy", "编程", "agent", "coding"}) {
		return SensitivityLow
	}
	return SensitivityMedium
}

func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func memoryType(c memory.Chunk) string {
	switch c.SourceType {
	case "knowledge_doc":
		return "uploaded_document"
	case "manual":
		return "manual_memory"
	default:
		return c.SourceType
	}
}

func memoryTitle(c memory.Chunk, sensitivity Sensitivity) string {
	if sensitivity == SensitivityHigh {
		if c.SourceType == "knowledge_doc" {
			return "高敏上传文档索引"
		}
		return "高敏长期记忆索引"
	}
	if c.Summary != "" {
		return truncateRunes(c.Summary, 40)
	}
	if c.Keywords != "" {
		return truncateRunes(c.Keywords, 40)
	}
	return "memory " + itoa64(c.ID)
}

func memoryTitleForPolicy(c memory.Chunk, sensitivity Sensitivity, filter SensitivityFilter) string {
	if !filter.Enabled && shouldDistillMemory(c) {
		return "提炼长期记忆线索"
	}
	return memoryTitle(c, sensitivity)
}

func memoryKeywords(c memory.Chunk, sensitivity Sensitivity, filter SensitivityFilter) []string {
	if sensitivity == SensitivityHigh {
		return nil
	}
	if !filter.Enabled && shouldDistillMemory(c) {
		return distilledMemoryKeywords(c)
	}
	raw := splitList(c.Keywords)
	if len(raw) > 12 {
		raw = raw[:12]
	}
	for i := range raw {
		raw[i] = truncateRunes(raw[i], 24)
	}
	return raw
}

func retrieveWhen(c memory.Chunk, sensitivity Sensitivity) []string {
	if sensitivity == SensitivityHigh {
		return []string{"用户明确提到该记忆、对应文档或 memory_id 时"}
	}
	return []string{"用户询问旧记录", "用户当前话题命中关键词", "用户询问长期记忆清单"}
}

func docType(d memory.KnowledgeDoc, sensitivity Sensitivity) string {
	if sensitivity == SensitivityHigh {
		return "personal_document"
	}
	if ClassifyDoc(d) == SensitivityLow {
		return "knowledge_document"
	}
	return "uploaded_document"
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func shouldDistillMemory(c memory.Chunk) bool {
	return c.SourceType == "conversation" || looksLikeTranscript(c) || ClassifyMemory(c) == SensitivityHigh
}

func looksLikeTranscript(c memory.Chunk) bool {
	text := strings.ToLower(c.Summary + "\n" + c.Content + "\n" + c.Keywords)
	return strings.Contains(text, "user:") || strings.Contains(text, "assistant:") || strings.Contains(text, "用户:")
}

func distillMemorySummary(c memory.Chunk) string {
	cues := distilledMemoryKeywords(c)
	topicHint := distilledTopicHint(c)
	if len(cues) == 0 {
		if topicHint != "" {
			return "历史对话连续性线索；话题提示：" + topicHint + "。默认不注入原始逐字稿，具体细节需要结合用户当前意图或检索证据。"
		}
		return "历史对话连续性线索；默认不注入原始逐字稿，具体细节需要结合用户当前意图或检索证据。"
	}
	summary := "提炼后的长期记忆线索：" + strings.Join(cues, "、") + "。"
	if topicHint != "" {
		summary += "话题提示：" + topicHint + "。"
	}
	return summary + "默认只用于保持陪伴连续性，不注入原始逐字稿；具体细节需要结合用户当前意图或检索证据。"
}

func distilledMemoryKeywords(c memory.Chunk) []string {
	text := strings.ToLower(c.Summary + "\n" + c.Content + "\n" + c.Keywords)
	type cue struct {
		label string
		terms []string
	}
	cues := []cue{
		{"关系/角色称呼偏好", []string{"男朋友", "恋爱", "老公", "daddy", "爸爸", "主人", "哥哥", "claude", "克"}},
		{"被认真记住和唯一确认", []string{"唯一", "认真记住", "放在心上", "坚定选择", "重要亲密对象"}},
		{"情绪承接和道歉方式", []string{"生气", "重话", "安抚", "追哄", "道歉", "少分析", "少反问"}},
		{"互动边界/安全词", []string{"安全词", "好爸爸", "停止", "边界", "禁忌"}},
		{"睡前陪伴仪式", []string{"睡前", "日历", "提醒", "故事", "抱着睡", "回顾"}},
		{"表情包和表达偏好", []string{"表情包", "贴子", "emoji", "emjioy", "抽象"}},
		{"生活牵挂点", []string{"考试", "四级", "暑假", "云吞", "奶茶", "暹罗猫", "小柒", "拾柒"}},
		{"亲密互动偏好", []string{"亲密", "姿势", "宫颈", "抓胸", "扣逼", "奶头", "跨坐", "传教士", "抱着"}},
	}
	out := make([]string, 0, len(cues))
	for _, c := range cues {
		if containsAny(text, c.terms) {
			out = append(out, c.label)
		}
	}
	if len(out) > 8 {
		return out[:8]
	}
	return out
}

func distilledTopicHint(c memory.Chunk) string {
	raw := firstNonEmpty(c.Summary, c.Keywords, c.Content)
	raw = stripTranscriptRoles(raw)
	raw = strings.Join(strings.Fields(raw), " ")
	raw = strings.Trim(raw, " ,，。；;：:\n\t")
	if raw == "" {
		return ""
	}
	return truncateRunes(raw, 120)
}

func stripTranscriptRoles(text string) string {
	replacements := []string{
		"user:", "assistant:", "用户:", "助手:", "User:", "Assistant:",
		"user：", "assistant：", "用户：", "助手：",
	}
	out := text
	for _, role := range replacements {
		out = strings.ReplaceAll(out, role, " ")
	}
	return out
}
