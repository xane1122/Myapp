package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"myapp/internal/db"
)

func TestHandleChatFailuresReturnsOnlySafeUnresolvedNotices(t *testing.T) {
	setupAPITestDB(t)
	insert := func(jobID, status string, userMessageID, assistantMessageID int64, failure string) {
		t.Helper()
		_, err := db.DB.Exec(`INSERT INTO native_reply_jobs(job_id,idempotency_key_hash,token_hash,status,conversation_id,assistant_message_id,user_message_id,error_text,expires_at) VALUES(?,?,?,?,1,?,?,?,datetime('now','+1 day'))`, jobID, "idem-"+jobID, "token-"+jobID, status, assistantMessageID, userMessageID, failure)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("failed-invalid", "failed", 41, 0, invalidNativeReplyOutput)
	insert("failed-private", "failed", 42, 0, "provider secret URL")
	insert("completed-later", "completed", 42, 99, "")
	insert("failed-generic", "failed", 43, 0, "upstream internal detail")

	request := httptest.NewRequest(http.MethodGet, "/api/chat-failures?conversation_id=1", nil)
	response := httptest.NewRecorder()
	handleChatFailures(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Failures []chatFailureNotice `json:"failures"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Failures) != 2 || payload.Failures[0].UserMessageID != 41 || payload.Failures[1].UserMessageID != 43 {
		t.Fatalf("unexpected notices: %#v", payload.Failures)
	}
	if payload.Failures[0].Error != invalidChatReplyError || payload.Failures[1].Error != "后台回复生成失败，请稍后重试。" {
		t.Fatalf("unsafe or incorrect errors: %#v", payload.Failures)
	}
	if strings.Contains(response.Body.String(), "provider") || strings.Contains(response.Body.String(), "upstream internal") {
		t.Fatalf("raw failure leaked: %s", response.Body.String())
	}
}
