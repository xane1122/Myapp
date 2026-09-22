package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"myapp/internal/db"
)

func TestBellCRUDCheckinAndStats(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "bells.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()

	body := []byte(`{"title":"喝水","note":"八杯","kind":"mine","frequency":"daily","reminder_time":"09:30","push_target":"both","enabled":true}`)
	created := bellRequest(t, h, http.MethodPost, "/api/bells", body, http.StatusOK)
	bellData := created["bell"].(map[string]interface{})
	id := int64(bellData["id"].(float64))

	list := bellRequest(t, h, http.MethodGet, "/api/bells", nil, http.StatusOK)
	if got := len(list["bells"].([]interface{})); got != 1 {
		t.Fatalf("bells length = %d", got)
	}

	checked := bellRequest(t, h, http.MethodPost, "/api/bells/1/checkins", nil, http.StatusOK)
	if checked["completed"] != true {
		t.Fatalf("completed = %#v", checked["completed"])
	}
	stats := bellRequest(t, h, http.MethodGet, "/api/bells/stats", nil, http.StatusOK)
	if stats["today_completed"].(float64) != 1 {
		t.Fatalf("stats = %#v", stats)
	}

	updatedBody := []byte(`{"title":"喝温水","note":"","kind":"ai","frequency":"weekly","reminder_time":"10:00","push_target":"ai","enabled":true}`)
	updated := bellRequest(t, h, http.MethodPut, "/api/bells/"+jsonNumber(id), updatedBody, http.StatusOK)
	if updated["bell"].(map[string]interface{})["title"] != "喝温水" {
		t.Fatalf("updated = %#v", updated)
	}
	bellRequest(t, h, http.MethodDelete, "/api/bells/"+jsonNumber(id), nil, http.StatusOK)
	bellRequest(t, h, http.MethodGet, "/api/bells/"+jsonNumber(id), nil, http.StatusNotFound)
}

func TestBellCreateToolIsForcedAndWritesAsAI(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "bell-tool.sqlite"))
	if !explicitBellCreateRequested("帮我在铃铛里新增一个喝水提醒") {
		t.Fatal("bell request was not detected")
	}
	names := explicitToolNames("帮我在铃铛里新增一个喝水提醒")
	if len(names) != 1 || names[0] != "bell_create" {
		t.Fatalf("tool names=%v", names)
	}
	result := runBellCreateTool(`{"title":"喝水","note":"喝一杯温水","frequency":"daily","reminder_time":"09:30"}`, 1)
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("result=%s", result)
	}
	var title, kind, target string
	var enabled int
	if err := db.DB.QueryRow(`SELECT title,kind,push_target,enabled FROM bells`).Scan(&title, &kind, &target, &enabled); err != nil {
		t.Fatal(err)
	}
	if title != "喝水" || kind != "ai" || target != "me" || enabled != 1 {
		t.Fatalf("title=%q kind=%q target=%q enabled=%d", title, kind, target, enabled)
	}
}

func TestBellCreateToolRejectsInvalidArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"title":"","frequency":"daily","reminder_time":"09:30"}`,
		`{"title":"喝水","frequency":"sometimes","reminder_time":"09:30"}`,
		`{"title":"喝水","frequency":"daily","reminder_time":"25:00"}`,
	} {
		if result := runBellCreateTool(arguments, 1); !strings.Contains(result, `"error"`) {
			t.Fatalf("arguments=%s result=%s", arguments, result)
		}
	}
}

func TestBellScheduleAndDeliveryDeduplication(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "bell-schedule.sqlite"))
	_, err := db.DB.Exec(`INSERT INTO bells(title,note,kind,frequency,reminder_time,reminder_interval,push_target,enabled,created_at)
		VALUES('喝水','','mine','daily','09:00','1h','me',1,'2026-07-19 00:00:00')`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, wakeLocation)
	var delivered []dueBell
	processDueBells(now, func(b dueBell) { delivered = append(delivered, b) })
	processDueBells(now, func(b dueBell) { delivered = append(delivered, b) })
	if len(delivered) != 1 || delivered[0].Title != "喝水" {
		t.Fatalf("delivered=%#v", delivered)
	}
	if _, err = db.DB.Exec(`INSERT INTO bell_checkins(bell_id,checkin_date) VALUES(1,'2026-07-20')`); err != nil {
		t.Fatal(err)
	}
	processDueBells(now.Add(time.Hour), func(b dueBell) { delivered = append(delivered, b) })
	if len(delivered) != 1 {
		t.Fatalf("completed bell delivered again: %#v", delivered)
	}
}

func TestBellCompletionPersistsAcrossDays(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO bells(id,title,kind,frequency,reminder_time,push_target,enabled) VALUES(1,'喝水','mine','daily','09:00','me',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO bell_checkins(bell_id,checkin_date) VALUES(1,'2026-07-20')`); err != nil {
		t.Fatal(err)
	}
	b, err := scanBell(db.DB.QueryRow(bellSelect+` WHERE b.id=?`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Completed {
		t.Fatal("completed bell became incomplete after its check-in date")
	}
	var delivered []dueBell
	processDueBells(time.Date(2026, 7, 21, 9, 0, 0, 0, wakeLocation), func(item dueBell) { delivered = append(delivered, item) })
	if len(delivered) != 0 {
		t.Fatalf("completed bell delivered on a later day: %#v", delivered)
	}
}

func TestBellScheduleFrequencyAndValidation(t *testing.T) {
	monday := time.Date(2026, 7, 20, 9, 0, 0, 0, wakeLocation)
	if _, ok := bellScheduledAt("weekdays", "09:00", "", "2026-07-19 00:00:00", monday); !ok {
		t.Fatal("weekday bell was not due")
	}
	if _, ok := bellScheduledAt("weekdays", "09:00", "", "2026-07-19 00:00:00", monday.AddDate(0, 0, 5)); ok {
		t.Fatal("weekday bell was due on Saturday")
	}
	for _, value := range []string{"0m", "25h", "hourly", ""} {
		if _, ok := parseBellInterval(value); ok {
			t.Fatalf("interval %q accepted", value)
		}
	}
	for _, value := range []string{"1m", "30m", "1h", "24h"} {
		if _, ok := parseBellInterval(value); !ok {
			t.Fatalf("interval %q rejected", value)
		}
	}
	jan31 := "2026-01-31 09:00:00"
	feb28 := time.Date(2026, 2, 28, 9, 0, 0, 0, wakeLocation)
	if _, ok := bellScheduledAt("monthly", "09:00", "", jan31, feb28); !ok {
		t.Fatal("monthly bell created on day 31 should run on the last day of a shorter month")
	}
}

func TestBellRejectsUnsafeJSONAndOversizedFields(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	valid := `{"title":"喝水","kind":"mine","frequency":"daily","reminder_time":"09:30","push_target":"me","enabled":true}`
	for _, body := range []string{
		valid + `{}`,
		`{"title":"喝水","kind":"mine","frequency":"daily","reminder_time":"09:30","push_target":"me","enabled":true,"admin":true}`,
		`{"title":"` + strings.Repeat("字", 81) + `","kind":"mine","frequency":"daily","reminder_time":"09:30","push_target":"me","enabled":true}`,
		`{"title":"喝水","note":"` + strings.Repeat("字", 501) + `","kind":"mine","frequency":"daily","reminder_time":"09:30","push_target":"me","enabled":true}`,
	} {
		bellRequest(t, h, http.MethodPost, "/api/bells", []byte(body), http.StatusBadRequest)
	}
}

func TestGenerateBellNotificationFallsBackWithoutServerAPI(t *testing.T) {
	t.Setenv("ALLOW_SERVER_API_KEY", "false")
	t.Setenv("OPENROUTER_BASE_URL", "")
	b := dueBell{Title: "喝水", Note: "记得喝一杯温水", PushTarget: "me"}
	if got, want := generateBellNotificationText(b), bellNotificationText(b); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestActiveDeadlinesSystemPrompt(t *testing.T) {
	setupAPITestDB(t)
	_, err := db.DB.Exec(`INSERT INTO bells(title,note,deadline,enabled) VALUES
		('交报告','提交最终版本','2026-07-21',1),
		('已过期','不应注入','2026-07-18',1),
		('无截止日期','','',1),
		('已禁用','','2026-07-22',0)`)
	if err != nil {
		t.Fatal(err)
	}
	prompt := activeDeadlinesSystemPrompt(time.Date(2026, 7, 19, 12, 0, 0, 0, wakeLocation))
	for _, want := range []string{"当前活跃截止日期", "2026-07-21", "交报告", "还有2天"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("deadline prompt missing %q: %s", want, prompt)
		}
	}
	for _, unwanted := range []string{"已过期", "无截止日期", "已禁用"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("deadline prompt contains %q: %s", unwanted, prompt)
		}
	}
}

func bellRequest(t *testing.T, h http.Handler, method, path string, body []byte, status int) map[string]interface{} {
	t.Helper()
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonNumber(v int64) string { b, _ := json.Marshal(v); return string(b) }
