package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestValidNativeReplyClient(t *testing.T) {
	for _, value := range []string{"MyAppBeta/15", "MyAppBeta/16", "MyAppBeta/120"} {
		if !validNativeReplyClient(value) {
			t.Fatalf("expected valid marker %q", value)
		}
	}
	for _, value := range []string{"", "MyAppBeta/14", "MyAppBeta/15x", "Other/15", " MyAppBeta/15"} {
		if validNativeReplyClient(value) {
			t.Fatalf("expected invalid marker %q", value)
		}
	}
}

func TestPrepareNativeReplyBodyForcesNonStreaming(t *testing.T) {
	request, body, err := prepareNativeReplyBody([]byte(`{"conversation_id":47,"assistant":"grok","message":"hello","stream":true}`))
	if err != nil || request.ConversationID != 47 {
		t.Fatalf("prepare request: %#v, %v", request, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["stream"] != false {
		t.Fatalf("stream was not disabled: %#v", payload["stream"])
	}
}

func TestNativeReplyJobIDFromPath(t *testing.T) {
	valid := "/api/native-replies/12345678-1234-4abc-8def-1234567890ab/result"
	if job, ok := nativeReplyJobIDFromPath(valid); !ok || job != "12345678-1234-4abc-8def-1234567890ab" {
		t.Fatalf("valid path rejected: %q, %v", job, ok)
	}
	for _, path := range []string{"/api/native-replies/result", valid + "/extra", "/api/native-replies/../../result"} {
		if _, ok := nativeReplyJobIDFromPath(path); ok {
			t.Fatalf("invalid path accepted: %q", path)
		}
	}
}

func TestNativeReplyRecorder(t *testing.T) {
	recorder := &nativeReplyRecorder{header: make(http.Header)}
	recorder.Header().Set("Content-Type", "application/json")
	_, _ = recorder.Write([]byte(`{"ok":true}`))
	if recorder.statusCode() != http.StatusOK || recorder.body.String() != `{"ok":true}` {
		t.Fatalf("unexpected recorder: %d %q", recorder.statusCode(), recorder.body.String())
	}
}

func TestNativeReplyFailureMessage(t *testing.T) {
	if got := nativeReplyFailureMessage("chat status 422"); got == nativeReplyFailureMessage("chat status 500") {
		t.Fatal("budget failure must have an actionable reason")
	}
	if got := nativeReplyFailureMessage("provider secret URL"); got != "后台回复生成失败，请稍后重试。" {
		t.Fatal("raw failure must not reach client")
	}
}
func TestNativeReplyResultMatchesSwiftStringIDs(t *testing.T) {
	raw, err := json.Marshal(nativeReplyResult{JobID: "job", Status: "completed", ConversationID: 47, AssistantMessageID: 82})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Conversation string `json:"conversation_id"`
		Message      string `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Conversation != "47" || document.Message != "82" {
		t.Fatalf("Swift result contract: %s, %v", raw, err)
	}
}
