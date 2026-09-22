package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	contextbuilder "myapp/internal/contextbuilder"
	"myapp/internal/db"
	"myapp/internal/memory"
)

type flushTrackingWriter struct {
	header    http.Header
	body      bytes.Buffer
	snapshots []string
}

func TestAudioHandlerServesMP3(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-08-04-42.mp3"), []byte("ID3-test-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := withAudioCache(http.StripPrefix("/audio/", http.FileServer(http.Dir(dir))))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/audio/2026-08-04-42.mp3", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ID3-test-audio" {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type=%q", got)
	}
}

func TestRoleplayPageDisablesBrowserCaching(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "roleplay"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "roleplay", "index.html"), []byte("roleplay-v855"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/roleplay", "/roleplay.html"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			staticHandler(root).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path+"?v=855", nil))
			if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/roleplay/" {
				t.Fatalf("status = %d location = %q", recorder.Code, recorder.Header().Get("Location"))
			}
		})
	}
	recorder := httptest.NewRecorder()
	staticHandler(root).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/roleplay/?v=855", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "roleplay-v855") {
		t.Fatalf("canonical status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestStandaloneHTMLPagesDisableBrowserCaching(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"mailbox", "games", filepath.Join("games", "ludo")} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "mailbox", "index.html"), []byte("mailbox"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "games", "index.html"), []byte("games"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "games", "ludo", "index.html"), []byte("ludo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := withStaticCache(staticHandler(root))
	for _, path := range []string{"/", "/index.html", "/mailbox", "/mailbox/", "/games", "/games/", "/games/ludo", "/games/ludo/"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if got := recorder.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}

func (w *flushTrackingWriter) Header() http.Header         { return w.header }
func (w *flushTrackingWriter) WriteHeader(int)             {}
func (w *flushTrackingWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *flushTrackingWriter) Flush()                      { w.snapshots = append(w.snapshots, w.body.String()) }

func TestChatEventStreamFlushesEveryDelta(t *testing.T) {
	w := &flushTrackingWriter{header: make(http.Header)}
	stream, err := newChatEventStream(w)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.writeModelDelta("你"); err != nil {
		t.Fatal(err)
	}
	if err := stream.writeModelDelta("好"); err != nil {
		t.Fatal(err)
	}
	if len(w.snapshots) != 3 {
		t.Fatalf("flush count = %d, want initial + 2 deltas", len(w.snapshots))
	}
	if !strings.Contains(w.snapshots[1], `"text":"你"`) || strings.Contains(w.snapshots[1], `"text":"好"`) {
		t.Fatalf("first delta was not independently flushed: %q", w.snapshots[1])
	}
	if !strings.Contains(w.snapshots[2], `"text":"好"`) {
		t.Fatalf("second delta missing: %q", w.snapshots[2])
	}
	if got := w.header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
}

func TestJSONResponseNormalizesTimestampsToBeijing(t *testing.T) {
	recorder := httptest.NewRecorder()
	jsonResp(recorder, http.StatusOK, map[string]interface{}{
		"created_at": "2026-07-20 04:30:00",
		"nested": map[string]interface{}{
			"captured_at": "2026-07-20T04:30:00Z",
			"deadline":    "2026-07-21",
		},
	})
	var got map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["created_at"] != "2026-07-20T12:30:00+08:00" {
		t.Fatalf("created_at=%q", got["created_at"])
	}
	nested := got["nested"].(map[string]interface{})
	if nested["captured_at"] != "2026-07-20T12:30:00+08:00" || nested["deadline"] != "2026-07-21" {
		t.Fatalf("nested=%#v", nested)
	}
}

func TestParseStickerIdentityKeyValue(t *testing.T) {
	got, ok := parseStickerIdentity(`name: 好看
filename: 好看.jpg
tags: 漂亮,好看,夸夸,开心
description: 适合夸对方好看的表情包
mood: happy`)
	if !ok {
		t.Fatal("expected key:value output to parse")
	}
	if got["name"] != "好看" || got["filename"] != "好看.jpg" || got["mood"] != "happy" {
		t.Fatalf("unexpected parse result: %#v", got)
	}
}

func TestSplitMemoryBlocksByCountAndGap(t *testing.T) {
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	msgs := make([]memory.Message, 16)
	for i := range msgs {
		msgs[i] = memory.Message{ID: int64(i + 1), CreatedAt: base.Add(time.Duration(i) * time.Minute).Format("2006-01-02 15:04:05")}
	}
	blocks := splitMemoryBlocks(msgs, false)
	if len(blocks) != 1 || len(blocks[0]) != 15 {
		t.Fatalf("count split = %#v", blocks)
	}
	gapMsgs := []memory.Message{{ID: 1, CreatedAt: base.Format("2006-01-02 15:04:05")}, {ID: 2, CreatedAt: base.Add(31 * time.Minute).Format("2006-01-02 15:04:05")}}
	blocks = splitMemoryBlocks(gapMsgs, false)
	if len(blocks) != 1 || len(blocks[0]) != 1 {
		t.Fatalf("gap split = %#v", blocks)
	}
}

func TestSniffImageRejectsExtensionOnlyAndSVG(t *testing.T) {
	if _, _, ok := sniffImage([]byte("not really a png"), ".png"); ok {
		t.Fatal("extension-only image must be rejected")
	}
	if _, _, ok := sniffImage([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"); ok {
		t.Fatal("svg must be rejected")
	}
	mime, ext, ok := sniffImage([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, "")
	if !ok || mime != "image/png" || ext != ".png" {
		t.Fatalf("png detection=%q %q %v", mime, ext, ok)
	}
}

func TestCreateChatFileAndDownload(t *testing.T) {
	setupAPITestDB(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	var attachmentIDs []int64
	result := runCreateChatFileTool(`{"filename":"guide.html","content":"<!doctype html><h1>Guide</h1>"}`, 1, &attachmentIDs)
	if len(attachmentIDs) != 1 || !strings.Contains(result, `"ok":true`) {
		t.Fatalf("tool result = %s, attachment IDs = %#v", result, attachmentIDs)
	}
	messageID, err := memory.SaveMessage(1, "assistant", "file attached")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.LinkAttachments(messageID, attachmentIDs, 1); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/attachments/download?id="+strconv.FormatInt(attachmentIDs[0], 10), nil)
	recorder := httptest.NewRecorder()
	handleAttachmentDownload(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", response.StatusCode, recorder.Body.String())
	}
	if got := response.Header.Get("Content-Disposition"); !strings.Contains(got, "attachment") || !strings.Contains(got, "guide.html") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := response.Header.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	if recorder.Body.String() != "<!doctype html><h1>Guide</h1>" {
		t.Fatalf("download body = %q", recorder.Body.String())
	}
}

func TestAttachmentPreviewOnlyServesSentHTMLInSandbox(t *testing.T) {
	setupAPITestDB(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	var htmlIDs []int64
	result := runCreateChatFileTool(`{"filename":"preview.html","content":"<!doctype html><button onclick='this.textContent=1'>Run</button>"}`, 1, &htmlIDs)
	if len(htmlIDs) != 1 || !strings.Contains(result, `"ok":true`) {
		t.Fatalf("tool result = %s, attachment IDs = %#v", result, htmlIDs)
	}

	requestPreview := func(id int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/attachments/preview?id="+strconv.FormatInt(id, 10), nil)
		recorder := httptest.NewRecorder()
		handleAttachmentPreview(recorder, req)
		return recorder
	}
	if got := requestPreview(htmlIDs[0]).Code; got != http.StatusNotFound {
		t.Fatalf("unsent HTML preview status = %d", got)
	}

	messageID, err := memory.SaveMessage(1, "assistant", "HTML attached")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.LinkAttachments(messageID, htmlIDs, 1); err != nil {
		t.Fatal(err)
	}
	recorder := requestPreview(htmlIDs[0])
	if recorder.Code != http.StatusOK {
		t.Fatalf("sent HTML preview status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	csp := recorder.Header().Get("Content-Security-Policy")
	for _, policy := range []string{"sandbox allow-scripts", "connect-src 'none'", "script-src 'unsafe-inline' https:"} {
		if !strings.Contains(csp, policy) {
			t.Fatalf("Content-Security-Policy %q does not contain %q", csp, policy)
		}
	}

	var markdownIDs []int64
	result = runCreateChatFileTool(`{"filename":"notes.md","content":"hello"}`, 1, &markdownIDs)
	if len(markdownIDs) != 1 || !strings.Contains(result, `"ok":true`) {
		t.Fatalf("tool result = %s, attachment IDs = %#v", result, markdownIDs)
	}
	markdownMessageID, err := memory.SaveMessage(1, "assistant", "Markdown attached")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.LinkAttachments(markdownMessageID, markdownIDs, 1); err != nil {
		t.Fatal(err)
	}
	markdownRecorder := requestPreview(markdownIDs[0])
	if markdownRecorder.Code != http.StatusOK {
		t.Fatalf("Markdown preview status = %d", markdownRecorder.Code)
	}
	if got := markdownRecorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") {
		t.Fatalf("Markdown preview Content-Type = %q", got)
	}
	if got := markdownRecorder.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
		t.Fatalf("Markdown preview Content-Disposition = %q", got)
	}
	if got := markdownRecorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Fatalf("Markdown preview Content-Security-Policy = %q", got)
	}
}

func TestValidateGeneratedFilename(t *testing.T) {
	for _, name := range []string{"notes.md", "page.HTML"} {
		if _, _, err := validateGeneratedFilename(name); err != nil {
			t.Fatalf("%q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"script.js", "README", ".."} {
		if _, _, err := validateGeneratedFilename(name); err == nil {
			t.Fatalf("%q should be rejected", name)
		}
	}
}

func TestBuildModelMessagesDoesNotReinjectHistoricalImages(t *testing.T) {
	hist := []memory.Message{{Role: "user", Content: "旧照片", Attachments: []memory.Attachment{{OriginalName: "old.png", MimeType: "image/png", FilePath: "/missing"}}}}
	msgs := buildModelMessages("system", hist, "继续", nil)
	if len(msgs) != 3 {
		t.Fatalf("messages=%d", len(msgs))
	}
	content, ok := msgs[1].Content.(string)
	if !ok || !strings.Contains(content, "历史图片 1 张") {
		t.Fatalf("historical content=%#v", msgs[1].Content)
	}
}

func TestSanitizeLinuxImageFilename(t *testing.T) {
	got := sanitizeLinuxImageFilename(` 好 看/$bad;name'.jpg`, ".png")
	if got != "badname.jpg" {
		t.Fatalf("unexpected filename: %q", got)
	}
	got = sanitizeLinuxImageFilename(` 好 看 ;name'.jpg`, ".png")
	if got != "好-看-name.jpg" {
		t.Fatalf("unexpected chinese filename: %q", got)
	}
}

func TestRenameStickerFileUsesCleanFilename(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if err := os.MkdirAll(filepath.Join("uploads", "stickers"), 0755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("uploads", "stickers", "123.jpg")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	path, url := renameStickerFile(src, "/uploads/stickers/123.jpg", "好看.jpg")
	if path != filepath.Join("uploads", "stickers", "好看.jpg") {
		t.Fatalf("unexpected path: %q", path)
	}
	if url != "/uploads/stickers/好看.jpg" {
		t.Fatalf("unexpected url: %q", url)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestStickerSearchTokensKeyValue(t *testing.T) {
	got := stickerSearchTokens("intent:安慰; visual:抱抱; synonyms:安慰,陪你")
	want := []string{"安慰", "抱抱", "陪你"}
	if len(got) != len(want) {
		t.Fatalf("unexpected tokens: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

func TestExplicitTimeRequested(t *testing.T) {
	if !explicitTimeRequested("现在几点了？") {
		t.Fatal("expected Chinese time question to request time tool")
	}
	if !explicitTimeRequested("what is the current time?") {
		t.Fatal("expected English time question to request time tool")
	}
	if explicitTimeRequested("我们聊聊天") {
		t.Fatal("did not expect casual chat to request time tool")
	}
}

func TestRunCurrentTimeTool(t *testing.T) {
	out := runCurrentTimeTool(`{"timezone":"Asia/Shanghai"}`)
	for _, key := range []string{"date:", "time:", "weekday:", "weekday_zh:", "timezone:", "utc_offset:", "unix:", "iso8601:"} {
		if !strings.Contains(out, key) {
			t.Fatalf("time tool output missing %q: %s", key, out)
		}
	}
	if !strings.Contains(out, "utc_offset:+08:00") {
		t.Fatalf("expected Shanghai UTC offset, got: %s", out)
	}
}

func TestHistoryLimitFromRequest(t *testing.T) {
	if got := historyLimitFromRequest(httptest.NewRequest("GET", "/api/history", nil)); got != defaultUIHistoryLimit {
		t.Fatalf("default history limit = %d, want %d", got, defaultUIHistoryLimit)
	}
	if got := historyLimitFromRequest(httptest.NewRequest("GET", "/api/history?limit=42", nil)); got != 42 {
		t.Fatalf("parsed history limit = %d, want 42", got)
	}
	if got := historyLimitFromRequest(httptest.NewRequest("GET", "/api/history?limit=50000", nil)); got != maxUIHistoryLimit {
		t.Fatalf("clamped history limit = %d, want %d", got, maxUIHistoryLimit)
	}
	if got := historyLimitFromRequest(httptest.NewRequest("GET", "/api/history?limit=-1", nil)); got != defaultUIHistoryLimit {
		t.Fatalf("invalid history limit = %d, want %d", got, defaultUIHistoryLimit)
	}
}

func TestHistoryAfterReturnsOnlyCurrentConversationNewMessages(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.EnsureConversation(2); err != nil {
		t.Fatal(err)
	}
	first, err := memory.SaveMessage(1, "user", "旧消息")
	if err != nil {
		t.Fatal(err)
	}
	second, err := memory.SaveMessage(1, "assistant", "新消息")
	if err != nil {
		t.Fatal(err)
	}
	third, err := memory.SaveMessage(1, "user", "更新消息")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := memory.CreateAttachment(1, "photo.png", "/tmp/photo.png", "/uploads/photo.png", "image/png", 123)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.LinkAttachments(third, []int64{attachment.ID}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(2, "user", "其他会话消息"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/history?conversation_id=1&after_id="+strconv.FormatInt(first, 10)+"&limit=10", nil)
	rec := httptest.NewRecorder()
	handleHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		History []historyMessageResp `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.History) != 2 || body.History[0].ID != second || body.History[1].ID != third {
		t.Fatalf("unexpected after_id history: %#v", body.History)
	}
	if body.History[0].ConversationID != 1 || body.History[1].ConversationID != 1 {
		t.Fatalf("unexpected conversation leak in after_id history: %#v", body.History)
	}
	if len(body.History[1].Attachments) != 1 || body.History[1].Attachments[0].ID != attachment.ID {
		t.Fatalf("expected attachment on incremental message, got %#v", body.History[1].Attachments)
	}
}

func TestHistoryLimitOneReturnsLatestMessage(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.SaveMessage(1, "user", "第一条"); err != nil {
		t.Fatal(err)
	}
	latest, err := memory.SaveMessage(1, "assistant", "最新一条")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/history?conversation_id=1&limit=1", nil)
	rec := httptest.NewRecorder()
	handleHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		History []historyMessageResp `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.History) != 1 || body.History[0].ID != latest {
		t.Fatalf("limit=1 history = %#v, want latest id %d", body.History, latest)
	}
}

func TestHistoryAroundRespectsExactLimit(t *testing.T) {
	setupAPITestDB(t)
	var centerID int64
	for i := 0; i < 205; i++ {
		id, err := memory.SaveMessage(1, "user", "历史窗口 "+strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 102 {
			centerID = id
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/history?conversation_id=1&around_id="+strconv.FormatInt(centerID, 10)+"&limit=200", nil)
	rec := httptest.NewRecorder()
	handleHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		History []historyMessageResp `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.History) != 200 {
		t.Fatalf("around history count = %d, want 200", len(body.History))
	}
	found := false
	for _, message := range body.History {
		if message.ID == centerID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("around history omitted center id %d", centerID)
	}
}

func TestMaybeCompressArchivesWithoutDeletingMessages(t *testing.T) {
	setupAPITestDB(t)

	for i := 1; i <= compressAt; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		if _, err := memory.SaveMessage(1, role, "测试消息 "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	maybeCompress(1, "", "")

	total, err := memory.CountMessages(1)
	if err != nil {
		t.Fatal(err)
	}
	if total != compressAt {
		t.Fatalf("auto archive should keep full message history, got %d", total)
	}
	unarchived, err := memory.CountUnarchivedMessages(1)
	if err != nil {
		t.Fatal(err)
	}
	if unarchived != compressAt-compressCount {
		t.Fatalf("unarchived count = %d, want %d", unarchived, compressAt-compressCount)
	}
	chunks, err := memory.ListChunksByScope(1, memory.ScopeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one memory chunk after compression, got %#v", chunks)
	}

	maybeCompress(1, "", "")
	chunks, err = memory.ListChunksByScope(1, memory.ScopeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected second auto compress to wait for more messages, got %#v", chunks)
	}
}

func TestSplitMemoryBlocksOnTopicChange(t *testing.T) {
	msgs := []memory.Message{
		{Role: "user", Content: "咖啡豆和手冲咖啡", CreatedAt: "2026-07-16 10:00:00"},
		{Role: "assistant", Content: "咖啡研磨度可以调细", CreatedAt: "2026-07-16 10:01:00"},
		{Role: "user", Content: "继续说咖啡滤杯", CreatedAt: "2026-07-16 10:02:00"},
		{Role: "assistant", Content: "滤杯会影响萃取", CreatedAt: "2026-07-16 10:03:00"},
		{Role: "user", Content: "周末去海边露营看日出", CreatedAt: "2026-07-16 10:04:00"},
		{Role: "assistant", Content: "记得准备帐篷", CreatedAt: "2026-07-16 10:05:00"},
	}
	blocks := splitMemoryBlocks(msgs, true)
	if len(blocks) != 2 || len(blocks[0]) != 4 || len(blocks[1]) != 2 {
		t.Fatalf("unexpected topic blocks: %#v", blocks)
	}
}

func TestCompressedMemoryGlobalPromotion(t *testing.T) {
	if got := compressedMemoryScope("high", false); got != memory.ScopeGlobal {
		t.Fatalf("high importance scope=%s", got)
	}
	if got := compressedMemoryScope("medium", true); got != memory.ScopeGlobal {
		t.Fatalf("correction scope=%s", got)
	}
	if got := compressedMemoryScope("medium", false); got != memory.ScopeConversation {
		t.Fatalf("ordinary scope=%s", got)
	}
}

func TestExplicitStickerListRequested(t *testing.T) {
	if !explicitStickerListRequested("你现在有哪些表情包？") {
		t.Fatal("expected sticker list question to request list tool")
	}
	choice := chatToolChoice("列出表情包列表", defaultChatTools())
	got, ok := choice.(map[string]interface{})
	if !ok {
		t.Fatalf("expected forced tool choice, got %#v", choice)
	}
	fn := got["function"].(map[string]interface{})
	if fn["name"] != "list_stickers" {
		t.Fatalf("expected list_stickers tool, got %#v", choice)
	}
}

func TestBuildSystemPromptUsesConfigAndFrontendPref(t *testing.T) {
	setupAPITestDB(t)
	promptPath := filepath.Join(t.TempDir(), "system_prompt.md")
	if err := os.WriteFile(promptPath, []byte("配置文件提示词"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSTEM_PROMPT_PATH", promptPath)
	if err := memory.SetPref("system_prompt.rhys", "Rhys 前端补充提示词"); err != nil {
		t.Fatal(err)
	}
	if err := memory.SetPref("system_prompt.grok", "Grok 前端补充提示词"); err != nil {
		t.Fatal(err)
	}
	if err := memory.SetPref("system_prompt", "不得读取的旧公共提示词"); err != nil {
		t.Fatal(err)
	}

	got := buildSystemPrompt(1, nil)
	if !strings.Contains(got, "配置文件提示词") {
		t.Fatalf("system prompt missing config prompt: %s", got)
	}
	if !strings.Contains(got, "【前端基础系统提示词】\nRhys 前端补充提示词") {
		t.Fatalf("system prompt missing frontend prompt: %s", got)
	}
	if strings.Contains(got, "Grok 前端补充提示词") || strings.Contains(got, "不得读取的旧公共提示词") {
		t.Fatalf("Rhys prompt leaked another persona namespace: %s", got)
	}
}

func TestBuildSystemPromptIncludesConversationContextNote(t *testing.T) {
	setupAPITestDB(t)

	if err := memory.SetPref(contextNotePrefKey(1), "这轮对话里不要再提项圈。"); err != nil {
		t.Fatal(err)
	}
	got := buildSystemPrompt(1, nil)
	if !strings.Contains(got, "【当前会话上下文修正】\n这轮对话里不要再提项圈。") {
		t.Fatalf("system prompt missing context note: %s", got)
	}
}

func TestBuildSystemPromptIncludesMemoryIndex(t *testing.T) {
	setupAPITestDB(t)

	if err := memory.SaveChunkWithMetadata(1, memory.ScopeGlobal, "manual", sql.NullInt64{}, "小宝写过一封给爸爸的信，里面提到想要被认真看见。", "给爸爸的信", "关系,爸爸,信,认真看见", memory.ChunkMetadata{TopicLabel: "关系约定", Importance: "high"}); err != nil {
		t.Fatal(err)
	}
	got := buildSystemPrompt(1, nil)
	for _, want := range []string{"【长期记忆索引（摘要，不是全文）】", "#1 关系约定"} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing memory index %q: %s", want, got)
		}
	}
	if !strings.Contains(got, "必须调用 search_memory 并传 memory_id") {
		t.Fatalf("system prompt missing progressive disclosure rule: %s", got)
	}
}

func TestMemoryIndexLimitFromEnv(t *testing.T) {
	t.Setenv("MEMORY_INDEX_LIMIT", "")
	if got := memoryIndexLimit(); got != 8 {
		t.Fatalf("default memory index limit = %d, want 8", got)
	}
	t.Setenv("MEMORY_INDEX_LIMIT", "80")
	if got := memoryIndexLimit(); got != 12 {
		t.Fatalf("env memory index limit = %d, want 12", got)
	}
	t.Setenv("MEMORY_INDEX_LIMIT", "500")
	if got := memoryIndexLimit(); got != 12 {
		t.Fatalf("capped memory index limit = %d, want 12", got)
	}
	t.Setenv("MEMORY_INDEX_LIMIT", "bad")
	if got := memoryIndexLimit(); got != 8 {
		t.Fatalf("invalid memory index limit = %d, want 8", got)
	}
}

func TestSelectRelevantMemoryChunksUsesScopeQuotas(t *testing.T) {
	chunks := []memory.Chunk{
		{ID: 1, Scope: memory.ScopeGlobal, IsCorrection: true},
		{ID: 2, Scope: memory.ScopeGlobal, IsCorrection: true},
		{ID: 3, Scope: memory.ScopeConversation},
		{ID: 4, Scope: memory.ScopeConversation},
		{ID: 5, Scope: memory.ScopeConversation},
		{ID: 6, Scope: memory.ScopeConversation},
		{ID: 7, Scope: memory.ScopeGlobal},
		{ID: 8, Scope: memory.ScopeGlobal},
		{ID: 9, Scope: memory.ScopeGlobal},
	}
	got := selectRelevantMemoryChunks(chunks)
	if len(got) != 6 {
		t.Fatalf("selected %d chunks, want 6: %#v", len(got), got)
	}
	want := []int64{1, 3, 4, 5, 7, 8}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("selected ids=%#v, want %#v", got, want)
		}
	}
}

func TestFormatContextPreviewShowsCoreSections(t *testing.T) {
	hist := []memory.Message{{ID: 7, Role: "user", Content: "你好"}}
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "get_current_time", Description: "获取时间"}}}
	usage := usageResp{UsedTokens: 12, ContextWindow: 120000, Ratio: 0.0001}

	got := formatContextPreview("test-model", "系统提示", hist, "现在几点", tools, forceToolChoice("get_current_time"), usage)
	for _, want := range []string{"# 当前模型上下文预览", "## system", "系统提示", "get_current_time", "message_id=7", "现在几点", "forced_tool_choice"} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview missing %q: %s", want, got)
		}
	}
}

func TestFormatContextPreviewFromMessagesShowsActualJSONContext(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "短 system"},
		{Role: "user", Content: "<context_json>\n{\"current_user_message\":\"你好\"}\n</context_json>"},
	}
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "search_memory", Description: "搜索记忆"}}}
	usage := estimateMessagesUsage(messages)

	got := formatContextPreviewFromMessages("test-model", messages, tools, nil, usage)
	for _, want := range []string{"## messages", "### 1. system", "短 system", "<context_json>", "search_memory"} {
		if !strings.Contains(got, want) {
			t.Fatalf("JSON preview missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "## recent_messages") {
		t.Fatalf("JSON preview should not use legacy recent_messages section: %s", got)
	}
}

func TestContextPreviewDraftForBuilderUsesPlaceholderForEmptyDraft(t *testing.T) {
	got := contextPreviewDraftForBuilder("  ")
	if got == "" {
		t.Fatal("expected placeholder for empty preview draft")
	}
	if !strings.Contains(got, "输入框为空") {
		t.Fatalf("unexpected placeholder: %q", got)
	}
	if got := contextPreviewDraftForBuilder("  你好  "); got != "你好" {
		t.Fatalf("non-empty draft = %q, want 你好", got)
	}
}

func TestBuildJSONContextMessagesUsesStructuredUserContext(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("MAX_RECENT_MESSAGES", "1")
	doc, err := memory.CreateKnowledgeDoc("私人 PDF", "uploads/docs/private.pdf", "这是一段 PDF 可读正文，包含六面骰规则和安全边界说明。", "ready", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveChunk(1, memory.ScopeGlobal, "knowledge_doc", sql.NullInt64{Int64: doc.ID, Valid: true}, "亲密正文不应默认出现", "私人亲密文档", "私人,爸爸"); err != nil {
		t.Fatal(err)
	}
	hist := []memory.Message{
		{ID: 1, Role: "user", Content: "旧消息"},
		{ID: 2, Role: "assistant", Content: "近消息"},
	}

	built, err := buildJSONContextMessages(1, "那份 私人 PDF 里写了什么？", hist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(built.Messages))
	}
	if strings.Contains(built.SystemPrompt, "亲密正文不应默认出现") {
		t.Fatalf("system prompt leaked memory content: %s", built.SystemPrompt)
	}
	userContext, ok := built.Messages[1].Content.(string)
	if !ok {
		t.Fatalf("expected string user context, got %#v", built.Messages[1].Content)
	}
	for _, want := range []string{"<context_json>", `"memory_index"`, `"current_user_message":"那份 私人 PDF 里写了什么？"`} {
		if !strings.Contains(userContext, want) {
			t.Fatalf("structured context missing %q: %s", want, userContext)
		}
	}
	if strings.Contains(userContext, "亲密正文不应默认出现") {
		t.Fatalf("structured context leaked high sensitivity content: %s", userContext)
	}
	if !strings.Contains(userContext, `"retrieved_memory"`) || !strings.Contains(userContext, `"retrieved_doc_chunks"`) {
		t.Fatalf("structured context missing retrieved evidence fields: %s", userContext)
	}
	if !strings.Contains(userContext, "提炼后的长期记忆线索") {
		t.Fatalf("retrieved memory should include distilled continuity cues: %s", userContext)
	}
	if !strings.Contains(userContext, "六面骰规则") || !strings.Contains(userContext, fmt.Sprintf(`"doc_id":%d`, doc.ID)) {
		t.Fatalf("retrieved doc chunk missing expected excerpt/doc id: %s", userContext)
	}
	if !built.Debug.MemoryRoute.NeedSearchMemory || !built.Debug.DocRoute.NeedReadUploadedDoc {
		t.Fatalf("expected memory and doc routes, got %#v", built.Debug)
	}
	if len(built.Debug.InjectedMemoryIDs) == 0 || len(built.Debug.InjectedDocChunks) == 0 {
		t.Fatalf("expected injected evidence ids in debug, got %#v", built.Debug)
	}
}

func TestBuildJSONContextMessagesRecallsHealthFactFromShortQuestion(t *testing.T) {
	setupAPITestDB(t)
	if err := memory.SaveChunkWithMetadata(
		1,
		memory.ScopeGlobal,
		"conversation",
		sql.NullInt64{},
		"用户纠正了校园场景中的第二人称视角。",
		"校园场景应使用第二人称视角。",
		"校园,视角纠正",
		memory.ChunkMetadata{IsCorrection: true, Importance: "high"},
	); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveChunk(
		1,
		memory.ScopeGlobal,
		"auto_global",
		sql.NullInt64{},
		"用户有双相情感障碍，需要持续关注治疗和用药。",
		"用户患有双相情感障碍。",
		"健康,疾病,诊断,双相情感障碍",
	); err != nil {
		t.Fatal(err)
	}

	built, err := buildJSONContextMessages(1, "我有什么病", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !built.Debug.MemoryRoute.NeedSearchMemory {
		t.Fatalf("short personal fact question did not trigger memory recall: %#v", built.Debug.MemoryRoute)
	}
	if len(built.Debug.InjectedMemoryIDs) == 0 {
		t.Fatalf("health memory was not injected: %#v", built.Debug)
	}
	if len(built.Debug.InjectedMemoryIDs) != 1 {
		t.Fatalf("unrelated correction memory was injected: %#v", built.Debug.InjectedMemoryIDs)
	}
	userContext, ok := built.Messages[1].Content.(string)
	if !ok || !strings.Contains(userContext, "双相情感障碍") {
		t.Fatalf("retrieved health fact missing from context: %#v", built.Messages[1].Content)
	}
}

func TestModelHistorySourceLimitAllowsLogicalCoalescing(t *testing.T) {
	t.Setenv("ENABLE_JSON_CONTEXT_BUILDER", "true")
	if got := modelHistorySourceLimit(); got != jsonContextRawHistoryLimit {
		t.Fatalf("JSON context raw history limit=%d, want %d", got, jsonContextRawHistoryLimit)
	}
	t.Setenv("ENABLE_JSON_CONTEXT_BUILDER", "false")
	if got := modelHistorySourceLimit(); got != maxHistory {
		t.Fatalf("legacy history limit=%d, want %d", got, maxHistory)
	}
}

func TestRetrieveDocsForGenericPDFQueryFallsBackToReadablePrivateDoc(t *testing.T) {
	t.Setenv("MAX_RETRIEVED_DOC_CHARS", "200")
	docs := []memory.KnowledgeDoc{
		{ID: 2, Title: "私人 PDF", SourcePath: "uploads/docs/private.pdf", ContentText: "爸爸 私人 PDF 正文，包含需要继续扮演的设定。", Status: "ready"},
		{ID: 1, Title: "Karpathy-CLAUDE", SourcePath: "uploads/docs/claude.md", ContentText: "AI 编程 agent guideline", Status: "ready"},
	}
	route := contextbuilder.DocRouteDecision{
		NeedReadUploadedDoc: true,
		Query:               "那份 PDF 里写了什么？",
	}
	items := retrieveDocsForJSONContext(route.Query, route, docs)
	if len(items) == 0 {
		t.Fatal("expected generic PDF query to retrieve a readable document")
	}
	if items[0].DocID != 2 || !strings.Contains(items[0].Content, "私人 PDF 正文") {
		t.Fatalf("unexpected fallback doc chunks: %#v", items)
	}
}

func TestDeleteConversationHandlerRemovesConversationAndReturnsNext(t *testing.T) {
	setupAPITestDB(t)

	first, err := memory.CreateConversation("要删除的对话")
	if err != nil {
		t.Fatal(err)
	}
	second, err := memory.CreateConversation("保留的对话")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(first.ID, "user", "旧消息"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations?id="+strconv.FormatInt(first.ID, 10), nil)
	rec := httptest.NewRecorder()
	handleConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK               bool                  `json:"ok"`
		Conversations    []memory.Conversation `json:"conversations"`
		NextConversation memory.Conversation   `json:"next_conversation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.NextConversation.ID != second.ID {
		t.Fatalf("unexpected delete response: %#v", body)
	}
	for _, c := range body.Conversations {
		if c.ID == first.ID {
			t.Fatalf("deleted conversation still listed: %#v", body.Conversations)
		}
	}
}

func TestDeleteConversationHandlerPrefersExistingEmptyConversation(t *testing.T) {
	setupAPITestDB(t)

	deleting, err := memory.CreateConversation("要删除的对话")
	if err != nil {
		t.Fatal(err)
	}
	nonEmpty, err := memory.CreateConversation("有内容的对话")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(nonEmpty.ID, "user", "已有内容"); err != nil {
		t.Fatal(err)
	}
	empty, err := memory.CreateConversation("空白对话")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations?id="+strconv.FormatInt(deleting.ID, 10), nil)
	rec := httptest.NewRecorder()
	handleConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		NextConversation memory.Conversation `json:"next_conversation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.NextConversation.ID != empty.ID {
		t.Fatalf("next conversation = %d, want empty conversation %d", body.NextConversation.ID, empty.ID)
	}
}

func TestDeletedConversationIDDoesNotRecreateOnRead(t *testing.T) {
	setupAPITestDB(t)

	c, err := memory.CreateConversation("会被删除")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.DeleteConversation(c.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/history?conversation_id="+strconv.FormatInt(c.ID, 10), nil)
	rec := httptest.NewRecorder()
	handleHistory(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	conversations, err := memory.ListConversations()
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range conversations {
		if got.ID == c.ID {
			t.Fatalf("deleted conversation was recreated: %#v", conversations)
		}
	}
}

func TestVisibleContextDebugRequiresFlag(t *testing.T) {
	debug := &contextbuilder.DebugInfo{RequestID: "abc"}
	t.Setenv("ENABLE_CONTEXT_DEBUG", "")
	if visibleContextDebug(debug) != nil {
		t.Fatal("expected debug hidden by default")
	}
	t.Setenv("ENABLE_CONTEXT_DEBUG", "true")
	if visibleContextDebug(debug) == nil {
		t.Fatal("expected debug visible with flag")
	}
}

func TestChatToolsLoadsConfigFile(t *testing.T) {
	toolsPath := filepath.Join(t.TempDir(), "tools.json")
	raw := `[{"type":"function","function":{"name":"custom_tool","description":"from config","parameters":{"type":"object","properties":{}}}}]`
	if err := os.WriteFile(toolsPath, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOOLS_CONFIG_PATH", toolsPath)

	tools := chatTools()
	if len(tools) != 1 || tools[0].Function.Name != "custom_tool" {
		t.Fatalf("expected configured tool, got %#v", tools)
	}
	if choice := chatToolChoice("现在几点了", tools); choice != nil {
		t.Fatalf("did not expect forced choice for missing configured tool, got %#v", choice)
	}
}

func TestExplicitMemorySearchRequested(t *testing.T) {
	if !explicitMemorySearchRequested("你还记得小林说过什么吗？") {
		t.Fatal("expected memory question to request memory search")
	}
	if !explicitMemorySearchRequested("长期记忆里小宝喜欢什么？") {
		t.Fatal("expected long-term memory fact question to request memory search")
	}
	for _, prompt := range []string{
		"昨天和你说的那件事是什么？",
		"上次那个你还知道吗？",
		"刚才我说的那个先别忘",
		"之前聊的那件事继续",
	} {
		if !explicitMemorySearchRequested(prompt) {
			t.Fatalf("expected natural memory reference to request memory search: %s", prompt)
		}
	}
	choice := chatToolChoice("之前我说过小林吗？", defaultChatTools())
	got, ok := choice.(map[string]interface{})
	if !ok {
		t.Fatalf("expected forced tool choice, got %#v", choice)
	}
	fn := got["function"].(map[string]interface{})
	if fn["name"] != "search_memory" {
		t.Fatalf("expected search_memory tool, got %#v", choice)
	}

	choice = chatToolChoice("长期记忆里小宝喜欢什么？", defaultChatTools())
	got, ok = choice.(map[string]interface{})
	if !ok {
		t.Fatalf("expected forced tool choice for long-term memory fact, got %#v", choice)
	}
	fn = got["function"].(map[string]interface{})
	if fn["name"] != "search_memory" {
		t.Fatalf("expected search_memory tool for long-term memory fact, got %#v", choice)
	}
}

func TestWorldBookQuestionLeavesToolChoiceToMemoryModel(t *testing.T) {
	for _, prompt := range []string{"世界书里有什么内容？", "读取设定集里的王都词条", "你还记得世界书中的角色设定吗？"} {
		if !explicitWorldBookRequested(prompt) {
			t.Fatalf("expected world book request: %q", prompt)
		}
		if choice := chatToolChoice(prompt, defaultChatTools()); choice != nil {
			t.Fatalf("expected B to choose a tool autonomously for %q, got %#v", prompt, choice)
		}
	}
}

func TestExplicitVirtualTransferForcesToolChoice(t *testing.T) {
	for _, prompt := range []string{"给我转账 52 元", "转账给我520", "给老婆打钱"} {
		if !explicitVirtualTransferRequested(prompt) {
			t.Fatalf("expected virtual transfer request: %q", prompt)
		}
		choice := chatToolChoice(prompt, defaultChatTools())
		got, ok := choice.(map[string]interface{})
		if !ok {
			t.Fatalf("expected forced tool choice for %q, got %#v", prompt, choice)
		}
		fn := got["function"].(map[string]interface{})
		if fn["name"] != "send_virtual_transfer" {
			t.Fatalf("expected send_virtual_transfer for %q, got %#v", prompt, choice)
		}
	}
}

func TestVirtualTransferDiagnosticsDoNotCreateAnotherTransfer(t *testing.T) {
	for _, prompt := range []string{"转账没有显示", "为什么转账没效果", "AI调用转账失败", "转账工具报错了"} {
		if explicitVirtualTransferRequested(prompt) {
			t.Fatalf("diagnostic prompt must not request a new transfer: %q", prompt)
		}
	}
}

func TestContextualVirtualTransferFollowup(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("转账测试")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(conversation.ID, "assistant", "续费的话，需要你支付给我。"); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(conversation.ID, "user", "要支付520000"); err != nil {
		t.Fatal(err)
	}
	if !contextualVirtualTransferFollowupRequested(conversation.ID, "要支付520000") {
		t.Fatal("expected payment follow-up to request virtual transfer")
	}
	if contextualVirtualTransferFollowupRequested(conversation.ID, "为什么转账没显示") {
		t.Fatal("diagnostic follow-up must not request another transfer")
	}
}

func TestContextualVirtualTransferAcceptance(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("转账承接测试")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(conversation.ID, "assistant", "我给你转 52 元，好不好？"); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{"好", "嗯，那就转吧", "可以"} {
		if !contextualVirtualTransferFollowupRequested(conversation.ID, reply) {
			t.Fatalf("expected acceptance %q to force virtual transfer", reply)
		}
	}
}

func TestExplicitShellRequestedForDeletingUploadedResources(t *testing.T) {
	for _, message := range []string{"删除上传文档", "我想删文档", "删除长期记忆 #228", "删表情包", "你去看一下 VPS 吧", "检查服务器状态", "看看线上服务"} {
		if !explicitShellRequested(message) {
			t.Fatalf("expected %q to request linux_shell", message)
		}
	}
	choice := chatToolChoice("我想删除上传文档", defaultChatTools())
	got, ok := choice.(map[string]interface{})
	if !ok {
		t.Fatalf("expected forced linux_shell choice, got %#v", choice)
	}
	fn := got["function"].(map[string]interface{})
	if fn["name"] != "linux_shell" {
		t.Fatalf("expected linux_shell tool, got %#v", choice)
	}
}

func TestExplicitChatFileRequestedForNaturalPhrases(t *testing.T) {
	for _, message := range []string{
		"给我发个 md 文件",
		"用 Markdown 格式发我",
		"把这份内容导出成 .md",
		"发一个 HTML 文件",
		"现在可以发md了，你看一下你那里可以发吗",
		"你现在可以发md文件吗尝试发一个",
		"能不能发一个 Markdown 附件",
		"乱码。给我个md的",
		"给我一个 Markdown 文件",
		"整理成 md 给我",
		"弄个 html 文件",
		"来一个 .md",
	} {
		if !explicitChatFileRequested(message) {
			t.Errorf("expected chat file request for %q", message)
		}
		if got := chatResponseMaxTokens(message); got != 8192 {
			t.Errorf("max tokens for %q = %d, want 8192", message, got)
		}
	}

	for _, message := range []string{"AI 发不了 md 文件", "解释 Markdown 是什么", "普通聊天"} {
		if explicitChatFileRequested(message) {
			t.Errorf("unexpected chat file request for %q", message)
		}
	}
	if got := chatResponseMaxTokens("普通聊天"); got != 6144 {
		t.Fatalf("ordinary chat max tokens = %d, want 6144", got)
	}
	choice := chatToolChoice("乱码。给我个md的", defaultChatTools())
	got, ok := choice.(map[string]interface{})
	if !ok {
		t.Fatalf("expected forced create_chat_file choice, got %#v", choice)
	}
	fn := got["function"].(map[string]interface{})
	if fn["name"] != "create_chat_file" {
		t.Fatalf("expected create_chat_file tool, got %#v", choice)
	}
}

func TestContextualChatFileFollowupRequested(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("文件测试")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(conversation.ID, "assistant", `{"reply":"我写好 Markdown 文件发你。"}`); err != nil {
		t.Fatal(err)
	}
	if !contextualChatFileFollowupRequested(conversation.ID, "没收到") {
		t.Fatal("expected missing-file follow-up to require a file tool")
	}
	if contextualChatFileFollowupRequested(conversation.ID, "普通聊天") {
		t.Fatal("ordinary chat must not require a file tool")
	}
}

func TestSuspiciousAssistantDraft(t *testing.T) {
	for _, reply := range []string{
		"...",
		"...\n\nNow I have the complete picture. Here's the summary of all issues:\nLet me compile the full bug list:",
		"I need to analyze the request before answering.",
	} {
		if !suspiciousAssistantDraft(reply) {
			t.Errorf("expected suspicious draft: %q", reply)
		}
	}
	for _, reply := range []string{"我检查完了，有两个问题。", "Now I have time to answer you properly."} {
		if suspiciousAssistantDraft(reply) {
			t.Errorf("unexpected suspicious draft: %q", reply)
		}
	}
}

func TestChatToolChoiceAllowsMultiToolQuestions(t *testing.T) {
	choice := chatToolChoice("当前长期记忆哪些，你可以看到哪些表情包？", defaultChatTools())
	if choice != nil {
		t.Fatalf("expected multi-tool question to leave tool choice automatic, got %#v", choice)
	}
	names := explicitToolNames("当前长期记忆哪些，你可以看到哪些表情包？")
	if strings.Join(names, ",") != "list_stickers,search_memory" {
		t.Fatalf("unexpected explicit tools: %#v", names)
	}
}

func TestToolChoiceForThinkingModel(t *testing.T) {
	choice := forceToolChoiceIfConfigured("search_memory", defaultChatTools())
	if choice == nil {
		t.Fatal("expected configured forced tool choice")
	}
	if got := toolChoiceForModel("[按量]claude-opus-4-6-thinking", choice); got != nil {
		t.Fatalf("thinking model must use automatic tool choice, got %#v", got)
	}
	if got := toolChoiceForModel("奈米按量-opus-4.6", choice); got == nil {
		t.Fatal("ordinary model should keep forced tool choice")
	}
}

func TestRunSearchMemoryToolFindsUploadedDocs(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO knowledge_docs(id,title,status) VALUES(9,'测试文档','ready')`); err != nil {
		t.Fatal(err)
	}

	if err := memory.SaveChunk(1, memory.ScopeGlobal, "knowledge_doc", sql.NullInt64{Int64: 9, Valid: true}, "小林说生日想去海边看日落。", "小林想去海边看日落", "小林,生日,海边,日落"); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveChunk(1, memory.ScopeConversation, "conversation", sql.NullInt64{}, "小林这条普通会话不应出现在 uploaded_docs 范围。", "普通会话", "小林,普通会话"); err != nil {
		t.Fatal(err)
	}

	out := runSearchMemoryTool(`{"query":"小林 海边","scope":"uploaded_docs","limit":5}`, 1, "")
	for _, want := range []string{"no_evidence:false", "memory_1_source_type:knowledge_doc", "memory_1_source_doc_id:9", "小林想去海边看日落"} {
		if !strings.Contains(out, want) {
			t.Fatalf("memory search output missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "普通会话") {
		t.Fatalf("uploaded_docs scope leaked conversation memory: %s", out)
	}
}

func TestRunSearchMemoryToolCanReadCurrentConversationMessages(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO conversations(id,title) VALUES(2,'其他会话')`); err != nil {
		t.Fatal(err)
	}

	longTopic := "这是一段很长的本次话题，里面包含唯一关键词：银色风铃计划。" + strings.Repeat("要保留原始细节。", 300)
	if _, err := memory.SaveMessage(1, "user", longTopic); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(2, "user", "其他会话的银色风铃计划不应出现。"); err != nil {
		t.Fatal(err)
	}

	out := runSearchMemoryTool(`{"query":"银色风铃计划","scope":"messages","limit":3}`, 1, "")
	for _, want := range []string{"message_search:true", "message_1_role:user", "银色风铃计划", "要保留原始细节"} {
		if !strings.Contains(out, want) {
			t.Fatalf("message search output missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "其他会话的银色风铃计划") {
		t.Fatalf("message search leaked another conversation: %s", out)
	}
}

func TestRunSearchMemoryToolRecentMessageFallbackForVagueReference(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.SaveMessage(1, "user", "第一条普通消息"); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(1, "user", "刚才真正要继续的话题：蓝色日历提醒。"); err != nil {
		t.Fatal(err)
	}

	out := runSearchMemoryTool(`{"query":"刚才那个","scope":"messages","limit":1}`, 1, "")
	for _, want := range []string{"message_search:true", "count:1", "蓝色日历提醒"} {
		if !strings.Contains(out, want) {
			t.Fatalf("recent message fallback missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "第一条普通消息") {
		t.Fatalf("recent message fallback returned too many messages: %s", out)
	}
}

func TestRunSearchMemoryToolListsVisibleLongTermMemory(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO conversations(id,title) VALUES(2,'其他会话')`); err != nil {
		t.Fatal(err)
	}

	if err := memory.SaveChunk(1, memory.ScopeGlobal, "manual", sql.NullInt64{}, "小宝喜欢黑色页面和清脆铃声。", "小宝偏好黑色页面", "小宝,偏好"); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveChunk(1, memory.ScopeConversation, "manual", sql.NullInt64{}, "当前会话里要记住：不要重复问同一件事。", "不要重复问同一件事", "重复,偏好"); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveChunk(2, memory.ScopeConversation, "manual", sql.NullInt64{}, "其他会话记忆不应出现。", "其他会话", "隔离"); err != nil {
		t.Fatal(err)
	}

	out := runSearchMemoryTool(`{"query":"当前长期记忆有哪些","limit":10}`, 1, "")
	for _, want := range []string{"memory_list:true", "count:2", "小宝偏好黑色页面", "不要重复问同一件事"} {
		if !strings.Contains(out, want) {
			t.Fatalf("memory list output missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "其他会话记忆不应出现") {
		t.Fatalf("memory list leaked another conversation: %s", out)
	}
}

func TestRunSearchMemoryToolReadsMemoryByID(t *testing.T) {
	setupAPITestDB(t)

	if err := memory.SaveChunk(1, memory.ScopeGlobal, "manual", sql.NullInt64{}, "完整正文：小宝写给爸爸的信要保留温柔但直接的语气。", "给爸爸的信", "爸爸,信"); err != nil {
		t.Fatal(err)
	}
	chunks, err := memory.ListChunksByScope(1, memory.ScopeGlobal, 1)
	if err != nil {
		t.Fatal(err)
	}
	out := runSearchMemoryTool(fmt.Sprintf(`{"memory_id":%d}`, chunks[0].ID), 1, "")
	for _, want := range []string{"memory_detail:true", "给爸爸的信", "完整正文：小宝写给爸爸的信"} {
		if !strings.Contains(out, want) {
			t.Fatalf("memory detail output missing %q: %s", want, out)
		}
	}
}

func TestRunSearchMemoryToolBlocksOtherConversationMemoryByID(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO conversations(id,title) VALUES(2,'其他会话')`); err != nil {
		t.Fatal(err)
	}

	if err := memory.SaveChunk(2, memory.ScopeConversation, "manual", sql.NullInt64{}, "会话2私有记忆", "会话2摘要", "私有"); err != nil {
		t.Fatal(err)
	}
	chunks, err := memory.ListChunksByScope(2, memory.ScopeConversation, 1)
	if err != nil {
		t.Fatal(err)
	}
	out := runSearchMemoryTool(fmt.Sprintf(`{"memory_id":%d}`, chunks[0].ID), 1, "")
	for _, want := range []string{"no_evidence:true", "不属于当前会话"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected blocked memory detail missing %q: %s", want, out)
		}
	}
}

func TestUploadedDocsListIncludesDocsWithoutChunks(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.CreateKnowledgeDoc("CLAUDE.md", "uploads/docs/a.md", "规则正文", "ready", ""); err != nil {
		t.Fatal(err)
	}
	out := runSearchMemoryTool(`{"query":"我上传了哪些文档","scope":"uploaded_docs","limit":10}`, 1, "")
	for _, want := range []string{"no_evidence:false", "doc_count:1", "doc_1_title:CLAUDE.md", "doc_1_index_chunks:0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("uploaded docs list missing %q: %s", want, out)
		}
	}
}

func TestEnsureKnowledgeDocIndexesBackfillsReadyDocs(t *testing.T) {
	setupAPITestDB(t)

	doc, err := memory.CreateKnowledgeDoc("CLAUDE.md", "uploads/docs/a.md", "规则正文", "ready", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureKnowledgeDocIndexes(); err != nil {
		t.Fatal(err)
	}
	count, err := memory.CountChunksBySourceDoc("knowledge_doc", doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("chunk count = %d, want 1", count)
	}
	chunks, err := memory.SearchChunksBySourceTypes(1, "CLAUDE", []string{"knowledge_doc"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || !strings.Contains(chunks[0].Content, "document_title:CLAUDE.md") {
		t.Fatalf("expected searchable document title chunk, got %#v", chunks)
	}
	if err := EnsureKnowledgeDocIndexes(); err != nil {
		t.Fatal(err)
	}
	count, err = memory.CountChunksBySourceDoc("knowledge_doc", doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("repair should be idempotent, chunk count = %d", count)
	}
}

func TestRunReadUploadedDocToolReadsIndexedPDFText(t *testing.T) {
	setupAPITestDB(t)

	doc, err := memory.CreateKnowledgeDoc("game-rules.pdf", "uploads/docs/game-rules.pdf", "第一章 开场\n玩家需要选择角色。\n第二章 规则\n战斗使用六面骰。", "ready", "")
	if err != nil {
		t.Fatal(err)
	}
	out := runReadUploadedDocTool(`{"doc_id":` + strconv.FormatInt(doc.ID, 10) + `,"query":"六面骰","max_chars":80}`)
	for _, want := range []string{"no_evidence:false", "doc_title:game-rules.pdf", "content_excerpt:", "六面骰"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read doc output missing %q: %s", want, out)
		}
	}
}

func TestRunReadUploadedDocToolRequiresSelectionForMultipleDocs(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.CreateKnowledgeDoc("a.pdf", "uploads/docs/a.pdf", "A", "ready", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.CreateKnowledgeDoc("b.pdf", "uploads/docs/b.pdf", "B", "ready", ""); err != nil {
		t.Fatal(err)
	}
	out := runReadUploadedDocTool(`{"query":"规则"}`)
	if !strings.Contains(out, `"error_type":"parameter_error"`) {
		t.Fatalf("expected parameter error without doc selection, got: %s", out)
	}
}

func TestRunReadUploadedDocToolValidatesIDAndMatchesDocTitle(t *testing.T) {
	setupAPITestDB(t)
	if _, err := memory.CreateKnowledgeDoc("旅行计划 2026.pdf", "uploads/docs/trip.pdf", "第一站是杭州。", "ready", ""); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"doc_id":"1"}`, `{"doc_id":1.5}`, `{"doc_id":999}`} {
		out := runReadUploadedDocTool(input)
		if !strings.Contains(out, `"error_type":"parameter_error"`) {
			t.Fatalf("input %s should fail validation: %s", input, out)
		}
	}
	out := runReadUploadedDocTool(`{"doc_title":"旅行计划","query":"杭州"}`)
	if !strings.Contains(out, "doc_title:旅行计划 2026.pdf") || !strings.Contains(out, "杭州") {
		t.Fatalf("doc_title fuzzy match failed: %s", out)
	}
}

func TestRunSearchMemoryToolNoEvidence(t *testing.T) {
	setupAPITestDB(t)

	out := runSearchMemoryTool(`{"query":"不存在的人"}`, 1, "")
	if !strings.Contains(out, "no_evidence:true") {
		t.Fatalf("expected no evidence marker, got: %s", out)
	}
}

func TestRunSearchMemoryToolUsesFallbackQuery(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO knowledge_docs(id,title,status) VALUES(10,'回退文档','ready')`); err != nil {
		t.Fatal(err)
	}

	if err := memory.SaveChunk(1, memory.ScopeGlobal, "knowledge_doc", sql.NullInt64{Int64: 10, Valid: true}, "阿月喜欢雨天散步。", "阿月喜欢雨天散步", "阿月,雨天,散步"); err != nil {
		t.Fatal(err)
	}
	out := runSearchMemoryTool(`{"query":""}`, 1, "你还记得阿月吗")
	if !strings.Contains(out, "阿月喜欢雨天散步") {
		t.Fatalf("expected fallback query to find memory, got: %s", out)
	}
}

func TestRunListStickersTool(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.CreateSticker("抱抱", "uploads/stickers/hug.png", "/uploads/stickers/hug.png", "image/png", "安慰,抱抱", "适合安慰", "comfort", false); err != nil {
		t.Fatal(err)
	}
	disabled, err := memory.CreateSticker("禁用", "uploads/stickers/off.png", "/uploads/stickers/off.png", "image/png", "禁用", "不应出现", "neutral", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.DisableSticker(disabled.ID); err != nil {
		t.Fatal(err)
	}

	out := runListStickersTool(`{"limit":10}`)
	for _, want := range []string{"count:1", "sticker_1_name:抱抱", "sticker_1_tags:安慰,抱抱", "sticker_1_mood:comfort"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list stickers output missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "禁用") {
		t.Fatalf("disabled sticker leaked into list: %s", out)
	}
}

func TestStickerVisionMessagesFromToolSummary(t *testing.T) {
	setupAPITestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "hug.png")
	if err := os.WriteFile(path, []byte("fake-png"), 0600); err != nil {
		t.Fatal(err)
	}
	sticker, err := memory.CreateSticker("抱抱", path, "/uploads/stickers/hug.png", "image/png", "安慰,抱抱", "两个人拥抱", "comfort", false)
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("id:%d\nname:抱抱\nmood:comfort", sticker.ID)
	summary := marshalRawToolResults([]channelAToolResult{rawToolResultForA(ToolCall{Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "send_sticker"}}, raw, 1)})

	messages := stickerVisionMessagesFromToolSummary(summary)
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("unexpected vision messages: %#v", messages)
	}
	parts, ok := messages[0].Content.([]ContentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil {
		t.Fatalf("sticker image was not injected: %#v", messages[0].Content)
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("unexpected sticker data URL: %q", parts[1].ImageURL.URL)
	}
}

func TestFindStickerForToolQueryFallbacks(t *testing.T) {
	setupAPITestDB(t)
	first, err := memory.CreateSticker("抱抱", "/missing/hug.png", "/uploads/hug.png", "image/png", "安慰,抱抱", "拥抱", "comfort", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.CreateSticker("鼓掌", "/missing/clap.png", "/uploads/clap.png", "image/png", "庆祝", "鼓励", "joy", false); err != nil {
		t.Fatal(err)
	}
	if got, err := findStickerForToolQuery("抱抱", ""); err != nil || len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("exact match: %#v, %v", got, err)
	}
	if got, err := findStickerForToolQuery("庆", ""); err != nil || len(got) != 1 {
		t.Fatalf("fuzzy match: %#v, %v", got, err)
	}
	if got, err := findStickerForToolQuery("完全不存在", ""); err != nil || len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("fallback: %#v, %v", got, err)
	}
	if err := memory.DisableSticker(first.ID); err != nil {
		t.Fatal(err)
	}
	second, _ := memory.GetSticker(first.ID + 1)
	if err := memory.DisableSticker(second.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := findStickerForToolQuery("完全不存在", ""); err != nil || len(got) != 0 {
		t.Fatalf("empty: %#v, %v", got, err)
	}
}

func TestStickerVisionMessagesMissingFileVisible(t *testing.T) {
	setupAPITestDB(t)
	sticker, err := memory.CreateSticker("缺失", "/tmp/no-such-sticker.png", "/uploads/no-such-sticker.png", "image/png", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	messages := stickerVisionMessagesByID(sticker.ID, "测试")
	if len(messages) != 1 || !strings.Contains(fmt.Sprint(messages[0].Content), "sticker id") || !strings.Contains(fmt.Sprint(messages[0].Content), sticker.FilePath) {
		t.Fatalf("missing-file error not visible: %#v", messages)
	}
}

func TestStickerVisionMessagesIgnoreOtherTools(t *testing.T) {
	summary := marshalRawToolResults([]channelAToolResult{{ToolName: "list_stickers", Result: json.RawMessage(`"count:1"`)}})
	if messages := stickerVisionMessagesFromToolSummary(summary); len(messages) != 0 {
		t.Fatalf("list_stickers unexpectedly injected images: %#v", messages)
	}
}

func TestRecentUserStickerVisionMessages(t *testing.T) {
	setupAPITestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "sent.png")
	if err := os.WriteFile(path, []byte("fake-png"), 0600); err != nil {
		t.Fatal(err)
	}
	sticker, err := memory.CreateSticker("收到", path, "/uploads/stickers/sent.png", "image/png", "收到", "点头", "neutral", false)
	if err != nil {
		t.Fatal(err)
	}
	history := []memory.Message{
		{Role: "user", Content: "[[sticker:999:standalone]]"},
		{Role: "assistant", Content: "较早的回复"},
		{Role: "user", Content: fmt.Sprintf("[[sticker:%d:standalone]]", sticker.ID)},
	}
	messages := recentUserStickerVisionMessages(history)
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("unexpected vision messages: %#v", messages)
	}
	parts, ok := messages[0].Content.([]ContentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("recent user sticker image was not injected: %#v", messages[0].Content)
	}
	if !strings.Contains(parts[0].Text, "用户在当前回复前直接发送") {
		t.Fatalf("unexpected sticker context: %q", parts[0].Text)
	}
}

func TestRecentUserStickerVisionMessagesStopsAtAssistantAndSkipsDisabled(t *testing.T) {
	setupAPITestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "old.png")
	if err := os.WriteFile(path, []byte("fake-png"), 0600); err != nil {
		t.Fatal(err)
	}
	sticker, err := memory.CreateSticker("旧图", path, "/uploads/stickers/old.png", "image/png", "旧图", "旧图", "neutral", false)
	if err != nil {
		t.Fatal(err)
	}
	history := []memory.Message{{Role: "user", Content: fmt.Sprintf("[[sticker:%d:standalone]]", sticker.ID)}, {Role: "assistant", Content: "已回复"}}
	if messages := recentUserStickerVisionMessages(history); len(messages) != 0 {
		t.Fatalf("answered sticker was reinjected: %#v", messages)
	}
	if err := memory.DisableSticker(sticker.ID); err != nil {
		t.Fatal(err)
	}
	history = []memory.Message{{Role: "user", Content: fmt.Sprintf("[[sticker:%d:standalone]]", sticker.ID)}}
	if messages := recentUserStickerVisionMessages(history); len(messages) != 0 {
		t.Fatalf("disabled sticker was injected: %#v", messages)
	}
}

func TestManualMemoryDefaultsToGlobalScope(t *testing.T) {
	setupAPITestDB(t)

	req := httptest.NewRequest("POST", "/api/memory/manual", strings.NewReader(`{"conversation_id":1,"text":"跨会话记忆：用户喜欢绿茶。","api_key":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleMemoryManual(rec, req)
	if rec.Code != 200 {
		t.Fatalf("manual memory status = %d, body=%s", rec.Code, rec.Body.String())
	}

	chunks, err := memory.SearchChunks(2, "绿茶", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected manual memory to be searchable from another conversation")
	}
	if chunks[0].Scope != memory.ScopeGlobal {
		t.Fatalf("manual memory scope = %q, want %q", chunks[0].Scope, memory.ScopeGlobal)
	}
}

func TestMemoryChunksHandlerFiltersByScope(t *testing.T) {
	setupAPITestDB(t)

	if _, err := memory.EnsureConversation(2); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		conversationID int64
		scope          string
		content        string
	}{
		{1, memory.ScopeGlobal, "全局记忆"},
		{1, memory.ScopeConversation, "会话1记忆"},
		{2, memory.ScopeConversation, "会话2记忆"},
	} {
		if err := memory.SaveChunk(item.conversationID, item.scope, "manual", sql.NullInt64{}, item.content, item.content, ""); err != nil {
			t.Fatal(err)
		}
	}

	getChunks := func(path string) []memoryChunkResp {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		handleMemoryChunks(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Chunks []memoryChunkResp `json:"chunks"`
			Scope  string            `json:"scope"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Chunks
	}

	global := getChunks("/api/memory/chunks?conversation_id=1&limit=10")
	if len(global) != 1 || global[0].Content != "全局记忆" || global[0].Scope != memory.ScopeGlobal {
		t.Fatalf("expected default global list, got %#v", global)
	}

	current := getChunks("/api/memory/chunks?conversation_id=1&scope=conversation&limit=10")
	if len(current) != 1 || current[0].Content != "会话1记忆" || current[0].Scope != memory.ScopeConversation {
		t.Fatalf("expected current conversation list, got %#v", current)
	}
}

func TestRunLinuxShellToolReadsFile(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "note.txt"), []byte("hello from shell\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENABLE_AGENT_SHELL", "true")

	out := runLinuxShellTool(`{"command":"cat note.txt","cwd":"` + filepath.ToSlash(tmp) + `"}`)
	for _, want := range []string{"tool:linux_shell", "exit_code:0", "stdout:", "hello from shell"} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell output missing %q: %s", want, out)
		}
	}
}

func TestRunLinuxShellToolBlocksDestructiveCommand(t *testing.T) {
	t.Setenv("ENABLE_AGENT_SHELL", "true")

	out := runLinuxShellTool(`{"command":"rm -rf /tmp/myapp-nope"}`)
	if !strings.Contains(out, "blocked:true") {
		t.Fatalf("expected destructive command to be blocked, got: %s", out)
	}
}

func TestRunLinuxShellToolAllowsCurlReadCommand(t *testing.T) {
	t.Setenv("ENABLE_AGENT_SHELL", "true")

	if reason, blocked := blockedShellCommand("curl -L https://example.com", "/opt/myapp"); blocked {
		t.Fatalf("expected curl read command to be allowed, blocked: %s", reason)
	}
}

func TestRunLinuxShellToolAllowsWritesOnlyWithinRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ENABLE_AGENT_SHELL", "true")
	t.Setenv("AGENT_SHELL_WRITE_ROOT", tmp)

	if reason, blocked := blockedShellCommand("printf hi > note.txt", tmp); blocked {
		t.Fatalf("expected write inside root to be allowed, blocked: %s", reason)
	}
	if reason, blocked := blockedShellCommand("printf hi > /tmp/outside-note.txt", tmp); !blocked {
		t.Fatalf("expected write outside root to be blocked, got allowed")
	} else if !strings.Contains(reason, "写目标超出允许目录") {
		t.Fatalf("unexpected block reason: %s", reason)
	}
	if reason, blocked := blockedShellCommand("rm -rf /tmp/outside-note.txt", tmp); !blocked {
		t.Fatalf("expected rm outside root to be blocked, got allowed")
	} else if !strings.Contains(reason, "写目标超出允许目录") {
		t.Fatalf("unexpected rm block reason: %s", reason)
	}
}

func TestLinuxShellSQLiteMutationsCountAsWrites(t *testing.T) {
	for _, command := range []string{
		`sqlite3 /opt/myapp/memories.db 'DELETE FROM knowledge_docs WHERE id=3'`,
		`sqlite3 /opt/myapp/memories.db 'UPDATE knowledge_docs SET status="deleted" WHERE id=3'`,
		`sqlite3 /opt/myapp/memories.db 'INSERT INTO knowledge_docs(title) VALUES("x")'`,
	} {
		if !shellWriteIntent(command) {
			t.Fatalf("expected sqlite mutation to count as write: %s", command)
		}
	}
	if shellWriteIntent(`sqlite3 /opt/myapp/memories.db 'SELECT id,title FROM knowledge_docs'`) {
		t.Fatal("expected sqlite select to stay read-only")
	}
}

func TestLinuxShellCallReadWriteClassification(t *testing.T) {
	writeCall := ToolCall{Type: "function"}
	writeCall.Function.Name = "linux_shell"
	writeCall.Function.Arguments = `{"command":"sqlite3 /opt/myapp/memories.db 'DELETE FROM knowledge_docs WHERE id=3'"}`
	if !isWriteLinuxShellCall(writeCall) {
		t.Fatal("expected sqlite delete call to be classified as write")
	}
	if isReadOnlyLinuxShellCall(writeCall) {
		t.Fatal("expected sqlite delete call not to be read-only")
	}

	readCall := ToolCall{Type: "function"}
	readCall.Function.Name = "linux_shell"
	readCall.Function.Arguments = `{"command":"sqlite3 /opt/myapp/memories.db 'SELECT id,title FROM knowledge_docs'"}`
	if isWriteLinuxShellCall(readCall) {
		t.Fatal("expected sqlite select call not to be classified as write")
	}
	if !isReadOnlyLinuxShellCall(readCall) {
		t.Fatal("expected sqlite select call to be read-only")
	}
}

func TestToolLoopUsesTwelveRoundsByDefault(t *testing.T) {
	t.Setenv("AGENT_TOOL_LOOP_MAX", "")
	if !toolLoopAllowed(0) || !toolLoopAllowed(11) || toolLoopAllowed(12) {
		t.Fatal("expected exactly twelve tool rounds by default")
	}
}

func TestToolLoopLimitCanBeConfigured(t *testing.T) {
	t.Setenv("AGENT_TOOL_LOOP_MAX", "5")
	if !toolLoopAllowed(4) || toolLoopAllowed(5) {
		t.Fatal("expected configured five-round tool limit")
	}

	t.Setenv("AGENT_TOOL_LOOP_MAX", "0")
	if !toolLoopAllowed(1000) {
		t.Fatal("expected zero to disable the tool-round limit")
	}
}

func TestToolFollowupRetriesProviderRateLimit(t *testing.T) {
	attempts := 0
	var waits []time.Duration
	result, err := callToolFollowupWithRetry(func() (ChatResult, error) {
		attempts++
		if attempts < 3 {
			return ChatResult{}, &providerRateLimitError{message: "HTTP 429"}
		}
		return ChatResult{Content: "完成"}, nil
	}, func(delay time.Duration) { waits = append(waits, delay) })
	if err != nil || result.Content != "完成" || attempts != 3 {
		t.Fatalf("result=%#v attempts=%d err=%v", result, attempts, err)
	}
	if len(waits) != 2 || waits[0] != providerCooldown || waits[1] != providerCooldown {
		t.Fatalf("unexpected retry waits: %#v", waits)
	}
}

func TestMainModelDoesNotRetry(t *testing.T) {
	attempts := 0
	_, err := callMainModelOnce(func() (ChatResult, error) {
		attempts++
		return ChatResult{}, errors.New("provider rejected request")
	})
	if err == nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v, want one failed attempt", attempts, err)
	}
}

func TestReplyModelToolCallExecutesDirectly(t *testing.T) {
	setupAPITestDB(t)
	originalExecute := executeReplyModelTool
	originalClient := httpClient
	t.Cleanup(func() {
		executeReplyModelTool = originalExecute
		httpClient = originalClient
	})

	executed := false
	executeReplyModelTool = func(call ToolCall, conversationID int64, fallbackQuery string, generatedAttachmentIDs, generatedTransferIDs *[]int64) string {
		executed = true
		if call.Function.Name != "get_current_time" || conversationID != 7 || fallbackQuery != "现在几点" {
			t.Fatalf("unexpected direct tool execution: call=%#v conversation=%d query=%q", call, conversationID, fallbackQuery)
		}
		return `{"time":"18:30:00"}`
	}
	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload chatRequest
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) == 0 || payload.Messages[len(payload.Messages)-1].Role != "tool" {
			t.Fatalf("tool result was not returned to A: %#v", payload.Messages)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"现在是 18:30。"}}]}`))}, nil
	})}

	call := ToolCall{ID: "call-a", Type: "function"}
	call.Function.Name = "get_current_time"
	call.Function.Arguments = `{}`
	reply, _, _, _, err := resolveToolCalls("key", "https://api.openai.com/v1", "model-a", 7, []ChatMessage{{Role: "user", Content: "现在几点"}}, ChatResult{ToolCalls: []ToolCall{call}}, defaultChatTools(), nil, nil)
	if err != nil || !executed || reply != "现在是 18:30。" {
		t.Fatalf("executed=%v reply=%q err=%v", executed, reply, err)
	}
}

func TestDelegatedToolArgumentsMustRemainEquivalent(t *testing.T) {
	if !sameToolArguments(`{"query":"coffee","limit":5}`, `{"limit":5,"query":"coffee"}`) {
		t.Fatal("equivalent JSON arguments should be accepted")
	}
	if sameToolArguments(`{"query":"coffee","limit":5}`, `{"query":"tea","limit":5}`) {
		t.Fatal("rewritten arguments should be rejected")
	}
}

func TestBoundedToolResultKeepsBeginningAndEnd(t *testing.T) {
	raw := strings.Repeat("a", 5000) + strings.Repeat("z", 2000)
	result := boundedToolResult(raw)
	if utf8.RuneCountInString(result) > 6050 || !strings.HasPrefix(result, strings.Repeat("a", 100)) || !strings.HasSuffix(result, strings.Repeat("z", 100)) {
		t.Fatalf("unexpected bounded result length=%d", utf8.RuneCountInString(result))
	}
}

func TestToolFollowupDoesNotRetryOtherErrors(t *testing.T) {
	attempts := 0
	_, err := callToolFollowupWithRetry(func() (ChatResult, error) {
		attempts++
		return ChatResult{}, errors.New("provider unavailable")
	}, func(time.Duration) { t.Fatal("unexpected sleep") })
	if err == nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestToolFollowupRetriesTemporaryProviderFailure(t *testing.T) {
	attempts := 0
	result, err := callToolFollowupWithRetry(func() (ChatResult, error) {
		attempts++
		if attempts == 1 {
			return ChatResult{}, errors.New("API错误: Service temporarily unavailable. Please try again later")
		}
		return ChatResult{Content: "完成"}, nil
	}, func(time.Duration) {})
	if err != nil || result.Content != "完成" || attempts != 2 {
		t.Fatalf("result=%#v attempts=%d err=%v", result, attempts, err)
	}
}

func TestPersonaActivateEndpointCanDeactivate(t *testing.T) {
	setupAPITestDB(t)

	p, err := memory.CreatePersona("测试人设", "用于停用", "", true)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/personas/activate", strings.NewReader(`{"id":`+strconv.FormatInt(p.ID, 10)+`,"active":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handlePersonaActivate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, err := memory.GetActivePersona(); err != nil || ok {
		t.Fatalf("expected no active persona after API deactivate, ok=%v err=%v", ok, err)
	}
}

func TestInferStickerPlacement(t *testing.T) {
	if got := inferStickerPlacement("", "\n抱抱你"); got != "standalone" {
		t.Fatalf("empty prefix placement = %q", got)
	}
	if got := inferStickerPlacement("给你一个", "继续说"); got != "inline" {
		t.Fatalf("inline placement = %q", got)
	}
}

func TestParseRoleReplyStructuredAndLegacy(t *testing.T) {
	structured, ok := parseRoleReply("```json\n{\"thought\":\"有点担心她\",\"action\":\"轻轻握住她的手\",\"reply\":\"我在这里。\"}\n```")
	if !ok {
		t.Fatal("expected structured role reply")
	}
	if structured.Thought != "有点担心她" || structured.Action != "轻轻握住她的手" || structured.Reply != "我在这里。" {
		t.Fatalf("unexpected structured role reply: %#v", structured)
	}

	legacy, ok := parseRoleReply("普通旧消息")
	if ok {
		t.Fatal("legacy message must not be marked structured")
	}
	if legacy.Reply != "普通旧消息" || legacy.Thought != "" || legacy.Action != "" {
		t.Fatalf("unexpected legacy fallback: %#v", legacy)
	}
}

func TestParseRoleReplyPreservesSemanticMessages(t *testing.T) {
	parsed, ok := parseRoleReply(`{"thought":"想安慰她","action":"坐近一点","reply":"先别急。我们慢慢来。","messages":["先别急。","我们慢慢来。"]}`)
	if !ok {
		t.Fatal("expected structured role reply")
	}
	if strings.Join(parsed.Messages, "|") != "先别急。|我们慢慢来。" {
		t.Fatalf("messages = %#v", parsed.Messages)
	}
}

func TestParseRoleReplyUnwrapsNestedStructuredReply(t *testing.T) {
	inner := `{"reply":"第一段。\n第二段。","messages":["第一段。","第二段。"]}`
	raw, err := json.Marshal(roleReply{Thought: "保留外层想法", Action: "保留外层动作", Reply: inner})
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := parseRoleReply(string(raw))
	if !ok {
		t.Fatal("expected nested role reply to be recovered")
	}
	if parsed.Thought != "保留外层想法" || parsed.Action != "保留外层动作" {
		t.Fatalf("outer role metadata was lost: %#v", parsed)
	}
	if parsed.Reply != "第一段。\n第二段。" || strings.Join(parsed.Messages, "|") != "第一段。|第二段。" {
		t.Fatalf("unexpected nested role reply: %#v", parsed)
	}
}

func TestParseRoleReplyRecoversReplyWithAppendedMessagesFragment(t *testing.T) {
	broken := `"第一段。\n第二段。","messages":["第一段。","第二段。"]`
	raw, err := json.Marshal(roleReply{Thought: "保留想法", Action: "保留动作", Reply: broken})
	if err != nil {
		t.Fatal(err)
	}
	parsed, ok := parseRoleReply(string(raw))
	if !ok {
		t.Fatal("expected appended messages fragment to be recovered")
	}
	if parsed.Reply != "第一段。\n第二段。" {
		t.Fatalf("unexpected recovered reply: %q", parsed.Reply)
	}
	if len(parsed.Messages) != 0 {
		t.Fatalf("duplicated messages were retained: %#v", parsed.Messages)
	}
	if parsed.Thought != "保留想法" || parsed.Action != "保留动作" {
		t.Fatalf("outer role metadata was lost: %#v", parsed)
	}
}

func TestBuildHistoryResponseExposesRoleFields(t *testing.T) {
	history := []memory.Message{{ID: 7, ConversationID: 1, Role: "assistant", Content: `{"thought":"想靠近一点","action":"向前走了一步","reply":"你好。"}`}}
	got := buildHistoryResponse(history)
	if len(got) != 1 {
		t.Fatalf("history response length = %d", len(got))
	}
	if got[0].Content != "你好。" || got[0].Reply != "你好。" || got[0].Thought != "想靠近一点" || got[0].Action != "向前走了一步" {
		t.Fatalf("unexpected history response: %#v", got[0])
	}
}

func TestParseRoleReplyRemovesDuplicatedActionFromReply(t *testing.T) {
	parsed, ok := parseRoleReply(`{"thought":"想抱抱她","action":"从背后抱住她","reply":"*从背后把她圈进怀里*\n累不累。\n*轻轻揉了揉她的头发*\n先休息一下。"}`)
	if !ok {
		t.Fatal("expected structured role reply")
	}
	if parsed.Action != "从背后抱住她" {
		t.Fatalf("action = %q", parsed.Action)
	}
	if parsed.Reply != "累不累。\n先休息一下。" {
		t.Fatalf("reply still contains action narration: %q", parsed.Reply)
	}
}

func TestParseRoleReplyToleratesUnescapedQuotes(t *testing.T) {
	raw := `{"thought":"页面标题就是"双重认证"，说明已开启。","action":"看了一眼截图。","reply":"点"好"关闭弹窗，再继续设置。"}`
	parsed, ok := parseRoleReply(raw)
	if !ok {
		t.Fatal("expected malformed structured role reply to be recovered")
	}
	if parsed.Thought != `页面标题就是"双重认证"，说明已开启。` || parsed.Action != "看了一眼截图。" || parsed.Reply != `点"好"关闭弹窗，再继续设置。` {
		t.Fatalf("unexpected recovered role reply: %#v", parsed)
	}
}

func TestParseRoleReplyToleratesPlainLabelsAndActiongTypo(t *testing.T) {
	parsed, ok := parseRoleReply("thought：有点担心她\nactiong：轻轻握住她的手\nreply：我在这里。")
	if !ok {
		t.Fatal("expected labeled role reply to be recovered")
	}
	if parsed.Thought != "有点担心她" || parsed.Action != "轻轻握住她的手" || parsed.Reply != "我在这里。" {
		t.Fatalf("unexpected recovered role reply: %#v", parsed)
	}
}

func TestMemoryChannelRunsNativeToolLoopWithGlobalMemory(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("B tool loop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(conversation.ID, "user", "回顾今天凌晨出租屋的角色扮演"); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(conversation.ID, "user", "找啊？"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO model_channel_configs(channel,base_url,model,api_key) VALUES(?,?,?,?)`, rhysMemoryChannel, "https://memory.example/v1", "memory-model", "memory-key"); err != nil {
		t.Fatal(err)
	}

	originalCall := callMemoryToolModel
	t.Cleanup(func() { callMemoryToolModel = originalCall })
	calls := 0
	callMemoryToolModel = func(ctx context.Context, _, _, _ string, messages []ChatMessage, _ int, tools []Tool, choice interface{}, _ func(string) error, _ upstreamPriority) (ChatResult, error) {
		calls++
		deadline, hasDeadline := ctx.Deadline()
		if !hasDeadline || time.Until(deadline) < 40*time.Second {
			t.Fatalf("B request deadline is still too short: %v, present=%t", time.Until(deadline), hasDeadline)
		}
		if len(tools) != len(chatTools()) {
			t.Fatalf("B received %d tools, want %d", len(tools), len(chatTools()))
		}
		toolNames := make(map[string]bool, len(tools))
		for _, tool := range tools {
			toolNames[tool.Function.Name] = true
		}
		for _, name := range []string{"see_screen", "search_memory", "get_current_time", "bell_create", "manage_life_data", "album_save", "fetch_url", "create_chat_file", "send_sticker", "list_stickers", "read_uploaded_doc", "linux_shell", "send_virtual_transfer", "receive_virtual_transfer"} {
			if !toolNames[name] {
				t.Fatalf("B tools missing %q: %#v", name, toolNames)
			}
		}
		if calls == 1 {
			if choice == nil {
				t.Fatal("memory route must force search_memory on the first call")
			}
			encoded, _ := json.Marshal(messages)
			var contextBuilder strings.Builder
			for _, message := range messages {
				fmt.Fprintln(&contextBuilder, message.Content)
			}
			contextText := contextBuilder.String()
			if !strings.Contains(contextText, "出租屋") || !strings.Contains(contextText, "全局记忆") {
				t.Fatalf("B context does not include conversation and global-memory instructions: %s", encoded)
			}
			for _, field := range []string{"context_json", `"user_profile"`, `"core_memory"`, `"active_memory"`, `"memory_index"`, `"uploaded_documents"`, `"conversation_state"`, `"recent_messages"`} {
				if !strings.Contains(contextText, field) {
					t.Fatalf("B context missing %s: %s", field, contextText)
				}
			}
			for _, rule := range []string{"睡觉、困倦、失眠、熬夜、晚安、刚醒、起床或作息", "必须调用 get_current_time", "不要仅凭对话或上下文中的旧时间"} {
				if !strings.Contains(contextText, rule) {
					t.Fatalf("B context missing sleep-time rule %q: %s", rule, contextText)
				}
			}
			var call ToolCall
			call.ID = "memory-1"
			call.Type = "function"
			call.Function.Name = "search_memory"
			call.Function.Arguments = `{"query":"出租屋 RP","scope":"all","limit":8}`
			return ChatResult{ToolCalls: []ToolCall{call}}, nil
		}
		if calls == 2 {
			if len(messages) == 0 || messages[len(messages)-1].Role != "tool" {
				t.Fatalf("B follow-up did not receive raw tool result: %#v", messages)
			}
			return ChatResult{Content: "状态：未找到。已查当前会话原始消息及全局记忆，未找到出租屋 RP 正文；只找到相关提及。"}, nil
		}
		return ChatResult{}, fmt.Errorf("unexpected B call %d", calls)
	}

	history, err := memory.GetHistory(conversation.ID, maxHistory)
	if err != nil {
		t.Fatal(err)
	}
	channelAContext, err := buildJSONContextMessages(conversation.ID, "找啊？", history, nil)
	if err != nil {
		t.Fatal(err)
	}
	rawPayload, err := collectToolSummaryWithMemoryChannel("找啊？", conversation.ID, channelAContext.Messages, chatTools(), true, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(rawPayload, `"tool_results"`) || !strings.Contains(rawPayload, "no_evidence") {
		t.Fatalf("calls=%d rawPayload=%q", calls, rawPayload)
	}
}

func TestUnfinishedToolReplyIsBlocked(t *testing.T) {
	for _, reply := range []string{"我现在去找，你等我一下。", "正在查，给我十秒。", "Let me search, give me a second."} {
		if !unfinishedToolReply(reply) {
			t.Fatalf("reply should be blocked: %q", reply)
		}
	}
	for _, reply := range []string{
		"已经查完，只找到两条相关消息，没有找到完整正文。",
		"行，先等一下。我不动。",
		"好，稍等，我们先不继续。",
		"等我一下，我整理整理思路。",
	} {
		if unfinishedToolReply(reply) {
			t.Fatalf("completed result or pause acknowledgement must not be blocked: %q", reply)
		}
	}
	fallback := completedToolFallback("状态：部分找到。找到两条提及，缺少完整正文。")
	if !strings.Contains(fallback, "工具调用已经完成") || !strings.Contains(fallback, "缺少完整正文") {
		t.Fatalf("fallback=%q", fallback)
	}
}

func TestProfileQuestionsForceGlobalMemorySearch(t *testing.T) {
	for _, message := range []string{"介绍我", "我喜欢什么？", "说说我的偏好和雷区", "what do you know about me"} {
		if !explicitMemorySearchRequested(message) {
			t.Fatalf("profile question did not trigger memory search: %q", message)
		}
		choice := chatToolChoice(message, chatTools())
		encoded, _ := json.Marshal(choice)
		if !strings.Contains(string(encoded), "search_memory") {
			t.Fatalf("profile question did not force search_memory: %q choice=%s", message, encoded)
		}
	}
}

func TestReplyAndMemoryChannelShareCompactContext(t *testing.T) {
	setupAPITestDB(t)
	if err := memory.SaveChunk(1, memory.ScopeGlobal, "manual", sql.NullInt64{}, "用户一直喜欢冷色界面和清脆铃声。", "用户喜欢冷色界面", "偏好,界面,铃声"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE memory_chunks SET importance='high',topic_label='界面偏好'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := memory.SaveChunk(1, memory.ScopeGlobal, "manual", sql.NullInt64{}, fmt.Sprintf("近期普通事项 %d", i), fmt.Sprintf("普通事项 %d", i), "临时,事项"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`UPDATE memory_chunks SET importance='high',topic_label=? WHERE id=(SELECT MAX(id) FROM memory_chunks)`, fmt.Sprintf("普通事项%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := buildJSONContextMessages(1, "介绍我", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result.Debug.FinalContextJSON.MemoryIndex); got > memoryIndexLimit() {
		t.Fatalf("reply memory_index count=%d, limit=%d", got, memoryIndexLimit())
	}
	toolMessages, err := memoryToolContext(1, "介绍我", result.Messages)
	if err != nil {
		t.Fatal(err)
	}
	toolText := fmt.Sprint(toolMessages)
	if toolText != fmt.Sprint(result.Messages) {
		t.Fatalf("B context differs from bounded A context")
	}
	if strings.Contains(toolText, "用户喜欢冷色界面") {
		t.Fatalf("B re-injected memory excluded from the bounded A context: %s", toolText)
	}
}

func setupAPITestDB(t *testing.T) {
	t.Helper()
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() {
		if db.DB != nil {
			_ = db.DB.Close()
			db.DB = nil
		}
	})
}

func TestExecuteMemoryToolRetriesAndReturnsErrorState(t *testing.T) {
	previousSleep := sleepMemoryToolRetry
	var sleeps int
	sleepMemoryToolRetry = func(time.Duration) { sleeps++ }
	t.Cleanup(func() { sleepMemoryToolRetry = previousSleep })

	call := ToolCall{ID: "call-failure"}
	call.Function.Name = "test_missing_tool"
	raw, attempts := executeMemoryToolWithRetry(call, 1, "", nil, nil)
	if attempts != 3 || sleeps != 2 {
		t.Fatalf("attempts=%d sleeps=%d, want 3 attempts and 2 delays", attempts, sleeps)
	}
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("error state is not JSON: %v: %s", err, raw)
	}
	if state["tool_name"] != "test_missing_tool" || state["error_type"] != "tool_execution_failed" || state["attempted_count"] != float64(3) {
		t.Fatalf("unexpected error state: %#v", state)
	}
}

func TestToolResultRequiresExpectedCoreFields(t *testing.T) {
	tests := []struct {
		tool string
		raw  string
		fail bool
	}{
		{"get_current_time", `{}`, true},
		{"get_current_time", "date:2026-08-08\ntime:12:00:00\ntimezone:CST\niso8601:2026-08-08T12:00:00+08:00", false},
		{"search_memory", `{"no_evidence":false}`, true},
		{"search_memory", "no_evidence:false\ncount:1\nmemory_1_summary:事实", false},
		{"read_uploaded_doc", "no_evidence:false\ndoc_id:1\ndoc_title:a.pdf", true},
		{"read_uploaded_doc", "no_evidence:false\ndoc_id:1\ndoc_title:a.pdf\ndoc_content_chars:20\ncontent_excerpt:\n正文", false},
		{"search_memory", `{"error":"database unavailable"}`, true},
	}
	for _, test := range tests {
		if got := toolResultHasError(test.tool, test.raw); got != test.fail {
			t.Errorf("tool=%s raw=%q failure=%v want %v", test.tool, test.raw, got, test.fail)
		}
	}
}

func TestMemoryChannelReportsDecisionErrorToA(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(rhysMemoryChannel, modelChannelInput{BaseURL: modelChannelString("https://memory.example/v1"), Model: modelChannelString("memory-model"), APIKey: modelChannelString("memory-key")}); err != nil {
		t.Fatal(err)
	}
	originalCall := callMemoryToolModel
	callMemoryToolModel = func(context.Context, string, string, string, []ChatMessage, int, []Tool, interface{}, func(string) error, upstreamPriority) (ChatResult, error) {
		return ChatResult{}, errors.New("provider unavailable")
	}
	t.Cleanup(func() { callMemoryToolModel = originalCall })
	payload, err := collectToolSummaryWithMemoryChannel("查记忆", 1, []ChatMessage{{Role: "user", Content: "查记忆"}}, chatTools(), true, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("B error must not abort chat: %v", err)
	}
	var response struct {
		Errors []channelAError `json:"errors"`
	}
	if json.Unmarshal([]byte(payload), &response) != nil || len(response.Errors) != 1 || response.Errors[0].ErrorSource != "channel_b.tool_decision" || response.Errors[0].ErrorMessage == "" || response.Errors[0].Timestamp == "" {
		t.Fatalf("missing standard B error response: %s", payload)
	}
}

func TestMemoryChannelDoesNotRetryTimedOutFileDecision(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(rhysMemoryChannel, modelChannelInput{BaseURL: modelChannelString("https://memory.example/v1"), Model: modelChannelString("memory-model"), APIKey: modelChannelString("memory-key")}); err != nil {
		t.Fatal(err)
	}
	originalCall := callMemoryToolModel
	calls := 0
	callMemoryToolModel = func(ctx context.Context, _ string, _ string, _ string, _ []ChatMessage, _ int, _ []Tool, _ interface{}, _ func(string) error, _ upstreamPriority) (ChatResult, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 85*time.Second {
			t.Fatalf("file decision deadline is too short: %v, present=%t", time.Until(deadline), ok)
		}
		return ChatResult{}, context.DeadlineExceeded
	}
	t.Cleanup(func() { callMemoryToolModel = originalCall })

	payload, err := collectToolSummaryWithMemoryChannel("生成文件", 1, []ChatMessage{{Role: "user", Content: "生成文件"}}, chatTools(), false, true, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("timed-out provider request was billed %d times, want 1", calls)
	}
	if !strings.Contains(payload, "channel_b.tool_decision") {
		t.Fatalf("missing B timeout payload: %s", payload)
	}
}

func TestRawToolResultForAPreservesJSON(t *testing.T) {
	call := ToolCall{ID: "call-raw"}
	call.Function.Name = "search_memory"
	raw := `{"items":[{"id":7,"content":"原文"}],"count":1}`
	result := rawToolResultForA(call, raw, 1)
	if result.Truncated || string(result.Result) != raw || result.TotalLength != utf8.RuneCountInString(raw) {
		t.Fatalf("raw result changed: %#v", result)
	}
	payload := marshalRawToolResults([]channelAToolResult{result})
	if !json.Valid([]byte(payload)) || !strings.Contains(payload, `"content":"原文"`) {
		t.Fatalf("forwarded payload is invalid or incomplete: %s", payload)
	}
}

func TestRawToolResultForAAllowsTypicalMemorySearchResult(t *testing.T) {
	call := ToolCall{ID: "call-memory"}
	call.Function.Name = "search_memory"
	raw := "no_evidence:false\ncount:6\nmemory_1_content:" + strings.Repeat("文", 6500)
	if estimateWorldBookTokens(raw) <= 3000 {
		t.Fatalf("test fixture no longer exceeds the former limit: %d", estimateWorldBookTokens(raw))
	}
	result := rawToolResultForA(call, raw, 1)
	if result.Truncated || string(result.Result) == "" {
		t.Fatalf("typical memory result was truncated: %#v", result)
	}
}

func TestRawToolResultForATruncatesWithMetadata(t *testing.T) {
	call := ToolCall{ID: "call-large"}
	call.Function.Name = "read_uploaded_doc"
	raw := `{"content":"` + strings.Repeat("文", maxRawToolResultTokens*4+50) + `"}`
	result := rawToolResultForA(call, raw, 1)
	if !result.Truncated || result.TotalLength != utf8.RuneCountInString(raw) || result.RawPrefix == "" || len(result.Result) != 0 {
		t.Fatalf("unexpected truncated result: %#v", result)
	}
	if estimateWorldBookTokens(result.RawPrefix) > maxRawToolResultTokens {
		t.Fatalf("raw prefix exceeds token budget: %d", estimateWorldBookTokens(result.RawPrefix))
	}
	if payload := marshalRawToolResults([]channelAToolResult{result}); !json.Valid([]byte(payload)) || !strings.Contains(payload, `"truncated":true`) {
		t.Fatalf("truncated payload is not structured JSON: %s", payload)
	}
}

func TestChatResponseMaxTokensLeavesHeadroomForNormalReplies(t *testing.T) {
	if got := chatResponseMaxTokens("请详细解释"); got != 6144 {
		t.Fatalf("normal reply max tokens = %d, want 6144", got)
	}
	if got := chatResponseMaxTokens("请生成一个文件"); got != 8192 {
		t.Fatalf("file reply max tokens = %d, want 8192", got)
	}
}
