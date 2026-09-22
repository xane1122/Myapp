package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"myapp/internal/db"
	"myapp/internal/memory"
	"myapp/internal/observability"
)

const automaticGlobalMemorySource = "auto_global"

const globalMemoryBatchSize = 1
const globalMemoryMaxAttempts = 5

var automaticGlobalMemoryMu sync.Mutex
var globalMemoryBatchMu sync.Mutex
var memoryWorkQueue = make(chan queuedGlobalMemoryTurn, 256)

type queuedGlobalMemoryTurn struct {
	ID               int64
	ConversationID   int64
	UserMessage      string
	AssistantMessage string
	Model            string
	CreatedAt        string
}

type globalMemoryCandidate struct {
	Summary    string   `json:"summary"`
	Category   string   `json:"category"`
	TopicLabel string   `json:"topic_label"`
	Keywords   []string `json:"keywords"`
	Emotion    string   `json:"emotion"`
	Correction string   `json:"correction"`
}

type globalMemoryAssessment struct {
	Memories []globalMemoryCandidate `json:"memories"`
}

var callMemoryAssessmentModel = Call

func queueAndMaybeProcessGlobalMemory(conversationID int64, apiKey, apiBaseURL, model, userMessage, assistantMessage, createdAt string) error {
	if explicitlyRequestsMemory(userMessage) {
		return saveGlobalMemoryCandidate(conversationID, globalMemoryCandidate{
			Summary: strings.TrimSpace(userMessage), Category: "explicit_memory_request",
			TopicLabel: "用户明确要求记住", Keywords: []string{"明确要求记住"},
		}, createdAt)
	}
	if _, err := db.DB.Exec(`INSERT INTO global_memory_candidates
		(conversation_id,user_message,assistant_message,model,created_at) VALUES(?,?,?,?,?)`,
		conversationID, strings.TrimSpace(userMessage), strings.TrimSpace(assistantMessage), strings.TrimSpace(model), createdAt); err != nil {
		return err
	}
	return processGlobalMemoryCandidateBatch(conversationID, apiKey, apiBaseURL, false)
}

func enqueueMemoryTurn(conversationID int64, userMessage, assistantMessage string) {
	cfg := backgroundModelChannelForConversation(conversationID)
	createdAt := time.Now().UTC().Format("2006-01-02 15:04:05")
	result, err := db.DB.Exec(`INSERT INTO global_memory_candidates
		(conversation_id,user_message,assistant_message,model,created_at) VALUES(?,?,?,?,?)`,
		conversationID, strings.TrimSpace(userMessage), strings.TrimSpace(assistantMessage), strings.TrimSpace(cfg.Model), createdAt)
	if err != nil {
		observability.Event("memory_queue.enqueue_failed", map[string]interface{}{"conversation_id": conversationID, "error": err.Error()})
		return
	}
	id, _ := result.LastInsertId()
	task := queuedGlobalMemoryTurn{ID: id, ConversationID: conversationID, UserMessage: strings.TrimSpace(userMessage), AssistantMessage: strings.TrimSpace(assistantMessage), Model: strings.TrimSpace(cfg.Model), CreatedAt: createdAt}
	select {
	case memoryWorkQueue <- task:
	default:
	}
}

func signalMemoryWorker() {
	select {
	case memoryWorkQueue <- queuedGlobalMemoryTurn{}:
	default:
	}
}

func processGlobalMemoryCandidateBatch(conversationID int64, apiKey, apiBaseURL string, force bool) error {
	globalMemoryBatchMu.Lock()
	defer globalMemoryBatchMu.Unlock()
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM global_memory_candidates
		WHERE conversation_id=? AND status='pending' AND (next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP)`, conversationID).Scan(&count); err != nil {
		return err
	}
	if count == 0 || (!force && count < globalMemoryBatchSize) {
		return nil
	}
	rows, err := db.DB.Query(`SELECT id,conversation_id,user_message,assistant_message,model,created_at
		FROM global_memory_candidates WHERE conversation_id=? AND status='pending'
		AND (next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP) ORDER BY id LIMIT ?`, conversationID, globalMemoryBatchSize)
	if err != nil {
		return err
	}
	var turns []queuedGlobalMemoryTurn
	for rows.Next() {
		var turn queuedGlobalMemoryTurn
		if err := rows.Scan(&turn.ID, &turn.ConversationID, &turn.UserMessage, &turn.AssistantMessage, &turn.Model, &turn.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		turns = append(turns, turn)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	ids := make([]interface{}, 0, len(turns))
	marks := make([]string, 0, len(turns))
	for _, turn := range turns {
		ids = append(ids, turn.ID)
		marks = append(marks, "?")
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := db.DB.Exec(`UPDATE global_memory_candidates SET status='processing',attempts=attempts+1,next_attempt_at=CURRENT_TIMESTAMP WHERE id IN (`+strings.Join(marks, ",")+`)`, ids...); err != nil {
		return err
	}
	cfg := backgroundModelChannelForConversation(conversationID)
	resolvedKey, resolvedBaseURL, batchModel := cfg.APIKey, cfg.BaseURL, cfg.Model
	if batchModel == "" {
		for i := len(turns) - 1; i >= 0; i-- {
			if strings.TrimSpace(turns[i].Model) != "" {
				batchModel = strings.TrimSpace(turns[i].Model)
				break
			}
		}
	}
	assessment, err := assessGlobalMemoryTurns(conversationID, resolvedKey, resolvedBaseURL, batchModel, turns)
	if err != nil {
		markGlobalMemoryBatchFailure(ids, marks, err)
		return err
	}
	createdAt := turns[len(turns)-1].CreatedAt
	for _, candidate := range assessment.Memories {
		if err := saveGlobalMemoryCandidateWithSource(conversationID, automaticGlobalMemorySource, candidate, createdAt); err != nil {
			markGlobalMemoryBatchFailure(ids, marks, err)
			return err
		}
	}
	_, err = db.DB.Exec(`UPDATE global_memory_candidates SET status='done',last_error='',processed_at=CURRENT_TIMESTAMP WHERE id IN (`+strings.Join(marks, ",")+`)`, ids...)
	return err
}

func markGlobalMemoryBatchFailure(ids []interface{}, marks []string, failure error) {
	if len(ids) == 0 {
		return
	}
	var attempts int
	_ = db.DB.QueryRow(`SELECT COALESCE(MAX(attempts),0) FROM global_memory_candidates WHERE id IN (`+strings.Join(marks, ",")+`)`, ids...).Scan(&attempts)
	message := truncateRunes(failure.Error(), 500)
	if attempts >= globalMemoryMaxAttempts {
		args := append([]interface{}{message}, ids...)
		_, _ = db.DB.Exec(`UPDATE global_memory_candidates SET status='dead',last_error=?,next_attempt_at=NULL,failed_at=CURRENT_TIMESTAMP WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
		return
	}
	delay := 5 * time.Minute * time.Duration(1<<max(0, attempts-1))
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	next := time.Now().UTC().Add(delay).Format("2006-01-02 15:04:05")
	args := append([]interface{}{message, next}, ids...)
	_, _ = db.DB.Exec(`UPDATE global_memory_candidates SET status='pending',last_error=?,next_attempt_at=?,failed_at=NULL WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
}

func assessGlobalMemoryTurns(conversationID int64, apiKey, apiBaseURL, model string, turns []queuedGlobalMemoryTurn) (globalMemoryAssessment, error) {
	if strings.TrimSpace(apiKey) == "" {
		return globalMemoryAssessment{}, fmt.Errorf("缺少API key")
	}
	var source strings.Builder
	for i, turn := range turns {
		fmt.Fprintf(&source, "\n--- turn %d ---\n用户：%s\n助手：%s\n", i+1, turn.UserMessage, turn.AssistantMessage)
	}
	prompt := `你是全局长期记忆批量筛选器。审查下面多轮对话，只提取以后跨会话有用的独立事实。
只收：用户纠错、稳定偏好或边界、重要事件或承诺、强烈情绪及原因、关系变化。不要收普通闲聊、临时任务、助手推测或重复事实。
先对照结构化上下文中的 memory_index、active_memory 和 core_memory 判断是否已有相同事实；已有内容不要重复写入。
只输出合法JSON：{"memories":[{"summary":"脱离原对话也能理解的事实","category":"correction|preference_boundary|important_event|emotional_outburst|relationship_change","topic_label":"稳定话题标签","keywords":["关键词"],"emotion":"neutral|happy|sad|angry","correction":"纠错变化，否则为空"}]}
没有命中则输出{"memories":[]}。待审查内容：` + source.String()
	history, err := memory.GetHistory(conversationID, maxHistory)
	if err != nil {
		return globalMemoryAssessment{}, err
	}
	currentUserMessage := turns[len(turns)-1].UserMessage
	built, err := buildJSONContextMessages(conversationID, currentUserMessage, history, nil)
	if err != nil {
		return globalMemoryAssessment{}, fmt.Errorf("记忆 Worker 上下文构建失败: %w", err)
	}
	messages := []ChatMessage{{Role: "system", Content: "你是异步长期记忆处理 Worker。只做结构化记忆判断，不生成聊天回复，不进行角色扮演。严格按当前任务要求只输出 memories JSON。"}}
	for _, message := range built.Messages {
		if message.Role != "system" {
			messages = append(messages, message)
		}
	}
	messages = append(messages, ChatMessage{Role: "user", Content: prompt})
	raw, err := callMemoryAssessmentModel(apiKey, apiBaseURL, model, messages, 1200)
	if err != nil {
		return globalMemoryAssessment{}, err
	}
	return parseGlobalMemoryAssessment(raw)
}

func assessAndSaveGlobalMemory(conversationID int64, apiKey, apiBaseURL, model string, userMessage, assistantMessage string, createdAt string) error {
	forced := explicitlyRequestsMemory(userMessage)
	resolvedKey := globalMemoryAPIKey(apiKey)
	assessment, err := assessGlobalMemory(resolvedKey, apiBaseURL, model, userMessage, assistantMessage)
	strongModel := strongGlobalMemoryModel()
	if err != nil && !strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(strongModel)) {
		assessment, err = assessGlobalMemory(resolvedKey, apiBaseURL, strongModel, userMessage, assistantMessage)
	}
	if err != nil {
		if !forced {
			return err
		}
		assessment.Memories = []globalMemoryCandidate{{
			Summary:  strings.TrimSpace(userMessage),
			Category: "explicit_memory_request",
			Keywords: []string{"明确要求记住"},
		}}
	}
	if forced && len(assessment.Memories) == 0 {
		assessment.Memories = append(assessment.Memories, globalMemoryCandidate{
			Summary:  strings.TrimSpace(userMessage),
			Category: "explicit_memory_request",
			Keywords: []string{"明确要求记住"},
		})
	}

	for _, candidate := range assessment.Memories {
		if err := saveGlobalMemoryCandidate(conversationID, candidate, createdAt); err != nil {
			return err
		}
	}
	return nil
}

func assessGlobalMemory(apiKey, apiBaseURL, model, userMessage, assistantMessage string) (globalMemoryAssessment, error) {
	if strings.TrimSpace(apiKey) == "" {
		return globalMemoryAssessment{}, fmt.Errorf("缺少API key")
	}
	prompt := `你是全局长期记忆筛选器。判断这一轮对话是否包含以后跨会话可能有用的信息。阈值要宽松，宁可多收，不要漏掉。
只收以下五类信号：
1. correction：用户纠正助手的错误或纠正旧事实；
2. preference_boundary：新的偏好、厌恶、禁忌、雷区、希望助手以后如何做；
3. important_event：对用户有持续影响的重要事件、计划、承诺或人生变化；
4. emotional_outburst：明显的愤怒、崩溃、强烈悲伤、狂喜等情绪爆发及其原因；
5. relationship_change：用户与助手或现实人物的关系、称呼、信任、亲密度、边界发生变化。
用户明确要求“记住这个/记住这件事/请记住/帮我记住”时必须至少输出一条，即使不属于上面五类。
可以输出多条。summary必须是脱离本轮也能理解的简洁事实，忠于用户原话，不推测。无信号时输出空数组。
只输出合法JSON，不要Markdown：
{"memories":[{"summary":"...","category":"correction|preference_boundary|important_event|emotional_outburst|relationship_change|explicit_memory_request","topic_label":"具体且稳定的话题标签","keywords":["..."],"emotion":"neutral|happy|sad|angry","correction":"若为纠正，写旧认知到新事实，否则留空"}]}

用户：` + userMessage + "\n助手：" + assistantMessage
	raw, err := Call(apiKey, apiBaseURL, model, []ChatMessage{{Role: "user", Content: prompt}}, 500)
	if err != nil {
		return globalMemoryAssessment{}, err
	}
	return parseGlobalMemoryAssessment(raw)
}

func parseGlobalMemoryAssessment(raw string) (globalMemoryAssessment, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var result globalMemoryAssessment
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return result, err
	}
	allowed := map[string]bool{
		"correction": true, "preference_boundary": true, "important_event": true,
		"emotional_outburst": true, "relationship_change": true, "explicit_memory_request": true,
	}
	filtered := result.Memories[:0]
	for _, candidate := range result.Memories {
		candidate.Summary = strings.TrimSpace(candidate.Summary)
		candidate.Category = strings.ToLower(strings.TrimSpace(candidate.Category))
		candidate.TopicLabel = strings.TrimSpace(candidate.TopicLabel)
		if candidate.Summary != "" && allowed[candidate.Category] {
			filtered = append(filtered, candidate)
		}
	}
	result.Memories = filtered
	return result, nil
}

func saveGlobalMemoryCandidate(conversationID int64, candidate globalMemoryCandidate, createdAt string) error {
	return saveGlobalMemoryCandidateWithSource(conversationID, automaticGlobalMemorySource, candidate, createdAt)
}

func saveGlobalMemoryCandidateWithSource(conversationID int64, sourceType string, candidate globalMemoryCandidate, createdAt string) error {
	content := strings.TrimSpace(candidate.Summary)
	if content == "" {
		return nil
	}
	automaticGlobalMemoryMu.Lock()
	defer automaticGlobalMemoryMu.Unlock()
	if duplicate, err := isDuplicateGlobalMemory(conversationID, candidate); err != nil {
		return err
	} else if duplicate {
		return nil
	}
	isCorrection := candidate.Category == "correction" || strings.TrimSpace(candidate.Correction) != ""
	topicLabel := strings.TrimSpace(candidate.TopicLabel)
	if topicLabel == "" {
		topicLabel = candidate.Category
	}
	meta := memory.ChunkMetadata{
		TimeStart: createdAt, TimeEnd: createdAt, Emotion: candidate.Emotion,
		Correction: candidate.Correction, TopicLabel: topicLabel,
		IsCorrection: isCorrection, Importance: "high", MemoryType:globalMemoryType(candidate.Category),
	}
	newID, err := memory.SaveChunkWithMetadataReturningID(conversationID, memory.ScopeGlobal, sourceType, sql.NullInt64{}, content, content, strings.Join(candidate.Keywords, ","), meta)
	if err != nil || !isCorrection || topicLabel == "" {
		return err
	}
	_, err = db.DB.Exec(`UPDATE memory_chunks SET active=0,superseded_by=?
		WHERE id<>? AND active=1 AND assistant=? AND scope=? AND topic_label=?`, newID, newID, memory.ConversationAssistant(conversationID),memory.ScopeGlobal, topicLabel)
	return err
}

func isDuplicateGlobalMemory(conversationID int64, candidate globalMemoryCandidate) (bool, error) {
	existing, err := memory.ListChunksByScope(conversationID, memory.ScopeGlobal, 5000)
	if err != nil {
		return false, err
	}
	for _, chunk := range existing {
		if normalizeMemoryPrefix(candidate.Summary)==normalizeMemoryPrefix(firstNonEmpty(chunk.Summary,chunk.Content)) {
			return true, nil
		}
	}
	return false, nil
}

func memoryTextSimilarity(a, b string) float64 {
	a = normalizeMemoryPrefix(a)
	b = normalizeMemoryPrefix(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	grams := func(s string) map[string]bool {
		runes := []rune(s)
		out := map[string]bool{}
		if len(runes) == 1 {
			out[s] = true
		}
		for i := 0; i+1 < len(runes); i++ {
			out[string(runes[i:i+2])] = true
		}
		return out
	}
	left, right := grams(a), grams(b)
	common := 0
	for gram := range left {
		if right[gram] {
			common++
		}
	}
	return float64(2*common) / float64(len(left)+len(right))
}

func normalizeMemoryPrefix(text string) string {
	runes := []rune(strings.ToLower(strings.TrimSpace(text)))
	if len(runes) > 100 {
		runes = runes[:100]
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x4e00 && r <= 0x9fff {
			return r
		}
		return -1
	}, string(runes))
}

func explicitlyRequestsMemory(text string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(text), ""))
	if strings.Contains(normalized, "吗") && (strings.Contains(normalized, "还记住") || strings.Contains(normalized, "记得")) {
		return false
	}
	for _, phrase := range []string{"记住这个", "记住这件事", "请记住", "帮我记住", "给我记住", "一定要记住"} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func cheapGlobalMemoryModel() string {
	if model := strings.TrimSpace(os.Getenv("GLOBAL_MEMORY_CHEAP_MODEL")); model != "" {
		return model
	}
	return strongGlobalMemoryModel()
}

func strongGlobalMemoryModel() string {
	if model := strings.TrimSpace(os.Getenv("GLOBAL_MEMORY_RESCAN_MODEL")); model != "" {
		return model
	}
	return resolveModel("")
}

func globalMemoryBatchModels(chatModel string) []string {
	models := []string{
		cheapGlobalMemoryModel(),
		strings.TrimSpace(os.Getenv("CHEAP_MODEL")),
		strongGlobalMemoryModel(),
		strings.TrimSpace(chatModel),
	}
	unique := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		key := strings.ToLower(model)
		if model == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, model)
	}
	return unique
}

func globalMemoryAPIKey(preferred string) string {
	for _, key := range []string{preferred, os.Getenv("OPENROUTER_API_KEY"), os.Getenv("CHEAP_API_KEY")} {
		if key = strings.TrimSpace(key); key != "" {
			return key
		}
	}
	return ""
}

func StartGlobalMemoryRescan() {
	go func() {
		signalMemoryWorker()
		retryTicker := time.NewTicker(30 * time.Second)
		defer retryTicker.Stop()
		for {
			select {
			case <-memoryWorkQueue:
				if err := processPendingGlobalMemoryCandidates(false); err != nil {
					log.Printf("记忆队列消费失败: %v", err)
				}
			case <-retryTicker.C:
				if err := processPendingGlobalMemoryCandidates(false); err != nil {
					log.Printf("记忆队列轮询失败: %v", err)
				}
			}
		}
	}()
}

func processPendingGlobalMemoryCandidates(force bool) error {
	_, _ = db.DB.Exec(`UPDATE global_memory_candidates SET status='pending',next_attempt_at=CURRENT_TIMESTAMP,
		last_error=CASE WHEN last_error='' THEN 'recovered stale processing batch' ELSE last_error END
		WHERE status='processing' AND next_attempt_at<datetime('now','-30 minutes')`)
	rows, err := db.DB.Query(`SELECT DISTINCT conversation_id FROM global_memory_candidates
		WHERE status='pending' AND (next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP) ORDER BY conversation_id`)
	if err != nil {
		return err
	}
	var conversationIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		conversationIDs = append(conversationIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range conversationIDs {
		cfg := backgroundModelChannelForConversation(id)
		if !modelChannelComplete(cfg) {
			observability.Event("global_memory.pending_batch_failed", map[string]interface{}{"conversation_id": id, "error": "当前角色的 A/B 通道均未完整配置"})
			continue
		}
		if err := processGlobalMemoryCandidateBatch(id, cfg.APIKey, cfg.BaseURL, true); err != nil {
			observability.Event("global_memory.pending_batch_failed", map[string]interface{}{"conversation_id": id, "error": err.Error()})
			continue
		}
		maybeCompress(id, cfg.APIKey, cfg.BaseURL)
	}
	return nil
}

func rescanGlobalMemoryDay(day time.Time) error {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 1)
	rows, err := db.DB.Query(`SELECT id,conversation_id,content,summary,created_at FROM memory_chunks WHERE scope=? AND created_at>=? AND created_at<? ORDER BY conversation_id,id`, memory.ScopeConversation, start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"))
	if err != nil {
		return err
	}
	defer rows.Close()
	type sourceChunk struct {
		id             int64
		conversationID int64
		content        string
		summary        string
		createdAt      string
	}
	var chunks []sourceChunk
	for rows.Next() {
		var chunk sourceChunk
		if err := rows.Scan(&chunk.id, &chunk.conversationID, &chunk.content, &chunk.summary, &chunk.createdAt); err != nil {
			return err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range chunks {
		cfg := backgroundModelChannelForConversation(item.conversationID)
		text := firstNonEmpty(item.summary, item.content)
		if err := assessAndSaveGlobalMemory(item.conversationID, cfg.APIKey, cfg.BaseURL, cfg.Model, text, "", item.createdAt); err != nil {
			observability.Event("global_memory.rescan_chunk_failed", map[string]interface{}{"conversation_id": item.conversationID, "memory_chunk_id": item.id, "error": err.Error()})
		}
	}
	return nil
}

type GlobalMemoryBackfillSummary struct {
	Conversations int
	Scanned       int
	Inserted      int
}

// BackfillConversationGlobalMemories distills historical conversation chunks
// into global manual memories without deleting the source chunks.
func BackfillConversationGlobalMemories(apiKey, apiBaseURL, model string, logf func(string, ...interface{})) (GlobalMemoryBackfillSummary, error) {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	if strings.TrimSpace(model) == "" {
		model = cheapGlobalMemoryModel()
	}
	rows, err := db.DB.Query(`SELECT DISTINCT conversation_id FROM memory_chunks WHERE scope=? ORDER BY conversation_id`, memory.ScopeConversation)
	if err != nil {
		return GlobalMemoryBackfillSummary{}, err
	}
	var conversationIDs []int64
	startConversationID, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("GLOBAL_MEMORY_BACKFILL_START_ID")), 10, 64)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return GlobalMemoryBackfillSummary{}, err
		}
		if id >= startConversationID {
			conversationIDs = append(conversationIDs, id)
		}
	}
	if err := rows.Close(); err != nil {
		return GlobalMemoryBackfillSummary{}, err
	}

	summary := GlobalMemoryBackfillSummary{Conversations: len(conversationIDs)}
	for _, conversationID := range conversationIDs {
		chunks, err := listConversationChunksForBackfill(conversationID)
		if err != nil {
			return summary, err
		}
		startOffset := 0
		if conversationID == startConversationID {
			startOffset, _ = strconv.Atoi(strings.TrimSpace(os.Getenv("GLOBAL_MEMORY_BACKFILL_START_OFFSET")))
			if startOffset < 0 || startOffset > len(chunks) {
				return summary, fmt.Errorf("conversation_id=%d start offset %d无效，记忆数=%d", conversationID, startOffset, len(chunks))
			}
		}
		inserted := 0
		for start := startOffset; start < len(chunks); start += 25 {
			end := start + 25
			if end > len(chunks) {
				end = len(chunks)
			}
			var assessment globalMemoryAssessment
			var assessErr error
			for attempt := 1; attempt <= 3; attempt++ {
				assessment, assessErr = assessGlobalMemoryBatch(globalMemoryAPIKey(apiKey), apiBaseURL, model, chunks[start:end])
				if assessErr == nil {
					break
				}
				logf("[重试] conversation_id=%d, batch=%d-%d, attempt=%d, error=%v", conversationID, start+1, end, attempt, assessErr)
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			if assessErr != nil {
				return summary, fmt.Errorf("conversation_id=%d batch=%d-%d: %w", conversationID, start+1, end, assessErr)
			}
			for _, candidate := range assessment.Memories {
				before, err := countGlobalMemories()
				if err != nil {
					return summary, err
				}
				if err := saveGlobalMemoryCandidateWithSource(1, "manual", candidate, time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
					return summary, err
				}
				after, err := countGlobalMemories()
				if err != nil {
					return summary, err
				}
				if after > before {
					inserted++
				}
			}
		}
		scanned := len(chunks) - startOffset
		summary.Scanned += scanned
		summary.Inserted += inserted
		logf("[整理] conversation_id=%d, 扫描 %d 条, 提取 %d 条进 global", conversationID, scanned, inserted)
	}
	return summary, nil
}

func listConversationChunksForBackfill(conversationID int64) ([]memory.Chunk, error) {
	return memory.ListChunksByScope(conversationID, memory.ScopeConversation, 10000)
}

func countGlobalMemories() (int, error) {
	var count int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM memory_chunks WHERE scope=?`, memory.ScopeGlobal).Scan(&count)
	return count, err
}

func assessGlobalMemoryBatch(apiKey, apiBaseURL, model string, chunks []memory.Chunk) (globalMemoryAssessment, error) {
	if strings.TrimSpace(apiKey) == "" {
		return globalMemoryAssessment{}, fmt.Errorf("缺少API key")
	}
	var source strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&source, "\n--- memory_chunk_id=%d ---\n%s\n", chunk.ID, firstNonEmpty(chunk.Summary, chunk.Content))
	}
	prompt := `你是历史长期记忆整理器。逐条审查下面的conversation记忆，提取以后跨会话有用的全局记忆。判断阈值宽松，宁多不漏，但不要把相同事实重复输出。
只提取五类：correction用户纠错；preference_boundary新偏好或雷区；important_event含人物、日期、承诺、约定或里程碑的重要事件；emotional_outburst强烈情绪及原因；relationship_change角色、称呼、信任、亲密度或关系定义变化。
每个独立事实输出一条，summary必须脱离原对话也能理解，忠于原文，不添加事实。只输出合法JSON：
{"memories":[{"summary":"一句完整记忆","category":"correction|preference_boundary|important_event|emotional_outburst|relationship_change","topic_label":"具体稳定的话题标签","keywords":["3到12个关键词"],"emotion":"neutral|happy|sad|angry","correction":"纠错前后变化，否则为空"}]}
没有命中则输出{"memories":[]}。
待审查内容：` + source.String()
	raw, err := Call(apiKey, apiBaseURL, model, []ChatMessage{{Role: "user", Content: prompt}}, 4000)
	if err != nil {
		return globalMemoryAssessment{}, err
	}
	return parseGlobalMemoryAssessment(raw)
}

func globalMemoryType(category string) string {
 switch category {case "preference_boundary","relationship_change":return "preference";case "important_event":return "diary";case "emotional_outburst":return "emotion";default:return "fact"}
}
