package api

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"myapp/internal/observability"
)

const defaultScreenPeekTimeout = 45 * time.Second
const screenPeekSMTPTimeout = 15 * time.Second

type screenShot struct {
	Path       string
	MIMEType   string
	CapturedAt time.Time
	Consumed   bool
}

type screenToolResult struct {
	Text       string
	Path       string
	MIMEType   string
	CapturedAt time.Time
}

var screenPeekState = struct {
	sync.Mutex
	latest screenShot
	notify chan struct{}
	flight *screenPeekFlight
}{notify: make(chan struct{}, 1)}

type screenPeekFlight struct {
	done   chan struct{}
	result screenToolResult
}

func handleScreenPeekStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tokenConfigured := strings.TrimSpace(os.Getenv("SCREEN_PEEK_TOKEN")) != ""
	smtpConfigured := strings.TrimSpace(os.Getenv("SCREEN_PEEK_SMTP_HOST")) != "" &&
		strings.TrimSpace(os.Getenv("SCREEN_PEEK_SMTP_USER")) != "" &&
		strings.TrimSpace(os.Getenv("SCREEN_PEEK_SMTP_PASSWORD")) != "" &&
		strings.TrimSpace(os.Getenv("SCREEN_PEEK_TO_EMAIL")) != ""
	screenPeekState.Lock()
	latest := screenPeekState.latest
	screenPeekState.Unlock()
	response := map[string]interface{}{
		"enabled":          tokenConfigured && smtpConfigured,
		"token_configured": tokenConfigured,
		"smtp_configured":  smtpConfigured,
		"capture_received": latest.Path != "",
	}
	if !latest.CapturedAt.IsZero() {
		response["latest_capture_at"] = latest.CapturedAt.Format(time.RFC3339)
	}
	jsonResp(w, http.StatusOK, response)
}

func handleScreenUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	configured := strings.TrimSpace(os.Getenv("SCREEN_PEEK_TOKEN"))
	supplied := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if supplied == "" {
		supplied = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if configured == "" || subtle.ConstantTimeCompare([]byte(configured), []byte(supplied)) != 1 {
		jsonResp(w, http.StatusForbidden, map[string]string{"error": "截图上传令牌无效"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	raw, err := readScreenUpload(r)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	mimeType, ext, ok := sniffImage(raw, "")
	if !ok || mimeType == "image/gif" || mimeType == "image/bmp" || mimeType == "image/heic" {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "屏幕截图仅支持 PNG、JPEG 或 WebP"})
		return
	}
	dir := screenPeekDataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "无法创建截图目录"})
		return
	}
	now := time.Now().UTC()
	path := filepath.Join(dir, fmt.Sprintf("screen_%d%s", now.UnixNano(), ext))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "无法保存截图"})
		return
	}

	screenPeekState.Lock()
	previous := screenPeekState.latest.Path
	screenPeekState.latest = screenShot{Path: path, MIMEType: mimeType, CapturedAt: now}
	screenPeekState.Unlock()
	if previous != "" && previous != path {
		_ = os.Remove(previous)
	}
	observability.Event("screen.capture_received", map[string]interface{}{
		"captured_at": now.Format(time.RFC3339Nano),
		"mime_type":   mimeType,
		"bytes":       len(raw),
	})
	select {
	case screenPeekState.notify <- struct{}{}:
	default:
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"ok": true, "captured_at": now.Format(time.RFC3339)})
}

func readScreenUpload(r *http.Request) ([]byte, error) {
	contentType := r.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			return nil, fmt.Errorf("截图过大或表单格式错误")
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, fmt.Errorf("表单缺少 file 字段")
		}
		defer file.Close()
		return readLimitedScreen(file)
	}
	return readLimitedScreen(r.Body)
}

func readLimitedScreen(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取截图失败")
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("截图内容为空")
	}
	if len(raw) > maxUploadBytes {
		return nil, fmt.Errorf("截图不能超过12MB")
	}
	return raw, nil
}

func runSeeScreenTool() screenToolResult {
	if strings.TrimSpace(os.Getenv("SCREEN_PEEK_TOKEN")) == "" {
		return screenToolResult{Text: "error:SCREEN_PEEK_TOKEN 未配置，iPhone 截图功能尚未启用。"}
	}
	flight, leader := beginScreenPeek()
	if !leader {
		<-flight.done
		return flight.result
	}
	result := runNewScreenPeek()
	finishScreenPeek(flight, result)
	return result
}

func beginScreenPeek() (*screenPeekFlight, bool) {
	screenPeekState.Lock()
	defer screenPeekState.Unlock()
	if screenPeekState.flight != nil {
		return screenPeekState.flight, false
	}
	flight := &screenPeekFlight{done: make(chan struct{})}
	screenPeekState.flight = flight
	return flight, true
}

func finishScreenPeek(flight *screenPeekFlight, result screenToolResult) {
	screenPeekState.Lock()
	flight.result = result
	if screenPeekState.flight == flight {
		screenPeekState.flight = nil
	}
	close(flight.done)
	screenPeekState.Unlock()
}

func screenResultFromShot(shot screenShot, note string) screenToolResult {
	return screenToolResult{
		Text:       "ok:true\ncaptured_at:" + shot.CapturedAt.In(wakeLocation).Format(time.RFC3339) + "\n说明:" + note,
		Path:       shot.Path,
		MIMEType:   shot.MIMEType,
		CapturedAt: shot.CapturedAt,
	}
}

func runNewScreenPeek() screenToolResult {
	started := time.Now().UTC()
	if err := sendScreenTriggerEmail(); err != nil {
		return screenToolResult{Text: "error:无法发送 iPhone 截图触发邮件：" + err.Error()}
	}
	timer := time.NewTimer(screenPeekTimeout())
	defer timer.Stop()
	for {
		if shot, ok := takeScreenShotAfter(started); ok {
			return screenResultFromShot(shot, "以下图片是本次请求后由用户 iPhone 上传的当前屏幕截图。")
		}
		select {
		case <-screenPeekState.notify:
		case <-timer.C:
			return screenToolResult{Text: "error:等待 iPhone 新截图超时；没有使用历史截图。请确认邮件自动化已设为立即运行。"}
		}
	}
}

func screenShotAfter(started time.Time) (screenShot, bool) {
	screenPeekState.Lock()
	defer screenPeekState.Unlock()
	shot := screenPeekState.latest
	return shot, shot.Path != "" && shot.CapturedAt.After(started)
}

func takeScreenShotAfter(started time.Time) (screenShot, bool) {
	screenPeekState.Lock()
	defer screenPeekState.Unlock()
	shot := screenPeekState.latest
	if shot.Path == "" || shot.Consumed || !shot.CapturedAt.After(started) {
		return screenShot{}, false
	}
	screenPeekState.latest.Consumed = true
	return shot, true
}

func sendScreenTriggerEmail() error {
	host := strings.TrimSpace(os.Getenv("SCREEN_PEEK_SMTP_HOST"))
	port := strings.TrimSpace(os.Getenv("SCREEN_PEEK_SMTP_PORT"))
	user := strings.TrimSpace(os.Getenv("SCREEN_PEEK_SMTP_USER"))
	password := os.Getenv("SCREEN_PEEK_SMTP_PASSWORD")
	to := strings.TrimSpace(os.Getenv("SCREEN_PEEK_TO_EMAIL"))
	if host == "" || user == "" || password == "" || to == "" {
		return fmt.Errorf("SMTP 配置不完整")
	}
	if port == "" {
		port = "587"
	}
	addr := host + ":" + port
	auth := &smtpLoginAuth{username: user, password: password, host: host}
	subject := strings.TrimSpace(os.Getenv("SCREEN_PEEK_EMAIL_SUBJECT"))
	if subject == "" {
		subject = "SCREEN_PEEK"
	}
	message := []byte("From: " + user + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"Authorized screen request at " + time.Now().UTC().Format(time.RFC3339) + "\r\n")
	if err := sendScreenTriggerSMTP(addr, host, user, to, auth, message, port == "465"); err != nil {
		return err
	}
	observability.Event("screen.trigger_email_sent", map[string]interface{}{
		"sent_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	return nil
}

func sendScreenTriggerSMTP(addr, host, from, to string, auth smtp.Auth, message []byte, implicitTLS bool) error {
	plainConn, err := net.DialTimeout("tcp", addr, screenPeekSMTPTimeout)
	if err != nil {
		return err
	}
	defer plainConn.Close()
	if err := plainConn.SetDeadline(time.Now().Add(screenPeekSMTPTimeout)); err != nil {
		return err
	}
	var conn net.Conn = plainConn
	if implicitTLS {
		tlsConn := tls.Client(plainConn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			return err
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !implicitTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// QQ Mail accepts AUTH LOGIN but may omit AUTH from its EHLO capability list.
// A custom Auth also avoids PlainAuth's provider-specific capability check.
type smtpLoginAuth struct {
	username string
	password string
	host     string
	step     int
}

func (a *smtpLoginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, fmt.Errorf("SMTP LOGIN requires TLS")
	}
	if !strings.EqualFold(server.Name, a.host) {
		return "", nil, fmt.Errorf("SMTP server name mismatch")
	}
	a.step = 0
	return "LOGIN", nil, nil
}

func (a *smtpLoginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch a.step {
	case 0:
		a.step++
		return []byte(a.username), nil
	case 1:
		a.step++
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("unexpected SMTP LOGIN challenge")
	}
}

func screenPeekTimeout() time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SCREEN_PEEK_TIMEOUT_SECONDS")))
	if err != nil || seconds < 5 || seconds > 180 {
		return defaultScreenPeekTimeout
	}
	return time.Duration(seconds) * time.Second
}

func screenPeekDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("SCREEN_PEEK_DATA_DIR")); dir != "" {
		return dir
	}
	return filepath.Join("data", "screen_peek")
}

func screenToolJSON(result screenToolResult) string {
	raw, _ := json.Marshal(map[string]interface{}{
		"result":      result.Text,
		"has_image":   result.Path != "",
		"captured_at": result.CapturedAt.In(wakeLocation).Format(time.RFC3339),
	})
	return string(raw)
}
