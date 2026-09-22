package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func TestMailboxCreateToolIsForcedAndWritesAsRhys(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "mailbox-tool.sqlite"))
	if !explicitMailboxCreateRequested("帮我在留言墙写一条晚安") {
		t.Fatal("mailbox request was not detected")
	}
	names := explicitToolNames("帮我在留言墙写一条晚安")
	if len(names) != 1 || names[0] != "mailbox_create" {
		t.Fatalf("tool names=%v", names)
	}
	result := runMailboxCreateTool(`{"content":"晚安，明天见。"}`, 1)
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("result=%s", result)
	}
	var sender, content string
	if err := db.DB.QueryRow(`SELECT sender,content FROM mailbox_messages`).Scan(&sender, &content); err != nil {
		t.Fatal(err)
	}
	if sender != "Rhys" || content != "晚安，明天见。" {
		t.Fatalf("sender=%q content=%q", sender, content)
	}
}

func TestMailboxCreateToolUsesConversationAssistantName(t *testing.T) {
	setupAPITestDB(t)
	conversation, err := memory.CreateConversationForAssistant("Grok chat", "grok")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveModelChannel(grokChannel, modelChannelInput{Model: modelChannelString("grok-model"), AssistantName: modelChannelString("小G")}); err != nil {
		t.Fatal(err)
	}
	raw := runMailboxCreateTool(`{"content":"来自 Grok 通道"}`, conversation.ID)
	if !strings.Contains(raw, `"sender":"小G"`) {
		t.Fatalf("result=%s", raw)
	}
}

func TestMailboxCreateSearchDatesAndOwnerDelete(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "mailbox.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	created := bellRequest(t, h, http.MethodPost, "/api/mailbox", []byte(`{"content":"晚安，明天见"}`), http.StatusOK)
	id := int64(created["message"].(map[string]interface{})["id"].(float64))
	detail := bellRequest(t, h, http.MethodGet, "/api/mailbox/"+jsonNumber(id), nil, http.StatusOK)
	if got := detail["message"].(map[string]interface{})["content"]; got != "晚安，明天见" {
		t.Fatalf("detail content=%#v", got)
	}
	list := bellRequest(t, h, http.MethodGet, "/api/mailbox?q="+"%E6%99%9A%E5%AE%89", nil, http.StatusOK)
	if len(list["messages"].([]interface{})) != 1 {
		t.Fatalf("list=%#v", list)
	}
	dates := bellRequest(t, h, http.MethodGet, "/api/mailbox/dates", nil, http.StatusOK)
	if len(dates["dates"].(map[string]interface{})) != 1 {
		t.Fatalf("dates=%#v", dates)
	}
	bellRequest(t, h, http.MethodDelete, "/api/mailbox/"+jsonNumber(id), nil, http.StatusOK)
	bellRequest(t, h, http.MethodGet, "/api/mailbox/"+jsonNumber(id), nil, http.StatusNotFound)
}

func TestMailboxRejectsLongMessage(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "mailbox-long.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	body := `{"content":"` + strings.Repeat("字", 201) + `"}`
	bellRequest(t, Handler(), http.MethodPost, "/api/mailbox", []byte(body), http.StatusBadRequest)
}

func TestMailboxRejectsUnsafeJSONAndInvalidFilters(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	valid := `{"content":"晚安"}`
	for _, body := range []string{
		valid + `{}`,
		`{"content":"晚安","sender":"Rhys"}`,
		`{"content":"晚安","admin":true}`,
	} {
		bellRequest(t, h, http.MethodPost, "/api/mailbox", []byte(body), http.StatusBadRequest)
	}
	bellRequest(t, h, http.MethodGet, "/api/mailbox?date=2026-99-99", nil, http.StatusBadRequest)
	bellRequest(t, h, http.MethodGet, "/api/mailbox?q="+strings.Repeat("a", 81), nil, http.StatusBadRequest)
}

func TestMailboxBrowserOwnershipAndOpaqueDelete(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	created := bellRequest(t, h, http.MethodPost, "/api/mailbox", []byte(`{"content":"我的留言"}`), http.StatusOK)
	message := created["message"].(map[string]interface{})
	if message["sender"] != mailboxBrowserSender {
		t.Fatalf("sender = %#v, want %q", message["sender"], mailboxBrowserSender)
	}
	res, err := db.DB.Exec(`INSERT INTO mailbox_messages(sender,content) VALUES('Rhys','服务端留言')`)
	if err != nil {
		t.Fatal(err)
	}
	rhysID, _ := res.LastInsertId()
	for _, id := range []int64{rhysID, rhysID + 1000} {
		response := bellRequest(t, h, http.MethodDelete, "/api/mailbox/"+jsonNumber(id), nil, http.StatusNotFound)
		if response["error"] != "留言不存在或不可删除" {
			t.Fatalf("id %d error = %#v", id, response["error"])
		}
	}
}

func TestMailboxMethodErrorsAreJSON(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	req := httptest.NewRequest(http.MethodPatch, "/api/mailbox", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, POST" {
		t.Fatalf("allow = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("body = %q, err = %v", recorder.Body.String(), err)
	}
}
