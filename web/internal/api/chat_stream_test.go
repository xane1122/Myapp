package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedResponseRecorder struct {
	mu sync.Mutex
	*httptest.ResponseRecorder
}

func (r *lockedResponseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}

func (r *lockedResponseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *lockedResponseRecorder) bodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func TestChatEventStreamWritesNDJSONEvents(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream, err := newChatEventStream(recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.writeModelDelta("你\n好"); err != nil {
		t.Fatal(err)
	}
	if err := stream.writeToolProgress("call_1", "search_memory", "started", "memory-model"); err != nil {
		t.Fatal(err)
	}
	if err := stream.write("done", chatResp{Reply: "你好", ConversationID: 7}); err != nil {
		t.Fatal(err)
	}

	response := recorder.Result()
	if got := response.Header.Get("Content-Type"); got != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if got := response.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	lines := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("event lines = %d, want 2; body=%q", len(lines), recorder.Body.String())
	}
	var delta chatStreamEvent
	if err := json.Unmarshal([]byte(lines[0]), &delta); err != nil {
		t.Fatal(err)
	}
	if delta.Type != "delta" {
		t.Fatalf("first event type = %q", delta.Type)
	}
	var tool chatStreamEvent
	if err := json.Unmarshal([]byte(lines[1]), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Type != "tool" || !strings.Contains(string(tool.Data.(map[string]interface{})["name"].(string)), "search_memory") {
		t.Fatalf("tool event = %#v", tool)
	}
	var done struct {
		Type string   `json:"type"`
		Data chatResp `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &done); err != nil {
		t.Fatal(err)
	}
	if done.Type != "done" || done.Data.Reply != "你好" || done.Data.ConversationID != 7 {
		t.Fatalf("done event = %#v", done)
	}
}

func TestChatEventStreamKeepAliveWritesPing(t *testing.T) {
	recorder := &lockedResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	stream, err := newChatEventStream(recorder)
	if err != nil {
		t.Fatal(err)
	}
	stop := stream.keepAlive(time.Millisecond)
	defer stop()

	deadline := time.Now().Add(100 * time.Millisecond)
	for !strings.Contains(recorder.bodyString(), `"type":"ping"`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if body := recorder.bodyString(); !strings.Contains(body, `"type":"ping"`) {
		t.Fatalf("keepalive event missing; body=%q", body)
	}
}

func TestStatusRecorderFlushPreservesStreamingStatus(t *testing.T) {
	base := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: base}
	recorder.Flush()
	if recorder.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.status)
	}
	if !base.Flushed {
		t.Fatal("underlying response was not flushed")
	}
}

type nonFlushingResponseWriter struct {
	header http.Header
}

func (w *nonFlushingResponseWriter) Header() http.Header         { return w.header }
func (w *nonFlushingResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nonFlushingResponseWriter) WriteHeader(int)             {}

func TestNewChatEventStreamRequiresFlusher(t *testing.T) {
	w := &nonFlushingResponseWriter{header: make(http.Header)}
	if _, err := newChatEventStream(w); err == nil {
		t.Fatal("expected unsupported streaming error")
	}
}

func TestRememberNonEmptyContentPreservesToolReply(t *testing.T) {
	got := rememberNonEmptyContent("", "工具执行后的长回复")
	got = rememberNonEmptyContent(got, "  ")
	if got != "工具执行后的长回复" {
		t.Fatalf("content = %q", got)
	}
}
