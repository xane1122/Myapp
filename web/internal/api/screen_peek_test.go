package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
}

func resetScreenPeekTestState() {
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{}
	screenPeekState.notify = make(chan struct{}, 1)
	screenPeekState.flight = nil
	screenPeekState.Unlock()
}

func TestScreenPeekCoalescesConcurrentRequests(t *testing.T) {
	resetScreenPeekTestState()
	first, leader := beginScreenPeek()
	if first == nil || !leader {
		t.Fatalf("expected first request to lead, got flight=%v leader=%v", first, leader)
	}
	second, leader := beginScreenPeek()
	if second != first || leader {
		t.Fatalf("expected second request to join flight, got flight=%v leader=%v", second, leader)
	}
	want := screenToolResult{Text: "ok:true", Path: "/new.png", MIMEType: "image/png"}
	finishScreenPeek(first, want)
	select {
	case <-second.done:
		if second.result.Path != want.Path {
			t.Fatalf("unexpected shared result: %#v", second.result)
		}
	default:
		t.Fatal("joined request was not released")
	}
}

func TestScreenPeekNeverReusesCompletedCapture(t *testing.T) {
	resetScreenPeekTestState()
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{Path: "/history.png", CapturedAt: time.Now().UTC()}
	screenPeekState.Unlock()

	flight, leader := beginScreenPeek()
	if flight == nil || !leader {
		t.Fatalf("historical capture must not satisfy a new request: flight=%v leader=%v", flight, leader)
	}
}

func TestScreenUploadRequiresToken(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "correct-token")
	t.Setenv("SCREEN_PEEK_DATA_DIR", t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/screen/upload", bytes.NewReader(tinyPNG))
	req.Header.Set("Content-Type", "image/png")
	rec := httptest.NewRecorder()
	handleScreenUpload(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenPeekStatusReportsConfigurationWithoutSecrets(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "secret-token")
	t.Setenv("SCREEN_PEEK_SMTP_HOST", "smtp.example.com")
	t.Setenv("SCREEN_PEEK_SMTP_USER", "sender@example.com")
	t.Setenv("SCREEN_PEEK_SMTP_PASSWORD", "secret-password")
	t.Setenv("SCREEN_PEEK_TO_EMAIL", "phone@example.com")
	req := httptest.NewRequest(http.MethodGet, "/api/screen-peek/status", nil)
	rec := httptest.NewRecorder()
	handleScreenPeekStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["enabled"] != true || response["token_configured"] != true || response["smtp_configured"] != true {
		t.Fatalf("unexpected status: %#v", response)
	}
	if strings.Contains(rec.Body.String(), "secret-token") || strings.Contains(rec.Body.String(), "secret-password") || strings.Contains(rec.Body.String(), "@example.com") {
		t.Fatalf("status leaked configuration: %s", rec.Body.String())
	}
}

func TestScreenPeekStatusReportsLatestCapture(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "token")
	t.Setenv("SCREEN_PEEK_SMTP_HOST", "smtp.example.com")
	t.Setenv("SCREEN_PEEK_SMTP_USER", "sender@example.com")
	t.Setenv("SCREEN_PEEK_SMTP_PASSWORD", "password")
	t.Setenv("SCREEN_PEEK_TO_EMAIL", "phone@example.com")
	screenPeekState.Lock()
	screenPeekState.latest = screenShot{Path: "/private/latest.png", CapturedAt: time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)}
	screenPeekState.Unlock()

	rec := httptest.NewRecorder()
	handleScreenPeekStatus(rec, httptest.NewRequest(http.MethodGet, "/api/screen-peek/status", nil))
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["capture_received"] != true || response["latest_capture_at"] != "2026-07-14T20:30:00+08:00" {
		t.Fatalf("unexpected status: %#v", response)
	}
	if strings.Contains(rec.Body.String(), "/private/latest.png") {
		t.Fatalf("status leaked screenshot path: %s", rec.Body.String())
	}
}

func TestScreenPeekStatusRejectsUnsupportedMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	handleScreenPeekStatus(rec, httptest.NewRequest(http.MethodPost, "/api/screen-peek/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestScreenUploadSavesPrivateImage(t *testing.T) {
	resetScreenPeekTestState()
	dir := t.TempDir()
	t.Setenv("SCREEN_PEEK_TOKEN", "correct-token")
	t.Setenv("SCREEN_PEEK_DATA_DIR", dir)
	req := httptest.NewRequest(http.MethodPost, "/api/screen/upload", bytes.NewReader(tinyPNG))
	req.Header.Set("Authorization", "Bearer correct-token")
	req.Header.Set("Content-Type", "image/png")
	rec := httptest.NewRecorder()
	handleScreenUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response["ok"] != true {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
	shot, ok := screenShotAfter(screenPeekState.latest.CapturedAt.Add(-1))
	if !ok || !strings.HasPrefix(shot.Path, dir+string(os.PathSeparator)) {
		t.Fatalf("unexpected stored screenshot: %#v", shot)
	}
	info, err := os.Stat(shot.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected private file mode 0600, got %o", info.Mode().Perm())
	}
	if filepath.Ext(shot.Path) != ".png" {
		t.Fatalf("expected png extension, got %s", shot.Path)
	}
}

func TestScreenUploadWithDedicatedTokenPassesGlobalAPIAuth(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("APP_BEARER_TOKEN", "app-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	t.Setenv("SCREEN_PEEK_TOKEN", "screen-token")
	t.Setenv("SCREEN_PEEK_DATA_DIR", t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/screen/upload?token=screen-token", bytes.NewReader(tinyPNG))
	req.Header.Set("Content-Type", "image/png")
	rec := httptest.NewRecorder()
	withAPIAuth(http.HandlerFunc(handleScreenUpload)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenUploadWithWrongDedicatedTokenIsRejectedAfterGlobalAPIAuth(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("APP_BEARER_TOKEN", "app-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	t.Setenv("SCREEN_PEEK_TOKEN", "screen-token")
	t.Setenv("SCREEN_PEEK_DATA_DIR", t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/screen/upload?token=wrong", bytes.NewReader(tinyPNG))
	rec := httptest.NewRecorder()
	withAPIAuth(http.HandlerFunc(handleScreenUpload)).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenUploadRejectsNonImage(t *testing.T) {
	resetScreenPeekTestState()
	t.Setenv("SCREEN_PEEK_TOKEN", "token")
	t.Setenv("SCREEN_PEEK_DATA_DIR", t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/screen/upload?token=token", strings.NewReader("not an image"))
	rec := httptest.NewRecorder()
	handleScreenUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSeeScreenIntentForcesConfiguredTool(t *testing.T) {
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "see_screen"}}}
	choice := chatToolChoice("你看看我的屏幕上是什么", tools)
	raw, _ := json.Marshal(choice)
	if !strings.Contains(string(raw), "see_screen") {
		t.Fatalf("expected see_screen choice, got %s", raw)
	}
}

func TestSeeScreenWithoutConfigFailsFast(t *testing.T) {
	t.Setenv("SCREEN_PEEK_TOKEN", "")
	result := runSeeScreenTool()
	if result.Path != "" || !strings.Contains(result.Text, "未配置") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestScreenPeekLive(t *testing.T) {
	if os.Getenv("SCREEN_PEEK_LIVE_TEST") != "1" {
		t.Skip("set SCREEN_PEEK_LIVE_TEST=1 to trigger the iPhone automation")
	}
	started := time.Now().UTC()
	if err := sendScreenTriggerEmail(); err != nil {
		t.Fatalf("live trigger email failed: %v", err)
	}
	statusURL := strings.TrimSpace(os.Getenv("SCREEN_PEEK_STATUS_URL"))
	if statusURL == "" {
		statusURL = "https://xanelove.com/api/screen-peek/status"
	}
	deadline := time.Now().Add(screenPeekTimeout())
	for time.Now().Before(deadline) {
		response, err := http.Get(statusURL)
		if err == nil {
			var status struct {
				LatestCaptureAt string `json:"latest_capture_at"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			if decodeErr == nil && status.LatestCaptureAt != "" {
				capturedAt, parseErr := time.Parse(time.RFC3339Nano, status.LatestCaptureAt)
				if parseErr == nil && capturedAt.After(started) {
					t.Logf("fresh screenshot received at %s", capturedAt.Format(time.RFC3339Nano))
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("no fresh screenshot arrived after %s", started.Format(time.RFC3339Nano))
}

func TestScreenPeekTimeoutAllowsDelayedMailAutomation(t *testing.T) {
	t.Setenv("SCREEN_PEEK_TIMEOUT_SECONDS", "120")
	if got := screenPeekTimeout(); got != 120*time.Second {
		t.Fatalf("expected 120s timeout, got %s", got)
	}

	t.Setenv("SCREEN_PEEK_TIMEOUT_SECONDS", "181")
	if got := screenPeekTimeout(); got != defaultScreenPeekTimeout {
		t.Fatalf("expected default timeout above maximum, got %s", got)
	}
}

func TestSMTPLoginAuthRequiresTLSAndReturnsCredentialsInOrder(t *testing.T) {
	auth := &smtpLoginAuth{username: "sender@example.com", password: "secret", host: "smtp.example.com"}
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Fatal("expected plaintext SMTP authentication to be rejected")
	}
	mechanism, initial, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true})
	if err != nil || mechanism != "LOGIN" || initial != nil {
		t.Fatalf("unexpected start: mechanism=%q initial=%q err=%v", mechanism, initial, err)
	}
	username, err := auth.Next([]byte("Username:"), true)
	if err != nil || string(username) != "sender@example.com" {
		t.Fatalf("unexpected username response: %q, %v", username, err)
	}
	password, err := auth.Next([]byte("Password:"), true)
	if err != nil || string(password) != "secret" {
		t.Fatalf("unexpected password response: %q, %v", password, err)
	}
	if _, err := auth.Next(nil, false); err != nil {
		t.Fatalf("unexpected completion error: %v", err)
	}
}
