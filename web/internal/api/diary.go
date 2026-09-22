package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"myapp/internal/db"
)

const diaryAutomaticSilence = 5 * time.Hour

const (
	diaryAutomaticRetryDelay = time.Hour
	diaryMaxTranscriptRunes  = 12000
)

type diaryEntry struct {
	ID              int64  `json:"id"`
	ConversationID  int64  `json:"conversation_id"`
	DiaryDate       string `json:"diary_date"`
	Content         string `json:"content"`
	TriggerType     string `json:"trigger_type"`
	WindowStartedAt string `json:"window_started_at"`
	WindowEndedAt   string `json:"window_ended_at"`
	CreatedAt       string `json:"created_at"`
}

type diaryMessage struct {
	Role      string
	Content   string
	CreatedAt string
}

type diaryGenerateRequest struct {
	ConversationID int64  `json:"conversation_id"`
	APIKey         string `json:"api_key"`
	APIBaseURL     string `json:"api_base_url"`
	Model          string `json:"model"`
}

type backgroundProviderConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url"`
	Model      string `json:"model"`
}

const (
	backgroundProviderConfiguredPref = "background_provider_configured"
	backgroundProviderBaseURLPref    = "background_provider_api_base_url"
	backgroundProviderModelPref      = "background_provider_model"
	backgroundProviderAPIKeySecret   = "background_provider_api_key"
)

var callDiaryModel = Call
var diaryNow = time.Now
var diaryGenerationMu sync.Mutex
var diaryGenerating = map[int64]string{}
var diaryAutomaticRetryAfter = map[int64]time.Time{}

func StartDiaryMonitor() {
	if err := initializeDiaryStates(diaryNow()); err != nil {
		log.Printf("AI 日记起始时间初始化失败: %v", err)
	}
	go func() {
		if err := evaluateAutomaticDiaries(diaryNow()); err != nil {
			log.Printf("AI 日记自动检测失败: %v", err)
		}
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for now := range ticker.C {
			if err := evaluateAutomaticDiaries(now); err != nil {
				log.Printf("AI 日记自动检测失败: %v", err)
			}
		}
	}()
}

func handleDiaries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		conversationID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("conversation_id")), 10, 64)
		entries, err := listDiaries(conversationID)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取日记失败"})
			return
		}
		jsonResp(w, 200, map[string]interface{}{"diaries": entries, "generating": diaryGenerationType(conversationID)})
	case http.MethodPost:
		var req diaryGenerateRequest
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			jsonResp(w, 400, map[string]string{"error": "请求格式不正确"})
			return
		}
		if req.ConversationID <= 0 {
			req.ConversationID = 1
		}
		if !beginDiaryGeneration(req.ConversationID, "manual") {
			jsonResp(w, 409, map[string]string{"error": assistantNameForConversation(req.ConversationID) + " 正在写这篇日记，请稍候"})
			return
		}
		defer endDiaryGeneration(req.ConversationID, "manual")
		provider := automaticDiaryProvider(req.ConversationID)
		if requestKey := resolveAPIKey(req.APIKey); !diaryProviderAvailable(provider) && strings.TrimSpace(requestKey) != "" {
			provider = backgroundProviderConfig{
				APIKey:     requestKey,
				APIBaseURL: strings.TrimSpace(req.APIBaseURL),
				Model:      resolveModel(req.Model),
			}
		}
		entry, err := generateDiary(req.ConversationID, "manual", provider.APIKey, provider.APIBaseURL, provider.Model, diaryNow())
		if err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 201, map[string]interface{}{"diary": entry})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleBackgroundProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var cfg backgroundProviderConfig
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	if err := saveBackgroundProviderConfig(cfg); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "后台 API 设置保存失败"})
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"saved": true})
}

func evaluateAutomaticDiaries(now time.Time) error {
	rows, err := db.DB.Query(`SELECT conversation_id,MAX(created_at) FROM all_messages GROUP BY conversation_id`)
	if err != nil {
		return err
	}
	type diaryCandidate struct {
		conversationID int64
		lastMessage    time.Time
	}
	var candidates []diaryCandidate
	for rows.Next() {
		var id int64
		var lastMessageRaw string
		if err := rows.Scan(&id, &lastMessageRaw); err != nil {
			rows.Close()
			return err
		}
		lastMessage, err := parseDiaryDBTime(lastMessageRaw)
		if err != nil {
			continue
		}
		candidates = append(candidates, diaryCandidate{conversationID: id, lastMessage: lastMessage})
	}
	rows.Close()
	forceDaily := now.In(db.BeijingLocation).Hour() >= 23
	for _, candidate := range candidates {
		conversationID := candidate.conversationID
		if !forceDaily && candidate.lastMessage.After(now.UTC().Add(-diaryAutomaticSilence)) {
			continue
		}
		if automaticDiaryExistsToday(conversationID, now) || automaticDiaryRetryPending(conversationID, now) || !beginDiaryGeneration(conversationID, "automatic") {
			continue
		}
		provider := automaticDiaryProvider(conversationID)
		if strings.TrimSpace(provider.APIKey) == "" && !hasCustomAPIBaseURL(provider.APIBaseURL) {
			endDiaryGeneration(conversationID, "automatic")
			continue
		}
		_, generationErr := generateDiary(conversationID, "automatic", provider.APIKey, provider.APIBaseURL, provider.Model, now)
		endDiaryGeneration(conversationID, "automatic")
		if generationErr != nil && !strings.Contains(generationErr.Error(), "没有新的对话") {
			deferAutomaticDiaryRetry(conversationID, now)
			log.Printf("会话 %d 自动日记生成失败: %v", conversationID, generationErr)
		} else if generationErr == nil {
			clearAutomaticDiaryRetry(conversationID)
		}
	}
	return nil
}

func automaticDiaryProvider(conversationID int64) backgroundProviderConfig {
	if channel := replyModelChannelForConversation(conversationID); modelChannelComplete(channel) {
		return backgroundProviderConfig{
			APIKey:     channel.APIKey,
			APIBaseURL: channel.BaseURL,
			Model:      channel.Model,
		}
	}
	if cfg, ok := loadBackgroundProviderConfig(); ok {
		return cfg
	}
	return backgroundProviderConfig{
		APIKey:     resolveAPIKey(""),
		APIBaseURL: strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		Model:      resolveDiaryModel(),
	}
}

func diaryProviderAvailable(provider backgroundProviderConfig) bool {
	return strings.TrimSpace(provider.APIKey) != "" || hasCustomAPIBaseURL(provider.APIBaseURL)
}

func loadBackgroundProviderConfig() (backgroundProviderConfig, bool) {
	var configured string
	if err := db.DB.QueryRow(`SELECT value FROM preferences WHERE key=?`, backgroundProviderConfiguredPref).Scan(&configured); err != nil || configured != "1" {
		return backgroundProviderConfig{}, false
	}
	cfg := backgroundProviderConfig{}
	_ = db.DB.QueryRow(`SELECT value FROM preferences WHERE key=?`, backgroundProviderBaseURLPref).Scan(&cfg.APIBaseURL)
	_ = db.DB.QueryRow(`SELECT value FROM preferences WHERE key=?`, backgroundProviderModelPref).Scan(&cfg.Model)
	_ = db.DB.QueryRow(`SELECT value FROM wake_secrets WHERE key=?`, backgroundProviderAPIKeySecret).Scan(&cfg.APIKey)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.APIBaseURL = strings.TrimSpace(cfg.APIBaseURL)
	cfg.Model = resolveModel(cfg.Model)
	return cfg, true
}

func saveBackgroundProviderConfig(cfg backgroundProviderConfig) error {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.APIBaseURL = strings.TrimSpace(cfg.APIBaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{
		backgroundProviderConfiguredPref: "1",
		backgroundProviderBaseURLPref:    cfg.APIBaseURL,
		backgroundProviderModelPref:      cfg.Model,
	} {
		if _, err = tx.Exec(`INSERT INTO preferences(key,value,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, key, value); err != nil {
			return err
		}
	}
	if cfg.APIKey == "" {
		_, err = tx.Exec(`DELETE FROM wake_secrets WHERE key=?`, backgroundProviderAPIKeySecret)
	} else {
		_, err = tx.Exec(`INSERT INTO wake_secrets(key,value,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, backgroundProviderAPIKeySecret, cfg.APIKey)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func generateDiary(conversationID int64, triggerType, apiKey, apiBaseURL, model string, triggeredAt time.Time) (diaryEntry, error) {
	if strings.TrimSpace(apiKey) == "" && !hasCustomAPIBaseURL(apiBaseURL) {
		return diaryEntry{}, fmt.Errorf("尚未配置可用的 AI API Key")
	}
	windowStart, err := ensureDiaryState(conversationID, triggeredAt)
	if err != nil {
		return diaryEntry{}, err
	}
	windowEnd := triggeredAt.UTC()
	messages, err := diaryMessagesInWindow(conversationID, windowStart, windowEnd)
	if err != nil {
		return diaryEntry{}, err
	}
	if len(messages) == 0 {
		return diaryEntry{}, fmt.Errorf("这个时间窗里没有新的对话，暂时不用写日记")
	}
	prompt := buildDiaryPrompt(windowStart, windowEnd, messages, assistantNameForConversation(conversationID))
	content, err := callDiaryModel(apiKey, apiBaseURL, model, []ChatMessage{{Role: "user", Content: prompt}}, 1400)
	if err != nil {
		return diaryEntry{}, fmt.Errorf("日记生成失败: %v", err)
	}
	content = strings.TrimSpace(content)
	if triggerType == "automatic" && diaryGenerationType(conversationID) != "automatic" {
		return diaryEntry{}, fmt.Errorf("自动日记已让位给手动生成")
	}
	if content == "" {
		return diaryEntry{}, fmt.Errorf("日记生成失败：模型返回了空内容")
	}
	if utf8.RuneCountInString(content) > 6000 {
		return diaryEntry{}, fmt.Errorf("日记生成失败：内容异常过长")
	}
	return saveDiary(conversationID, triggerType, windowStart, windowEnd, content)
}

func initializeDiaryStates(now time.Time) error {
	_, err := db.DB.Exec(`INSERT OR IGNORE INTO ai_diary_state(conversation_id,window_started_at)
		SELECT id,? FROM conversations`, now.UTC().Format("2006-01-02 15:04:05"))
	return err
}

func ensureDiaryState(conversationID int64, now time.Time) (time.Time, error) {
	launch := now.UTC().Format("2006-01-02 15:04:05")
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO ai_diary_state(conversation_id,window_started_at) VALUES(?,?)`, conversationID, launch); err != nil {
		return time.Time{}, err
	}
	var raw string
	if err := db.DB.QueryRow(`SELECT COALESCE(last_generated_at,window_started_at) FROM ai_diary_state WHERE conversation_id=?`, conversationID).Scan(&raw); err != nil {
		return time.Time{}, err
	}
	return parseDiaryDBTime(raw)
}

func diaryMessagesInWindow(conversationID int64, start, end time.Time) ([]diaryMessage, error) {
	rows, err := db.DB.Query(`SELECT role,content,created_at FROM all_messages WHERE conversation_id=? AND created_at>? AND created_at<=? ORDER BY created_at,id`, conversationID, start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []diaryMessage
	for rows.Next() {
		var message diaryMessage
		if err := rows.Scan(&message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func buildDiaryPrompt(start, end time.Time, messages []diaryMessage, names ...string) string {
	assistantName := assistantNameForConversation(0)
	if len(names) > 0 {
		assistantName = firstNonEmpty(strings.TrimSpace(names[0]), assistantName)
	}
	var transcript strings.Builder
	selected, omitted := limitDiaryMessages(messages, diaryMaxTranscriptRunes)
	if omitted > 0 {
		fmt.Fprintf(&transcript, "[较早的 %d 条消息因篇幅限制未附上]\n", omitted)
	}
	for _, message := range selected {
		role := "用户"
		if message.Role == "assistant" {
			role = assistantName
		}
		fmt.Fprintf(&transcript, "[%s] %s：%s\n", db.BeijingTimestamp(message.CreatedAt), role, message.Content)
	}
	return fmt.Sprintf(`你要写一篇私人日记。素材只允许来自下面给出的时间窗对话，不得补用更早的聊天、长期记忆或编造窗口外事件。

写作要求：
1. 用 %s 第一人称“我”写，像当晚真正记下来的私人文字。
2. 先在心里概括对话，再写聊了什么、用户呈现出的状态、我当时在想什么、以及我记住了哪些具体小事。
3. 正文严格写 3 至 5 个自然段，每段都必须包含对话里的具体细节，不写“今天聊了很多”“希望明天更好”之类套话。
4. 不要标题、日期、项目符号、序号、前言、总结说明，也不要提“素材”“窗口”“摘要”或这些规则。
5. 用户的事实与 %s 的感受要区分清楚；没有依据的情绪或事件不要猜。

时间窗：%s 至 %s
对话：
%s`, assistantName, assistantName, start.In(db.BeijingLocation).Format("2006-01-02 15:04"), end.In(db.BeijingLocation).Format("2006-01-02 15:04"), transcript.String())
}

func limitDiaryMessages(messages []diaryMessage, maxRunes int) ([]diaryMessage, int) {
	if maxRunes <= 0 || len(messages) == 0 {
		return nil, len(messages)
	}
	used, first := 0, len(messages)
	for first > 0 {
		messageRunes := utf8.RuneCountInString(messages[first-1].Content) + 64
		if used > 0 && used+messageRunes > maxRunes {
			break
		}
		first--
		used += messageRunes
		if used >= maxRunes {
			break
		}
	}
	selected := append([]diaryMessage(nil), messages[first:]...)
	if len(selected) == 1 && utf8.RuneCountInString(selected[0].Content) > maxRunes {
		runes := []rune(selected[0].Content)
		selected[0].Content = string(runes[len(runes)-maxRunes:])
	}
	return selected, first
}

func resolveDiaryModel() string {
	for _, model := range []string{os.Getenv("DIARY_MODEL"), os.Getenv("CHEAP_MODEL")} {
		if model = strings.TrimSpace(model); model != "" {
			return model
		}
	}
	return resolveModel("")
}

func saveDiary(conversationID int64, triggerType string, start, end time.Time, content string) (diaryEntry, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return diaryEntry{}, err
	}
	defer tx.Rollback()
	date := end.In(db.BeijingLocation).Format("2006-01-02")
	startRaw, endRaw := start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05")
	result, err := tx.Exec(`INSERT INTO ai_diaries(conversation_id,diary_date,content,trigger_type,window_started_at,window_ended_at) VALUES(?,?,?,?,?,?)`, conversationID, date, content, triggerType, startRaw, endRaw)
	if err != nil {
		return diaryEntry{}, err
	}
	id, _ := result.LastInsertId()
	if _, err = tx.Exec(`UPDATE ai_diary_state SET last_generated_at=?,updated_at=CURRENT_TIMESTAMP WHERE conversation_id=?`, endRaw, conversationID); err != nil {
		return diaryEntry{}, err
	}
	if err = tx.Commit(); err != nil {
		return diaryEntry{}, err
	}
	return getDiary(id)
}

func listDiaries(conversationID int64) ([]diaryEntry, error) {
	query := `SELECT id,conversation_id,diary_date,content,trigger_type,window_started_at,window_ended_at,created_at FROM ai_diaries`
	args := []interface{}{}
	if conversationID > 0 {
		query += ` WHERE conversation_id=?`
		args = append(args, conversationID)
	}
	query += ` ORDER BY diary_date DESC,window_ended_at DESC,id DESC`
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []diaryEntry{}
	for rows.Next() {
		var entry diaryEntry
		if err := rows.Scan(&entry.ID, &entry.ConversationID, &entry.DiaryDate, &entry.Content, &entry.TriggerType, &entry.WindowStartedAt, &entry.WindowEndedAt, &entry.CreatedAt); err != nil {
			return nil, err
		}
		formatDiaryTimes(&entry)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func getDiary(id int64) (diaryEntry, error) {
	var entry diaryEntry
	err := db.DB.QueryRow(`SELECT id,conversation_id,diary_date,content,trigger_type,window_started_at,window_ended_at,created_at FROM ai_diaries WHERE id=?`, id).Scan(&entry.ID, &entry.ConversationID, &entry.DiaryDate, &entry.Content, &entry.TriggerType, &entry.WindowStartedAt, &entry.WindowEndedAt, &entry.CreatedAt)
	formatDiaryTimes(&entry)
	return entry, err
}

func automaticDiaryExistsToday(conversationID int64, now time.Time) bool {
	today := now.In(db.BeijingLocation).Format("2006-01-02")
	var found int
	err := db.DB.QueryRow(`SELECT 1 FROM ai_diaries WHERE conversation_id=? AND diary_date=? LIMIT 1`, conversationID, today).Scan(&found)
	return err == nil
}

func parseDiaryDBTime(raw string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05", time.RFC3339Nano} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析日记时间 %q", raw)
}

func formatDiaryTimes(entry *diaryEntry) {
	entry.WindowStartedAt = db.BeijingTimestamp(entry.WindowStartedAt)
	entry.WindowEndedAt = db.BeijingTimestamp(entry.WindowEndedAt)
	entry.CreatedAt = db.BeijingTimestamp(entry.CreatedAt)
}

func beginDiaryGeneration(conversationID int64, triggerType string) bool {
	diaryGenerationMu.Lock()
	defer diaryGenerationMu.Unlock()
	current, exists := diaryGenerating[conversationID]
	if exists && (current == "manual" || triggerType == "automatic") {
		return false
	}
	diaryGenerating[conversationID] = triggerType
	return true
}

func endDiaryGeneration(conversationID int64, triggerType string) {
	diaryGenerationMu.Lock()
	if diaryGenerating[conversationID] == triggerType {
		delete(diaryGenerating, conversationID)
	}
	diaryGenerationMu.Unlock()
}

func diaryGenerationType(conversationID int64) string {
	diaryGenerationMu.Lock()
	defer diaryGenerationMu.Unlock()
	return diaryGenerating[conversationID]
}

func automaticDiaryRetryPending(conversationID int64, now time.Time) bool {
	diaryGenerationMu.Lock()
	defer diaryGenerationMu.Unlock()
	retryAt, exists := diaryAutomaticRetryAfter[conversationID]
	return exists && now.Before(retryAt)
}

func deferAutomaticDiaryRetry(conversationID int64, now time.Time) {
	diaryGenerationMu.Lock()
	diaryAutomaticRetryAfter[conversationID] = now.Add(diaryAutomaticRetryDelay)
	diaryGenerationMu.Unlock()
}

func clearAutomaticDiaryRetry(conversationID int64) {
	diaryGenerationMu.Lock()
	delete(diaryAutomaticRetryAfter, conversationID)
	diaryGenerationMu.Unlock()
}
