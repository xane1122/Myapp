package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/db"
)

func TestRedactToolJSON(t *testing.T) {
	got := redactToolJSON(`{"query":"hello","api_key":"secret","nested":{"token":"value"}}`)
	var value map[string]interface{}
	if err := json.Unmarshal([]byte(got), &value); err != nil {
		t.Fatal(err)
	}
	if value["api_key"] != "[redacted]" || value["nested"].(map[string]interface{})["token"] != "[redacted]" {
		t.Fatalf("secrets were not redacted: %s", got)
	}
}

func TestHandleToolActivityRejectsWrites(t *testing.T) {
	recorder := httptest.NewRecorder()
	handleToolActivity(recorder, httptest.NewRequest(http.MethodPost, "/api/tool-activity", strings.NewReader("{}")))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleToolActivityFiltersConversation(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "tool-activity.sqlite"))
	if _, err := db.DB.Exec(`INSERT INTO tool_activity(conversation_id,call_id,tool_name,arguments) VALUES(1,'call-1','search_memory','{}'),(2,'call-2','fetch_url','{}')`); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handleToolActivity(recorder, httptest.NewRequest(http.MethodGet, "/api/tool-activity?conversation_id=2", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []toolActivityEntry `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].ConversationID != 2 || response.Items[0].CallID != "call-2" {
		t.Fatalf("items = %#v", response.Items)
	}
}

func TestToolActivityPersistsModelAndBindsAssistantMessage(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "tool-activity-binding.sqlite"))
	call := ToolCall{ID: "call-b", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "search_memory", Arguments: `{}`}}
	id := startToolActivity(1, "channel-b-model", call)
	if id <= 0 {
		t.Fatal("tool activity was not inserted")
	}
	otherID := startToolActivity(1, "other-model", call)
	bindToolActivitiesToMessage(1, 42, []int64{id})

	recorder := httptest.NewRecorder()
	handleToolActivity(recorder, httptest.NewRequest(http.MethodGet, "/api/tool-activity?conversation_id=1&assistant_message_id=42", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []toolActivityEntry `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].AssistantMessageID != 42 || response.Items[0].ModelName != "channel-b-model" {
		t.Fatalf("items = %#v", response.Items)
	}
	var otherMessageID int64
	if err := db.DB.QueryRow(`SELECT assistant_message_id FROM tool_activity WHERE id=?`, otherID).Scan(&otherMessageID); err != nil {
		t.Fatal(err)
	}
	if otherMessageID != 0 {
		t.Fatalf("same provider call_id cross-bound activity %d to message %d", otherID, otherMessageID)
	}
}

func TestHandleToolActivityBoundOnlyExcludesLegacyRows(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "tool-activity-bound-only.sqlite"))
	if _, err := db.DB.Exec(`INSERT INTO tool_activity(conversation_id,assistant_message_id,call_id,tool_name,model_name,arguments) VALUES
		(1,0,'legacy','search_memory','','{}'),(1,52,'stable','fetch_url','channel-b-model','{}')`); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handleToolActivity(recorder, httptest.NewRequest(http.MethodGet, "/api/tool-activity?conversation_id=1&bound_only=1", nil))
	var response struct {
		Items []toolActivityEntry `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].CallID != "stable" {
		t.Fatalf("items = %#v", response.Items)
	}
}

func TestCompleteToolActivityDoesNotTreatQuotedHistoryAsFailure(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "tool-activity-result.sqlite"))
	call := ToolCall{ID: "call-history", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "search_memory", Arguments: `{}`}}
	id := startToolActivity(1, "channel-b-model", call)
	result := "no_evidence:false\ncount:1\nmemory_1_content:" + strings.Repeat("正文", 900) + `{"error":"这是被检索到的旧消息，不是工具错误"}`
	completeToolActivity(id, call.Function.Name, result)

	var status, stored string
	if err := db.DB.QueryRow(`SELECT status,result_summary FROM tool_activity WHERE id=?`, id).Scan(&status, &stored); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("quoted history incorrectly marked activity as %q", status)
	}
	if len([]rune(stored)) != len([]rune(result)) {
		t.Fatalf("activity result was truncated: got %d runes, want %d", len([]rune(stored)), len([]rune(result)))
	}
}

func TestCompleteToolActivityMarksTopLevelErrorAsFailure(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "tool-activity-error.sqlite"))
	call := ToolCall{ID: "call-error", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "search_memory", Arguments: `{}`}}
	id := startToolActivity(1, "channel-b-model", call)
	completeToolActivity(id, call.Function.Name, `{"error":"database unavailable"}`)

	var status string
	if err := db.DB.QueryRow(`SELECT status FROM tool_activity WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("top-level error status = %q, want failed", status)
	}
}
