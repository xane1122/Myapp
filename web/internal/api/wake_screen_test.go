package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"myapp/internal/db"
)

func TestWakeConfigUsesRhysChannelKeyState(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(replyChannel, modelChannelInput{
		APIKey:  modelChannelString(""),
		BaseURL: modelChannelString(""),
		Model:   modelChannelString("rhys-model"),
	}); err != nil {
		t.Fatal(err)
	}

	missing := httptest.NewRecorder()
	handleWakeConfig(missing, httptest.NewRequest(http.MethodPost, "/api/wake/config", strings.NewReader(`{"enabled":true,"conversation_id":1}`)))
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "Rhys API Key") {
		t.Fatalf("missing Rhys key status=%d body=%s", missing.Code, missing.Body.String())
	}

	if err := saveModelChannel(replyChannel, modelChannelInput{
		APIKey:  modelChannelString("rhys-key"),
		BaseURL: modelChannelString("https://rhys.example/v1"),
		Model:   modelChannelString("rhys-model"),
	}); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	handleWakeConfig(get, httptest.NewRequest(http.MethodGet, "/api/wake/config", nil))
	var result struct {
		HasAPIKey bool `json:"has_api_key"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.HasAPIKey {
		t.Fatal("wake config did not report the Rhys channel key")
	}
}

func TestBuildWakePromptIncludesCustomPrompt(t *testing.T) {
	setupAPITestDB(t)
	prompt := buildWakePrompt(wakeConfig{ConversationID: 1, DailyLimit: 2, Prompt: "只在确实关心用户时主动联系。"}, time.Now(), time.Time{}, nil)
	if !strings.Contains(prompt, "只在确实关心用户时主动联系。") {
		t.Fatalf("custom wake prompt missing: %s", prompt)
	}
	if !strings.Contains(prompt, "【客观状态】") {
		t.Fatalf("objective wake state missing: %s", prompt)
	}
	if !strings.Contains(defaultWakePrompt, "codex_task") {
		t.Fatal("default wake prompt must include Codex task decision")
	}
}

func TestNormalizeWakeConfigUpgradesLegacyPrompt(t *testing.T) {
	cfg := wakeConfig{Prompt: legacyWakePrompt}
	normalizeWakeConfig(&cfg)
	if cfg.Prompt != defaultWakePrompt {
		t.Fatal("legacy wake prompt was not upgraded")
	}
	if !strings.Contains(cfg.Prompt, "角色心情") {
		t.Fatalf("upgraded prompt does not include screenshot mood guidance: %s", cfg.Prompt)
	}
}

func TestNormalizeWakeConfigUpgradesPreviousDefaultPrompt(t *testing.T) {
	cfg := wakeConfig{Prompt: previousDefaultWakePrompt}
	normalizeWakeConfig(&cfg)
	if cfg.Prompt != defaultWakePrompt || !strings.Contains(cfg.Prompt, "codex_task") {
		t.Fatal("previous default wake prompt was not upgraded")
	}
}

func TestNormalizeWakeConfigAllowsUpToFiveHundredDailyMessages(t *testing.T) {
	cfg := wakeConfig{DailyLimit: 500}
	normalizeWakeConfig(&cfg)
	if cfg.DailyLimit != 500 {
		t.Fatalf("daily limit = %d, want 500", cfg.DailyLimit)
	}
	cfg.DailyLimit = 501
	normalizeWakeConfig(&cfg)
	if cfg.DailyLimit != 500 {
		t.Fatalf("clamped daily limit = %d, want 500", cfg.DailyLimit)
	}
}

func TestNormalizeWakeConfigAllowsFiveMinuteTiming(t *testing.T) {
	cfg := wakeConfig{MinSilenceMin: 5, CooldownMin: 5, EvaluationCooldownMin: 5}
	normalizeWakeConfig(&cfg)
	if cfg.MinSilenceMin != 5 || cfg.CooldownMin != 5 || cfg.EvaluationCooldownMin != 5 {
		t.Fatalf("timing = silence %d, send cooldown %d, evaluation cooldown %d; want 5, 5 and 5", cfg.MinSilenceMin, cfg.CooldownMin, cfg.EvaluationCooldownMin)
	}
	cfg.MinSilenceMin = 1
	cfg.CooldownMin = 1
	cfg.EvaluationCooldownMin = 1
	normalizeWakeConfig(&cfg)
	if cfg.MinSilenceMin != 5 || cfg.CooldownMin != 5 || cfg.EvaluationCooldownMin != defaultWakeEvaluationCooldownMin {
		t.Fatalf("clamped timing = silence %d, send cooldown %d, evaluation cooldown %d", cfg.MinSilenceMin, cfg.CooldownMin, cfg.EvaluationCooldownMin)
	}
}

func TestNormalizeWakeConfigDefaultsEvaluationGuards(t *testing.T) {
	cfg := wakeConfig{}
	normalizeWakeConfig(&cfg)
	if cfg.EvaluationCooldownMin != 30 {
		t.Fatalf("evaluation cooldown = %d, want 30", cfg.EvaluationCooldownMin)
	}
	if cfg.DailyEvaluationLimit != 50 {
		t.Fatalf("daily evaluation limit = %d, want 50", cfg.DailyEvaluationLimit)
	}
}

func TestBuildWakeMessagesIncludesFreshScreenImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "screen.png")
	if err := os.WriteFile(path, tinyPNG, 0600); err != nil {
		t.Fatal(err)
	}
	messages, err := buildWakeMessages("wake prompt", screenToolResult{Path: path, MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	parts, ok := messages[1].Content.([]ContentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("wake screen image missing: %#v", messages[1].Content)
	}
}

func TestBuildWakeMessagesFallsBackWhenScreenFileIsMissing(t *testing.T) {
	messages, err := buildWakeMessages("wake prompt", screenToolResult{Path: "/missing/screen.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != "user" {
		t.Fatalf("unexpected fallback messages: %#v", messages)
	}
	content, ok := messages[1].Content.(string)
	if !ok || !strings.Contains(content, "当前没有可用的屏幕信息") || !strings.Contains(content, "仅结合最近对话") {
		t.Fatalf("missing no-screen fallback guidance: %#v", messages[1].Content)
	}
}

func TestBuildWakeMessagesFallsBackWhenScreenPeekTimesOut(t *testing.T) {
	messages, err := buildWakeMessages("wake prompt", screenToolResult{Text: "error:等待 iPhone 新截图超时"})
	if err != nil {
		t.Fatal(err)
	}
	content, ok := messages[1].Content.(string)
	if !ok || !strings.Contains(content, "等待 iPhone 新截图超时") || !strings.Contains(content, "不要猜测用户当前屏幕") {
		t.Fatalf("missing timeout fallback guidance: %#v", messages[1].Content)
	}
}

func TestWakeScreenStatusMarksSuccessAndFailure(t *testing.T) {
	capturedAt := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	if got := wakeScreenStatus(screenToolResult{Path: "/screen.png", CapturedAt: capturedAt}); !strings.Contains(got, "截图成功") || !strings.Contains(got, "2026-07-17 11:00:00") {
		t.Fatalf("success status = %q", got)
	}
	if got := wakeScreenStatus(screenToolResult{Text: "error:等待 iPhone 新截图超时"}); got != "截图失败（等待 iPhone 新截图超时）" {
		t.Fatalf("failure status = %q", got)
	}
}

func TestAutomaticWakeConsumesOnlyCaptureAfterPendingRequest(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "token")
	requestedAt := time.Now().UTC().Add(-time.Minute)
	wakeScreenRequest.Lock()
	wakeScreenRequest.requestedAt = requestedAt
	wakeScreenRequest.Unlock()
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{Path: "/fresh.png", MIMEType: "image/png", CapturedAt: requestedAt.Add(time.Second)}
	screenPeekState.Unlock()

	result, pending := runWakeScreen(false)
	if pending || result.Path != "/fresh.png" {
		t.Fatalf("expected fresh pending capture, got pending=%v result=%#v", pending, result)
	}
	wakeScreenRequest.Lock()
	remaining := wakeScreenRequest.requestedAt
	wakeScreenRequest.Unlock()
	if !remaining.IsZero() {
		t.Fatalf("expected consumed request to be cleared, got %s", remaining)
	}
}

func TestAutomaticWakeKeepsWaitingWithoutFreshCapture(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "token")
	requestedAt := time.Now().UTC().Add(-time.Minute)
	wakeScreenRequest.Lock()
	wakeScreenRequest.requestedAt = requestedAt
	wakeScreenRequest.Unlock()
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{Path: "/history.png", MIMEType: "image/png", CapturedAt: requestedAt.Add(-time.Second)}
	screenPeekState.Unlock()

	result, pending := runWakeScreen(false)
	if !pending || result.Path != "" {
		t.Fatalf("expected pending result without historical reuse, got pending=%v result=%#v", pending, result)
	}
}

func TestAutomaticWakeContinuesToJudgmentAfterScreenTimeout(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "token")
	wakeScreenRequest.Lock()
	wakeScreenRequest.requestedAt = time.Now().UTC().Add(-wakeScreenRequestTTL - time.Second)
	wakeScreenRequest.Unlock()

	result, pending := runWakeScreen(false)
	if pending || !strings.Contains(result.Text, "截图超时") {
		t.Fatalf("expected timeout result to continue judgment, got pending=%v result=%#v", pending, result)
	}
	wakeScreenRequest.Lock()
	remaining := wakeScreenRequest.requestedAt
	wakeScreenRequest.Unlock()
	if !remaining.IsZero() {
		t.Fatalf("expected expired request to be cleared, got %s", remaining)
	}
}

func TestAutomaticWakeRejectsCaptureFromExpiredRequest(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "token")
	requestedAt := time.Now().UTC().Add(-wakeScreenRequestTTL - time.Minute)
	wakeScreenRequest.Lock()
	wakeScreenRequest.requestedAt = requestedAt
	wakeScreenRequest.Unlock()
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{
		Path:       "/stale-manual-capture.png",
		MIMEType:   "image/png",
		CapturedAt: requestedAt.Add(time.Minute),
	}
	screenPeekState.Unlock()

	result, pending := runWakeScreen(false)
	if pending || result.Path != "" || !strings.Contains(result.Text, "截图超时") {
		t.Fatalf("expired request reused stale capture: pending=%v result=%#v", pending, result)
	}
	wakeScreenRequest.Lock()
	remaining := wakeScreenRequest.requestedAt
	wakeScreenRequest.Unlock()
	if !remaining.IsZero() {
		t.Fatalf("expected expired request to be cleared, got %s", remaining)
	}
}

func TestHasRecentWakeActivityBlocksAcrossConversations(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now().Truncate(time.Second)
	if _, err := db.DB.Exec(`INSERT INTO wake_activity(id,conversation_id,last_seen_at,source,updated_at) VALUES(1,?,?,?,?)`, 39, now.Format("2006-01-02 15:04:05"), "pwa", now.Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	if !hasRecentWakeActivity(now, 10*time.Minute) {
		t.Fatal("activity anywhere in the app should block autonomous wake")
	}
}

func TestWakeModelAttemptTrackingSupportsCooldownAndDailyLimit(t *testing.T) {
	setupAPITestDB(t)
	attemptID, err := startWakeModelAttempt(1, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishWakeModelAttempt(attemptID, "wait", 0); err != nil {
		t.Fatal(err)
	}
	count, err := wakeModelAttemptsToday()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attempt count = %d, want 1", count)
	}
	last, err := lastWakeModelAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if last.IsZero() || time.Since(last) > time.Minute {
		t.Fatalf("last attempt = %s, want a recent timestamp", last)
	}
}

func TestLatestWakeConversationUsesNewestRhysUserMessage(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO conversations(id,title,assistant) VALUES(2,'grok','grok'),(3,'rhys','rhys')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO messages(conversation_id,role,content,created_at) VALUES(1,'user','older','2026-07-15 10:00:00'),(3,'user','newer rhys','2026-07-15 11:00:00'),(2,'user','newest grok','2026-07-15 12:00:00')`); err != nil {
		t.Fatal(err)
	}
	if got := latestWakeConversationID(1); got != 3 {
		t.Fatalf("conversation = %d, want newest Rhys user conversation 3", got)
	}
}

func TestLatestWakeConversationRejectsGrokFallback(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO conversations(id,title,assistant,updated_at) VALUES(2,'grok','grok','2026-07-15 12:00:00'),(3,'rhys','rhys','2026-07-15 11:00:00')`); err != nil {
		t.Fatal(err)
	}
	got := latestWakeConversationID(2)
	if got == 2 {
		t.Fatalf("conversation = %d, autonomous wake must reject Grok fallback", got)
	}
}

func TestLatestWakeConversationFallsBackWithoutUserMessages(t *testing.T) {
	setupAPITestDB(t)
	if got := latestWakeConversationID(1); got != 1 {
		t.Fatalf("conversation = %d, want fallback 1", got)
	}
}

func TestParseSQLiteTimeTreatsDatabaseTimestampAsUTC(t *testing.T) {
	got := parseSQLiteTime("2026-07-16 04:15:46")
	if got.Location() != time.UTC || got.Hour() != 4 {
		t.Fatalf("database timestamp should remain UTC: %v", got)
	}
	if beijingHour := got.In(wakeLocation).Hour(); beijingHour != 12 {
		t.Fatalf("expected 12:00 in Beijing, got hour %d", beijingHour)
	}
}

func TestWakePromptUsesBeijingTime(t *testing.T) {
	setupAPITestDB(t)
	now := time.Date(2026, 7, 16, 12, 30, 0, 0, wakeLocation)
	prompt := buildWakePrompt(wakeConfig{ConversationID: 1, DailyLimit: 2, Prompt: "test"}, now, time.Time{}, nil)
	if !strings.Contains(prompt, "2026-07-16T12:30:00+08:00") {
		t.Fatalf("wake prompt should contain Beijing time: %s", prompt)
	}
}
