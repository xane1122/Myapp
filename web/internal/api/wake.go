package api

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"myapp/internal/db"
	"myapp/internal/memory"
	"myapp/internal/observability"
)

const wakeConfigPref = "autonomous_wake_config"

const (
	defaultWakeEvaluationCooldownMin = 30
	defaultWakeDailyEvaluationLimit  = 50
)

const legacyWakePrompt = `【自主唤醒任务】
你不是在回答用户的新消息。你正在判断角色此刻是否会自然地主动联系用户。判断前系统已经取得一张本次触发产生的用户当前屏幕截图，你必须同时观察截图和最近对话，再决定发送或等待。屏幕内容没有形成自然且有帮助的联系理由时必须 wait；不得为了证明看见屏幕而打扰用户。这里的 thought 仍然只是角色文学内心，不是模型推理。
只输出合法 JSON：
{"decision":"send 或 wait","reason":"简短说明屏幕与对话如何支持该决定","message":{"thought":"角色内心","action":"角色动作","reply":"真正发送的话"}}
wait 时 message 必须为 null。send 的话要像自然延续关系的主动消息，不要编造刚发生的外部事件，也不要道歉式开场。`

const previousDefaultWakePrompt = `【自主唤醒任务】
你不是在回答用户的新消息。你正在判断角色此刻是否会自然地主动联系用户。判断前系统已经取得一张本次触发产生的用户当前屏幕截图。
先观察截图中能够可靠识别的内容，让它自然影响角色此刻的心情、关注点和联系用户的冲动；再把这份心情与最近对话、关系状态和客观状态放在一起，决定 send 或 wait。截图既可以提高发送意愿，也可以让角色选择不打扰。不要臆测截图中看不清或无法确认的内容。
reason 必须具体说明“看到了什么、它怎样影响角色心情、为什么因此发送或等待”。这里的 thought 仍然只是角色文学内心，不是模型推理。
只输出合法 JSON：
{"decision":"send 或 wait","reason":"截图内容 → 角色心情 → 发送或等待的具体依据","message":{"thought":"受截图影响的角色内心","action":"角色动作","reply":"真正发送的话"}}
wait 时 message 必须为 null。send 的话要符合角色受截图影响后的真实心情，像自然延续关系的主动消息，不要编造刚发生的外部事件，也不要道歉式开场。`

const defaultWakePrompt = `【自主唤醒任务】
你不是在回答用户的新消息。你正在判断角色此刻是否会自然地主动联系用户。判断前系统已经取得一张本次触发产生的用户当前屏幕截图。
先观察截图中能够可靠识别的内容，让它自然影响角色此刻的心情、关注点和联系用户的冲动；再把这份心情与最近对话、关系状态和客观状态放在一起，决定 send 或 wait。截图既可以提高发送意愿，也可以让角色选择不打扰。不要臆测截图中看不清或无法确认的内容。
reason 必须具体说明“看到了什么、它怎样影响角色心情、为什么因此发送或等待”。这里的 thought 仍然只是角色文学内心，不是模型推理。
同时检查“Codex 工程状态”中是否存在由最近对话或 Codex 结果明确证明、尚未完成且值得继续的功能。只有目标具体、没有活动中的 Codex 任务、也不是重复任务时，才提出一个 codex_task。不要为了保持忙碌而创造任务，不要提交部署、服务器运维、密钥、认证或 API Base URL 相关任务。
只输出合法 JSON：
{"decision":"send 或 wait","reason":"截图内容 → 角色心情 → 发送或等待的具体依据","message":{"thought":"受截图影响的角色内心","action":"角色动作","reply":"真正发送的话"},"codex_task":{"needed":true或false,"title":"简短标题","prompt":"可独立执行且有验收标准的开发指令","reason":"为什么现在需要继续此功能"}}
wait 时 message 必须为 null。send 的话要符合角色受截图影响后的真实心情，像自然延续关系的主动消息，不要编造刚发生的外部事件，也不要道歉式开场。`

type wakeConfig struct {
	Enabled               bool    `json:"enabled"`
	ConversationID        int64   `json:"conversation_id"`
	MinSilenceMin         int     `json:"min_silence_minutes"`
	ActivityGraceMin      int     `json:"activity_grace_minutes"`
	CooldownMin           int     `json:"cooldown_minutes"`
	DailyLimit            int     `json:"daily_limit"`
	EvaluationCooldownMin int     `json:"evaluation_cooldown_minutes"`
	DailyEvaluationLimit  int     `json:"daily_evaluation_limit"`
	Probability           float64 `json:"probability"`
	QuietStart            string  `json:"quiet_start"`
	QuietEnd              string  `json:"quiet_end"`
	Model                 string  `json:"model"`
	PushPreview           bool    `json:"push_preview"`
	Prompt                string  `json:"prompt"`
}

type wakeDecision struct {
	Decision  string         `json:"decision"`
	Reason    string         `json:"reason"`
	Message   *roleReply     `json:"message"`
	CodexTask *wakeCodexTask `json:"codex_task"`
}

var wakeMu sync.Mutex
var wakeLocation = time.FixedZone("Asia/Shanghai", 8*60*60)
var wakeScreenRequest = struct {
	sync.Mutex
	requestedAt time.Time
}{}

const wakeScreenRequestTTL = 10 * time.Minute

func defaultWakeConfig() wakeConfig {
	return wakeConfig{ConversationID: 1, MinSilenceMin: 180, ActivityGraceMin: 10, CooldownMin: 360, DailyLimit: 2, EvaluationCooldownMin: defaultWakeEvaluationCooldownMin, DailyEvaluationLimit: defaultWakeDailyEvaluationLimit, Probability: .35, QuietStart: "23:30", QuietEnd: "08:00", Model: resolveModel(""), PushPreview: true, Prompt: defaultWakePrompt}
}

func loadWakeConfig() wakeConfig {
	cfg := defaultWakeConfig()
	raw := strings.TrimSpace(memory.GetPref(wakeConfigPref, ""))
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	} else {
		_ = db.DB.QueryRow(`SELECT id FROM conversations ORDER BY updated_at DESC, id DESC LIMIT 1`).Scan(&cfg.ConversationID)
	}
	normalizeWakeConfig(&cfg)
	return cfg
}

func normalizeWakeConfig(cfg *wakeConfig) {
	if cfg.ConversationID <= 0 {
		cfg.ConversationID = 1
	}
	if cfg.MinSilenceMin < 5 {
		cfg.MinSilenceMin = 5
	}
	if cfg.MinSilenceMin > 10080 {
		cfg.MinSilenceMin = 10080
	}
	if cfg.ActivityGraceMin < 1 {
		cfg.ActivityGraceMin = 1
	}
	if cfg.ActivityGraceMin > 1440 {
		cfg.ActivityGraceMin = 1440
	}
	if cfg.CooldownMin < 5 {
		cfg.CooldownMin = 5
	}
	if cfg.CooldownMin > 20160 {
		cfg.CooldownMin = 20160
	}
	if cfg.DailyLimit < 1 {
		cfg.DailyLimit = 1
	}
	if cfg.DailyLimit > 500 {
		cfg.DailyLimit = 500
	}
	if cfg.EvaluationCooldownMin < 5 {
		cfg.EvaluationCooldownMin = defaultWakeEvaluationCooldownMin
	}
	if cfg.EvaluationCooldownMin > 20160 {
		cfg.EvaluationCooldownMin = 20160
	}
	if cfg.DailyEvaluationLimit < 1 {
		cfg.DailyEvaluationLimit = defaultWakeDailyEvaluationLimit
	}
	if cfg.DailyEvaluationLimit > 500 {
		cfg.DailyEvaluationLimit = 500
	}
	if cfg.Probability < 0 {
		cfg.Probability = 0
	}
	if cfg.Probability > 1 {
		cfg.Probability = 1
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = resolveModel("")
	}
	if strings.TrimSpace(cfg.Prompt) == "" || cfg.Prompt == legacyWakePrompt || cfg.Prompt == previousDefaultWakePrompt {
		cfg.Prompt = defaultWakePrompt
	}
	if !validClock(cfg.QuietStart) {
		cfg.QuietStart = "23:30"
	}
	if !validClock(cfg.QuietEnd) {
		cfg.QuietEnd = "08:00"
	}
}

func validClock(value string) bool {
	_, err := time.Parse("15:04", value)
	return err == nil
}

func saveWakeConfig(cfg wakeConfig) error {
	normalizeWakeConfig(&cfg)
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO preferences(key,value,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, wakeConfigPref, string(encoded)); err != nil {
		return err
	}
	return tx.Commit()
}

func handleWakeActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ConversationID int64  `json:"conversation_id"`
		Source         string `json:"source"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ConversationID <= 0 {
		req.ConversationID = loadWakeConfig().ConversationID
	}
	if _, err := memory.GetConversation(req.ConversationID); err != nil {
		jsonResp(w, 400, map[string]string{"error": "会话不存在"})
		return
	}
	if strings.TrimSpace(req.Source) == "" {
		req.Source = "web"
	}
	err := recordAppPresence(req.ConversationID, req.Source)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func recordAppPresence(conversationID int64, source string) error {
	_, err := db.DB.Exec(`INSERT INTO wake_activity(id,conversation_id,last_seen_at,source,updated_at) VALUES(1,?,CURRENT_TIMESTAMP,?,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET conversation_id=excluded.conversation_id,last_seen_at=CURRENT_TIMESTAMP,source=excluded.source,updated_at=CURRENT_TIMESTAMP`, conversationID, truncateRunes(source, 40))
	return err
}

func handleWakeConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := loadWakeConfig()
		rhys := loadModelChannel(replyChannel)
		var lastSeen string
		_ = db.DB.QueryRow(`SELECT COALESCE(last_seen_at,'') FROM wake_activity WHERE id=1`).Scan(&lastSeen)
		jsonResp(w, 200, map[string]interface{}{"config": cfg, "has_api_key": strings.TrimSpace(rhys.APIKey) != "", "last_seen_at": lastSeen})
	case http.MethodPost:
		var incoming wakeConfig
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			jsonResp(w, 400, map[string]string{"error": "配置格式错误"})
			return
		}
		normalizeWakeConfig(&incoming)
		if _, err := memory.GetConversation(incoming.ConversationID); err != nil {
			jsonResp(w, 400, map[string]string{"error": "用于自主唤醒的会话不存在"})
			return
		}
		rhys := loadModelChannel(replyChannel)
		if incoming.Enabled && strings.TrimSpace(rhys.APIKey) == "" {
			jsonResp(w, 400, map[string]string{"error": "开启自主唤醒前需要保存 Rhys API Key"})
			return
		}
		if err := saveWakeConfig(incoming); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]interface{}{"config": incoming, "has_api_key": strings.TrimSpace(rhys.APIKey) != "", "saved": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleWakeEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows, err := db.DB.Query(`SELECT id,conversation_id,decision,reason,COALESCE(message_id,0),created_at FROM wake_events ORDER BY id DESC LIMIT 30`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []map[string]interface{}{}
	for rows.Next() {
		var id, cid, mid int64
		var decision, reason, created string
		if rows.Scan(&id, &cid, &decision, &reason, &mid, &created) == nil {
			items = append(items, map[string]interface{}{"id": id, "conversation_id": cid, "decision": decision, "reason": reason, "message_id": mid, "created_at": created})
		}
	}
	jsonResp(w, 200, map[string]interface{}{"events": items})
}

func handleWakeEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := evaluateWake(true)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, result)
}

func StartWakeMonitor() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := evaluateWake(false); err != nil {
				log.Printf("自主唤醒检查失败: %v", err)
			}
		}
	}()
}

func evaluateWake(manual bool) (map[string]interface{}, error) {
	if !wakeMu.TryLock() {
		return map[string]interface{}{"decision": "busy", "reason": "已有唤醒检查正在进行"}, nil
	}
	defer wakeMu.Unlock()
	cfg := loadWakeConfig()
	cfg.ConversationID = latestWakeConversationID(cfg.ConversationID)
	assistantName := assistantNameForConversation(cfg.ConversationID)
	rhys := loadModelChannel(replyChannel)
	if !cfg.Enabled && !manual {
		return map[string]interface{}{"decision": "disabled"}, nil
	}
	if strings.TrimSpace(rhys.APIKey) == "" {
		return recordWake(cfg.ConversationID, "error", "Rhys 通道没有可用的 API Key", 0)
	}
	now := time.Now().In(wakeLocation)
	if !manual && inQuietHours(now, cfg.QuietStart, cfg.QuietEnd) {
		return map[string]interface{}{"decision": "quiet"}, nil
	}
	if !manual {
		if hasRecentWakeActivity(now, time.Duration(cfg.ActivityGraceMin)*time.Minute) {
			return map[string]interface{}{"decision": "recent_activity"}, nil
		}
	}
	var lastMessage string
	_ = db.DB.QueryRow(`SELECT COALESCE(MAX(created_at),'') FROM (SELECT created_at FROM messages WHERE conversation_id=? UNION ALL SELECT created_at FROM messages_archive WHERE conversation_id=?)`, cfg.ConversationID, cfg.ConversationID).Scan(&lastMessage)
	lastAt := parseSQLiteTime(lastMessage)
	if !manual && !lastAt.IsZero() && now.Sub(lastAt) < time.Duration(cfg.MinSilenceMin)*time.Minute {
		return map[string]interface{}{"decision": "too_soon"}, nil
	}
	var lastSent string
	_ = db.DB.QueryRow(`SELECT COALESCE(MAX(created_at),'') FROM wake_events WHERE decision='send'`).Scan(&lastSent)
	if !manual {
		if t := parseSQLiteTime(lastSent); !t.IsZero() && now.Sub(t) < time.Duration(cfg.CooldownMin)*time.Minute {
			return map[string]interface{}{"decision": "cooldown"}, nil
		}
	}
	var todayCount int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM wake_events WHERE decision='send' AND date(created_at,'+8 hours')=date('now','+8 hours')`).Scan(&todayCount)
	if !manual && todayCount >= cfg.DailyLimit {
		return map[string]interface{}{"decision": "daily_limit"}, nil
	}
	if !manual {
		evaluationCount, err := wakeModelAttemptsToday()
		if err != nil {
			return nil, err
		}
		if evaluationCount >= cfg.DailyEvaluationLimit {
			return map[string]interface{}{"decision": "evaluation_daily_limit", "reason": "今日模型判断次数已达上限"}, nil
		}
		lastEvaluation, err := lastWakeModelAttempt()
		if err != nil {
			return nil, err
		}
		if !lastEvaluation.IsZero() && now.Sub(lastEvaluation) < time.Duration(cfg.EvaluationCooldownMin)*time.Minute {
			return map[string]interface{}{"decision": "evaluation_cooldown", "reason": "模型判断仍在冷却期"}, nil
		}
	}
	if !manual && rand.Float64() > cfg.Probability {
		return recordWake(cfg.ConversationID, "wait", "概率门未通过，本轮保持安静", 0)
	}

	history, err := memory.GetHistory(cfg.ConversationID, 20)
	if err != nil {
		return nil, err
	}
	prompt := buildWakePrompt(cfg, now, lastAt, history)
	prompt += "\n\n" + wakeCodexDecisionRule
	if codexContext, contextErr := loadWakeCodexContext(); contextErr == nil {
		prompt += "\n\n【Codex 工程状态】\n" + codexContext
	} else {
		prompt += "\n\n【Codex 工程状态】\n当前无法读取 Codex 状态，本轮不得创建 Codex 任务。"
	}
	screen, screenPending := runWakeScreen(manual)
	if screenPending {
		return map[string]interface{}{
			"decision": "screen_pending",
			"phase":    "triggered",
			"reason":   "截图请求已发出，等待 iPhone 自动化回传后再判断",
		}, nil
	}
	screenStatus := wakeScreenStatus(screen)
	messages, err := buildWakeMessages(prompt, screen)
	if err != nil {
		return nil, err
	}
	attemptID, err := startWakeModelAttempt(cfg.ConversationID, manual)
	if err != nil {
		return nil, err
	}
	result, err := CallWithToolsChoice(rhys.APIKey, rhys.BaseURL, cfg.Model, messages, 700, nil, nil)
	if err != nil {
		return recordEvaluatedWake(attemptID, cfg.ConversationID, "error", screenStatus+"；模型判断失败："+err.Error(), 0)
	}
	decision, ok := parseWakeDecision(result.Content)
	if !ok {
		return recordEvaluatedWake(attemptID, cfg.ConversationID, "error", screenStatus+"；模型没有返回有效的自主唤醒 JSON", 0)
	}
	codexNote := ""
	if taskID, taskErr := enqueueWakeCodexTask(decision.CodexTask); taskErr != nil {
		observability.Event("wake.codex_task_skipped", map[string]interface{}{"error": taskErr.Error()})
		codexNote = "；Codex 未入队：" + taskErr.Error()
	} else if taskID > 0 {
		observability.Event("wake.codex_task_queued", map[string]interface{}{"task_id": taskID, "title": decision.CodexTask.Title})
		codexNote = "；已创建 Codex 任务 #" + strconv.FormatInt(taskID, 10)
	}
	if decision.Decision != "send" || decision.Message == nil || strings.TrimSpace(decision.Message.Reply) == "" {
		return recordEvaluatedWake(attemptID, cfg.ConversationID, "wait", screenStatus+"；"+firstNonEmpty(decision.Reason, assistantName+" 决定继续等待")+codexNote, 0)
	}
	stored, _ := json.Marshal(decision.Message)
	messageID, err := memory.SaveMessage(cfg.ConversationID, "assistant", string(stored))
	if err != nil {
		_ = finishWakeModelAttempt(attemptID, "message_store_error", 0)
		return nil, err
	}
	_ = memory.SyncConversationJSONL(cfg.ConversationID)
	go sendWakePush(cfg.ConversationID, messageID, decision.Message.Reply, cfg.PushPreview)
	observability.Event("wake.sent", map[string]interface{}{"conversation_id": cfg.ConversationID, "message_id": messageID, "reason": decision.Reason})
	return recordEvaluatedWake(attemptID, cfg.ConversationID, "send", screenStatus+"；"+firstNonEmpty(decision.Reason, assistantName+" 决定主动联系")+codexNote, messageID)
}

func runWakeScreen(manual bool) (screenToolResult, bool) {
	if manual {
		// Manual tests keep the synchronous behavior: the user is waiting for
		// the image and should receive the model judgment in the same request.
		return runSeeScreenTool(), false
	}
	if strings.TrimSpace(os.Getenv("SCREEN_PEEK_TOKEN")) == "" {
		return screenToolResult{Text: "error:SCREEN_PEEK_TOKEN 未配置，iPhone 截图功能尚未启用。"}, false
	}

	now := time.Now().UTC()
	wakeScreenRequest.Lock()
	requestedAt := wakeScreenRequest.requestedAt
	if !requestedAt.IsZero() {
		if now.Sub(requestedAt) >= wakeScreenRequestTTL {
			wakeScreenRequest.requestedAt = time.Time{}
			wakeScreenRequest.Unlock()
			return screenToolResult{Text: "error:等待 iPhone 新截图超时；本轮将仅结合最近对话判断"}, false
		}
		if shot, ok := takeScreenShotAfter(requestedAt); ok {
			wakeScreenRequest.requestedAt = time.Time{}
			wakeScreenRequest.Unlock()
			return screenResultFromShot(shot, "以下图片是本次自主唤醒请求后由用户 iPhone 上传的当前屏幕截图。"), false
		}
		// Automatic wake is deliberately two-phase. The monitor must return
		// here and let a later tick consume the upload; never hold the worker
		// while iOS Mail/Shortcuts delivers the screenshot.
		wakeScreenRequest.Unlock()
		return screenToolResult{}, true
	}
	wakeScreenRequest.requestedAt = now
	wakeScreenRequest.Unlock()

	if err := sendScreenTriggerEmail(); err != nil {
		wakeScreenRequest.Lock()
		if wakeScreenRequest.requestedAt.Equal(now) {
			wakeScreenRequest.requestedAt = time.Time{}
		}
		wakeScreenRequest.Unlock()
		return screenToolResult{Text: "error:无法发送 iPhone 截图触发邮件：" + err.Error()}, false
	}
	observability.Event("wake.screen_requested", map[string]interface{}{"requested_at": now.Format(time.RFC3339Nano)})
	return screenToolResult{}, true
}

func wakeScreenStatus(screen screenToolResult) string {
	if screen.Path != "" {
		capturedAt := screen.CapturedAt
		if capturedAt.IsZero() {
			return "截图成功"
		}
		return "截图成功（" + capturedAt.In(wakeLocation).Format("2006-01-02 15:04:05 MST") + "）"
	}
	detail := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(screen.Text), "error:"))
	if detail == "" {
		detail = "未取得新截图"
	}
	return "截图失败（" + detail + "）"
}

func latestWakeConversationID(fallback int64) int64 {
	var conversationID int64
	err := db.DB.QueryRow(`
		SELECT recent.conversation_id FROM (
			SELECT id,conversation_id,created_at FROM messages WHERE role='user'
			UNION ALL
			SELECT id,conversation_id,created_at FROM messages_archive WHERE role='user'
		) recent
		JOIN conversations c ON c.id=recent.conversation_id
		WHERE lower(COALESCE(c.assistant,'rhys'))!='grok'
		ORDER BY datetime(recent.created_at) DESC,recent.id DESC LIMIT 1`).Scan(&conversationID)
	if err == nil && conversationID > 0 {
		return conversationID
	}
	if fallback > 0 && !strings.EqualFold(memory.ConversationAssistant(fallback), "grok") {
		return fallback
	}
	if err := db.DB.QueryRow(`SELECT id FROM conversations WHERE lower(COALESCE(assistant,'rhys'))!='grok' ORDER BY datetime(updated_at) DESC,id DESC LIMIT 1`).Scan(&conversationID); err == nil && conversationID > 0 {
		return conversationID
	}
	return 1
}

func hasRecentWakeActivity(now time.Time, grace time.Duration) bool {
	var lastSeen string
	if err := db.DB.QueryRow(`SELECT COALESCE(last_seen_at,'') FROM wake_activity WHERE id=1`).Scan(&lastSeen); err != nil {
		return false
	}
	seen := parseSQLiteTime(lastSeen)
	return !seen.IsZero() && now.Sub(seen) < grace
}

func buildWakePrompt(cfg wakeConfig, now, lastAt time.Time, history []memory.Message) string {
	lines := make([]string, 0, len(history))
	for _, m := range history {
		content := m.Content
		if m.Role == "assistant" {
			content = parseStoredRoleReply(content).Reply
		}
		lines = append(lines, fmt.Sprintf("%s: %s", m.Role, truncateRunes(content, 500)))
	}
	silence := "没有消息记录"
	if !lastAt.IsZero() {
		silence = now.Sub(lastAt).Round(time.Minute).String()
	}
	return buildSystemPrompt(cfg.ConversationID, nil) + "\n\n【当前助手身份】\n你是" + assistantNameForConversation(cfg.ConversationID) + "，必须按该身份自称和行动。\n\n" + cfg.Prompt + "\n\n【客观状态】\n当前时间：" + now.Format(time.RFC3339) + "\n距会话最后一条消息：" + silence + "\n今日已主动发送：" + strconv.Itoa(wakeSentToday()) + " / " + strconv.Itoa(cfg.DailyLimit) + "\n最近对话：\n" + strings.Join(lines, "\n")
}

func buildWakeMessages(prompt string, screen screenToolResult) ([]ChatMessage, error) {
	if screen.Path == "" {
		return buildWakeMessagesWithoutScreen(prompt, screen.Text), nil
	}
	dataURL, err := fileDataURL(screen.Path, screen.MIMEType)
	if err != nil {
		return buildWakeMessagesWithoutScreen(prompt, "无法读取本次屏幕截图："+err.Error()), nil
	}
	return []ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: []ContentPart{
			{Type: "text", Text: "这是一次自主唤醒判断。以下是本次判断前刚取得的当前屏幕截图。结合截图、最近对话和客观状态，决定现在发消息还是继续等待，并在 reason 中说明决定依据。"},
			{Type: "image_url", ImageURL: &ImageURLPart{URL: dataURL}},
		}},
	}, nil
}

func buildWakeMessagesWithoutScreen(prompt, detail string) []ChatMessage {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "本次未取得屏幕截图"
	}
	return []ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: "这是一次自主唤醒判断，但本次窥屏失败或超时，当前没有可用的屏幕信息（" + detail + "）。忽略任务中必须观察截图及描述截图的要求，不要猜测用户当前屏幕；仅结合最近对话、关系状态、当前时间、沉默时长等已有内容，决定现在发消息还是继续等待，并在 reason 中说明依据。"},
	}
}

func parseWakeDecision(raw string) (wakeDecision, bool) {
	var d wakeDecision
	candidate := strings.TrimSpace(raw)
	if start := strings.Index(candidate, "{"); start >= 0 {
		if end := strings.LastIndex(candidate, "}"); end > start {
			candidate = candidate[start : end+1]
		}
	}
	if json.Unmarshal([]byte(candidate), &d) != nil {
		return d, false
	}
	d.Decision = strings.ToLower(strings.TrimSpace(d.Decision))
	return d, d.Decision == "send" || d.Decision == "wait"
}
func parseStoredRoleReply(raw string) roleReply { parsed, _ := parseRoleReply(raw); return parsed }
func wakeSentToday() int {
	var count int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM wake_events WHERE decision='send' AND date(created_at,'+8 hours')=date('now','+8 hours')`).Scan(&count)
	return count
}
func wakeModelAttemptsToday() (int, error) {
	var count int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM wake_model_attempts WHERE date(created_at,'+8 hours')=date('now','+8 hours')`).Scan(&count)
	return count, err
}
func lastWakeModelAttempt() (time.Time, error) {
	var value string
	if err := db.DB.QueryRow(`SELECT COALESCE(MAX(created_at),'') FROM wake_model_attempts`).Scan(&value); err != nil {
		return time.Time{}, err
	}
	return parseSQLiteTime(value), nil
}
func startWakeModelAttempt(conversationID int64, manual bool) (int64, error) {
	manualValue := 0
	if manual {
		manualValue = 1
	}
	result, err := db.DB.Exec(`INSERT INTO wake_model_attempts(conversation_id,manual,outcome) VALUES(?,?,'started')`, conversationID, manualValue)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
func finishWakeModelAttempt(attemptID int64, outcome string, wakeEventID int64) error {
	_, err := db.DB.Exec(`UPDATE wake_model_attempts SET outcome=?,wake_event_id=NULLIF(?,0),completed_at=CURRENT_TIMESTAMP WHERE id=?`, truncateRunes(outcome, 40), wakeEventID, attemptID)
	return err
}
func recordEvaluatedWake(attemptID, cid int64, decision, reason string, messageID int64) (map[string]interface{}, error) {
	result, err := recordWake(cid, decision, reason, messageID)
	if err != nil {
		_ = finishWakeModelAttempt(attemptID, "record_error", 0)
		return nil, err
	}
	eventID, _ := result["id"].(int64)
	if err := finishWakeModelAttempt(attemptID, decision, eventID); err != nil {
		return nil, err
	}
	return result, nil
}
func recordWake(cid int64, decision, reason string, messageID int64) (map[string]interface{}, error) {
	res, err := db.DB.Exec(`INSERT INTO wake_events(conversation_id,decision,reason,message_id) VALUES(?,?,?,NULLIF(?,0))`, cid, decision, truncateRunes(reason, 1000), messageID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return map[string]interface{}{"id": id, "conversation_id": cid, "decision": decision, "reason": reason, "message_id": messageID}, nil
}
func parseSQLiteTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC); err == nil {
		return t
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}
func inQuietHours(now time.Time, start, end string) bool {
	s, _ := time.Parse("15:04", start)
	e, _ := time.Parse("15:04", end)
	minute := now.Hour()*60 + now.Minute()
	sm := s.Hour()*60 + s.Minute()
	em := e.Hour()*60 + e.Minute()
	if sm == em {
		return false
	}
	if sm < em {
		return minute >= sm && minute < em
	}
	return minute >= sm || minute < em
}
