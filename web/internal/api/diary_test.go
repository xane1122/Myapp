package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"myapp/internal/db"
)

func TestGenerateDiaryUsesOnlyMessagesAfterLastWindow(t *testing.T) {
	setupAPITestDB(t)
	_, err := db.DB.Exec(`INSERT INTO messages(conversation_id,role,content,created_at) VALUES
		(1,'user','旧消息','2026-07-25 00:30:00'),
		(1,'user','窗口内的具体事情','2026-07-25 01:30:00'),
		(1,'assistant','窗口内的回答','2026-07-25 02:00:00')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO ai_diary_state(conversation_id,window_started_at,last_generated_at) VALUES(1,'2026-07-25 00:00:00','2026-07-25 01:00:00')`); err != nil {
		t.Fatal(err)
	}

	originalCall := callDiaryModel
	t.Cleanup(func() { callDiaryModel = originalCall })
	var prompt string
	callDiaryModel = func(_, _, _ string, messages []ChatMessage, _ int) (string, error) {
		prompt = fmt.Sprint(messages[0].Content)
		return "第一段具体内容。\n\n第二段具体内容。\n\n第三段具体内容。", nil
	}

	now := time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)
	entry, err := generateDiary(1, "manual", "test-key", "", "test-model", now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "旧消息") || !strings.Contains(prompt, "窗口内的具体事情") || !strings.Contains(prompt, "窗口内的回答") {
		t.Fatalf("unexpected prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "Rhys 第一人称") || !strings.Contains(prompt, "3 至 5 个自然段") {
		t.Fatalf("diary constraints missing from prompt: %s", prompt)
	}
	if entry.TriggerType != "manual" || entry.DiaryDate != "2026-07-25" {
		t.Fatalf("entry = %#v", entry)
	}
	var lastGenerated string
	if err := db.DB.QueryRow(`SELECT last_generated_at FROM ai_diary_state WHERE conversation_id=1`).Scan(&lastGenerated); err != nil {
		t.Fatal(err)
	}
	lastGeneratedAt, err := parseDiaryDBTime(lastGenerated)
	if err != nil || !lastGeneratedAt.Equal(now) {
		t.Fatalf("last_generated_at = %q, err = %v", lastGenerated, err)
	}
}

func TestGenerateDiaryDoesNotAdvanceEmptyWindow(t *testing.T) {
	setupAPITestDB(t)
	start := time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)
	if _, err := db.DB.Exec(`INSERT INTO ai_diary_state(conversation_id,window_started_at) VALUES(1,?)`, start.Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	_, err := generateDiary(1, "manual", "test-key", "", "test-model", start.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "没有新的对话") {
		t.Fatalf("error = %v", err)
	}
	var lastGenerated *string
	if err := db.DB.QueryRow(`SELECT last_generated_at FROM ai_diary_state WHERE conversation_id=1`).Scan(&lastGenerated); err != nil {
		t.Fatal(err)
	}
	if lastGenerated != nil {
		t.Fatalf("empty window advanced to %q", *lastGenerated)
	}
}

func TestEvaluateAutomaticDiariesWaitsForSilenceAndDeduplicatesDay(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("ALLOW_SERVER_API_KEY", "true")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	_, err := db.DB.Exec(`INSERT INTO messages(conversation_id,role,content,created_at) VALUES(1,'user','五小时前的对话','2026-07-25 06:59:00')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO ai_diary_state(conversation_id,window_started_at) VALUES(1,'2026-07-25 06:00:00')`); err != nil {
		t.Fatal(err)
	}

	originalCall := callDiaryModel
	t.Cleanup(func() { callDiaryModel = originalCall })
	calls := 0
	callDiaryModel = func(_, _, _ string, _ []ChatMessage, _ int) (string, error) {
		calls++
		return "第一段。\n\n第二段。\n\n第三段。", nil
	}
	if err := evaluateAutomaticDiaries(now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateAutomaticDiaries(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want 1", calls)
	}
	entries, err := listDiaries(1)
	if err != nil || len(entries) != 1 || entries[0].TriggerType != "automatic" {
		t.Fatalf("entries = %#v, err = %v", entries, err)
	}
}

func TestEvaluateAutomaticDiariesUsesCheapModelAndBacksOffAfterFailure(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("ALLOW_SERVER_API_KEY", "true")
	t.Setenv("MAIN_MODEL", "expensive-model")
	t.Setenv("CHEAP_MODEL", "diary-cheap-model")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	if _, err := db.DB.Exec(`INSERT INTO messages(conversation_id,role,content,created_at) VALUES(1,'user','五小时前的对话','2026-07-25 06:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO ai_diary_state(conversation_id,window_started_at) VALUES(1,'2026-07-25 05:00:00')`); err != nil {
		t.Fatal(err)
	}

	originalCall := callDiaryModel
	diaryGenerationMu.Lock()
	diaryAutomaticRetryAfter = map[int64]time.Time{}
	diaryGenerationMu.Unlock()
	t.Cleanup(func() {
		callDiaryModel = originalCall
		diaryGenerationMu.Lock()
		diaryAutomaticRetryAfter = map[int64]time.Time{}
		diaryGenerationMu.Unlock()
	})
	calls := 0
	callDiaryModel = func(_, _, model string, _ []ChatMessage, _ int) (string, error) {
		calls++
		if model != "diary-cheap-model" {
			t.Fatalf("model = %q", model)
		}
		return "", fmt.Errorf("provider unavailable")
	}
	if err := evaluateAutomaticDiaries(now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateAutomaticDiaries(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls during retry delay = %d, want 1", calls)
	}
}

func TestEvaluateAutomaticDiariesUsesStoredReplyChannelA(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("ALLOW_SERVER_API_KEY", "false")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	if _, err := db.DB.Exec(`INSERT INTO messages(conversation_id,role,content,created_at) VALUES(1,'user','五小时前的对话','2026-07-25 06:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO ai_diary_state(conversation_id,window_started_at) VALUES(1,'2026-07-25 05:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO model_channel_configs(channel,base_url,model,assistant_name,api_key) VALUES('reply','https://channel.example/v1','channel-model','Rhys','channel-key')
		ON CONFLICT(channel) DO UPDATE SET base_url=excluded.base_url,model=excluded.model,assistant_name=excluded.assistant_name,api_key=excluded.api_key`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO model_channel_configs(channel,base_url,model,assistant_name,api_key) VALUES('memory_rhys','https://memory.example/v1','memory-model','Rhys','memory-key')
		ON CONFLICT(channel) DO UPDATE SET base_url=excluded.base_url,model=excluded.model,assistant_name=excluded.assistant_name,api_key=excluded.api_key`); err != nil {
		t.Fatal(err)
	}

	originalCall := callDiaryModel
	diaryGenerationMu.Lock()
	diaryAutomaticRetryAfter = map[int64]time.Time{}
	diaryGenerationMu.Unlock()
	t.Cleanup(func() {
		callDiaryModel = originalCall
		diaryGenerationMu.Lock()
		diaryAutomaticRetryAfter = map[int64]time.Time{}
		diaryGenerationMu.Unlock()
	})
	callDiaryModel = func(key, baseURL, model string, _ []ChatMessage, _ int) (string, error) {
		if key != "channel-key" || baseURL != "https://channel.example/v1" || model != "channel-model" {
			t.Fatalf("provider = key:%q base:%q model:%q", key, baseURL, model)
		}
		return "第一段。\n\n第二段。\n\n第三段。", nil
	}

	if err := evaluateAutomaticDiaries(now); err != nil {
		t.Fatal(err)
	}
	entries, err := listDiaries(1)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %#v, err = %v", entries, err)
	}
}

func TestLimitDiaryMessagesKeepsNewestWithinBudget(t *testing.T) {
	messages := []diaryMessage{{Content: "old"}, {Content: strings.Repeat("中", 80)}, {Content: "new"}}
	selected, omitted := limitDiaryMessages(messages, 70)
	if omitted != 2 || len(selected) != 1 || selected[0].Content != "new" {
		t.Fatalf("selected=%#v omitted=%d", selected, omitted)
	}

	selected, omitted = limitDiaryMessages([]diaryMessage{{Content: strings.Repeat("字", 100)}}, 20)
	if omitted != 0 || len([]rune(selected[0].Content)) != 20 {
		t.Fatalf("oversized message runes=%d omitted=%d", len([]rune(selected[0].Content)), omitted)
	}
}

func TestDiaryGenerationLockAllowsManualToSupersedeAutomatic(t *testing.T) {
	diaryGenerationMu.Lock()
	diaryGenerating = map[int64]string{}
	diaryGenerationMu.Unlock()
	t.Cleanup(func() {
		diaryGenerationMu.Lock()
		diaryGenerating = map[int64]string{}
		diaryGenerationMu.Unlock()
	})
	if !beginDiaryGeneration(1, "automatic") {
		t.Fatal("automatic generation did not start")
	}
	if !beginDiaryGeneration(1, "manual") {
		t.Fatal("manual generation did not supersede automatic generation")
	}
	if beginDiaryGeneration(1, "manual") || beginDiaryGeneration(1, "automatic") {
		t.Fatal("parallel generation was accepted")
	}
	if got := diaryGenerationType(1); got != "manual" {
		t.Fatalf("generation type = %q", got)
	}
}

func TestDiaryHTTPGenerateListAndStrictJSON(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	if _, err := db.DB.Exec(`INSERT INTO messages(conversation_id,role,content,created_at) VALUES(1,'user','记下这件事','2026-07-25 01:00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO ai_diary_state(conversation_id,window_started_at) VALUES(1,'2026-07-25 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	originalCall, originalNow := callDiaryModel, diaryNow
	t.Cleanup(func() { callDiaryModel, diaryNow = originalCall, originalNow })
	diaryNow = func() time.Time { return time.Date(2026, 7, 25, 2, 0, 0, 0, time.UTC) }
	callDiaryModel = func(_, _, _ string, _ []ChatMessage, _ int) (string, error) {
		return "第一段。\n\n第二段。\n\n第三段。", nil
	}
	h := Handler()

	request := httptest.NewRequest(http.MethodPost, "/api/diaries", bytes.NewBufferString(`{"conversation_id":1,"api_key":"test-key"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://www.xanelove.com")
	request.Header.Set("X-Forwarded-Host", "www.xanelove.com")
	request.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/diaries?conversation_id=1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Diaries []diaryEntry `json:"diaries"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Diaries) != 1 {
		t.Fatalf("response = %s, err = %v", recorder.Body.String(), err)
	}

	for _, body := range []string{
		`{"conversation_id":1,"api_key":"test-key","unexpected":true}`,
		`{"conversation_id":1,"api_key":"test-key"}{}`,
	} {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/api/diaries", strings.NewReader(body))
		h.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, recorder.Code)
		}
	}
}

func TestDiaryHTTPRejectsForeignOriginBeforeGeneration(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")

	originalCall := callDiaryModel
	t.Cleanup(func() { callDiaryModel = originalCall })
	called := false
	callDiaryModel = func(_, _, _ string, _ []ChatMessage, _ int) (string, error) {
		called = true
		return "不应生成", nil
	}

	request := httptest.NewRequest(http.MethodPost, "/api/diaries", bytes.NewBufferString(`{"conversation_id":1,"api_key":"test-key"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-Forwarded-Host", "www.xanelove.com")
	request.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST status = %d, want 403; body = %s", recorder.Code, recorder.Body.String())
	}
	if called {
		t.Fatal("foreign-origin request reached diary generation")
	}
}
