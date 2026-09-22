package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func TestMessageSearchFindsMessagesAcrossConversations(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("旅行计划")
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := memory.SaveMessage(conversation.ID, "user", "周末去看银色风铃展览")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleMessageSearch(rec, httptest.NewRequest(http.MethodGet, "/api/messages/search?q="+url.QueryEscape("银色风铃"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			MessageID         int64  `json:"message_id"`
			ConversationID    int64  `json:"conversation_id"`
			ConversationTitle string `json:"conversation_title"`
			Content           string `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].MessageID != messageID || body.Items[0].ConversationID != conversation.ID || body.Items[0].ConversationTitle != "旅行计划" || !strings.Contains(body.Items[0].Content, "银色风铃") {
		t.Fatalf("unexpected results: %#v", body.Items)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM conversations WHERE id=?`, conversation.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conversation changed during search: count=%d err=%v", count, err)
	}
}

func TestMessageSearchRejectsEmptyQuery(t *testing.T) {
	setupAPITestDB(t)
	rec := httptest.NewRecorder()
	handleMessageSearch(rec, httptest.NewRequest(http.MethodGet, "/api/messages/search?q=", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestMessageSearchShowsSnippetAroundKeyword(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("长消息")
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("前", 400) + "银色风铃" + strings.Repeat("后", 400)
	if _, err := memory.SaveMessage(conversation.ID, "user", content); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleMessageSearch(rec, httptest.NewRequest(http.MethodGet, "/api/messages/search?q="+url.QueryEscape("银色风铃"), nil))
	var body struct {
		Items []struct {
			Content string `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || !strings.Contains(body.Items[0].Content, "银色风铃") {
		t.Fatalf("search snippet does not show the keyword: %#v", body.Items)
	}
}

func TestMessageSearchIgnoresHiddenAssistantFields(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("角色消息")
	if err != nil {
		t.Fatal(err)
	}
	stored := `{"thought":"银色风铃","action":"","reply":"今天去散步吧","messages":["今天去散步吧"]}`
	if _, err := memory.SaveMessage(conversation.ID, "assistant", stored); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleMessageSearch(rec, httptest.NewRequest(http.MethodGet, "/api/messages/search?q="+url.QueryEscape("银色风铃"), nil))
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 0 {
		t.Fatalf("hidden assistant fields leaked into results: %s", rec.Body.String())
	}
}

func TestMessageSearchReturnsMatchingAssistantBubble(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("分条回复")
	if err != nil {
		t.Fatal(err)
	}
	stored := `{"thought":"","action":"","reply":"第一条普通内容\n第二条包含银色风铃","messages":["第一条普通内容","第二条包含银色风铃"]}`
	messageID, err := memory.SaveMessage(conversation.ID, "assistant", stored)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleMessageSearch(rec, httptest.NewRequest(http.MethodGet, "/api/messages/search?q="+url.QueryEscape("银色风铃"), nil))
	var body struct {
		Items []struct {
			MessageID   int64  `json:"message_id"`
			BubbleIndex int    `json:"bubble_index"`
			Content     string `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].MessageID != messageID || body.Items[0].BubbleIndex != 1 || body.Items[0].Content != "第二条包含银色风铃" {
		t.Fatalf("unexpected matching bubble: %#v", body.Items)
	}
}

func TestMessageSearchPaginatesVisibleResults(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversation("分页搜索")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if _, err := memory.SaveMessage(conversation.ID, "user", fmt.Sprintf("分页关键词 %02d", i)); err != nil {
			t.Fatal(err)
		}
	}

	first := httptest.NewRecorder()
	handleMessageSearch(first, httptest.NewRequest(http.MethodGet, "/api/messages/search?q="+url.QueryEscape("分页关键词"), nil))
	var firstBody struct {
		Items      []json.RawMessage `json:"items"`
		HasMore    bool              `json:"has_more"`
		NextOffset int               `json:"next_offset"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if len(firstBody.Items) != 50 || !firstBody.HasMore || firstBody.NextOffset <= 0 {
		t.Fatalf("unexpected first page: count=%d has_more=%v next_offset=%d", len(firstBody.Items), firstBody.HasMore, firstBody.NextOffset)
	}

	second := httptest.NewRecorder()
	secondURL := fmt.Sprintf("/api/messages/search?q=%s&offset=%d", url.QueryEscape("分页关键词"), firstBody.NextOffset)
	handleMessageSearch(second, httptest.NewRequest(http.MethodGet, secondURL, nil))
	var secondBody struct {
		Items   []json.RawMessage `json:"items"`
		HasMore bool              `json:"has_more"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if len(secondBody.Items) != 5 || secondBody.HasMore {
		t.Fatalf("unexpected second page: count=%d has_more=%v", len(secondBody.Items), secondBody.HasMore)
	}
}

func TestMessageSearchRejectsInvalidOffset(t *testing.T) {
	setupAPITestDB(t)
	rec := httptest.NewRecorder()
	handleMessageSearch(rec, httptest.NewRequest(http.MethodGet, "/api/messages/search?q=test&offset=-1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
