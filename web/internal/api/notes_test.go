package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"myapp/internal/db"
)

func TestNotesCRUDFiltersAndSoftDelete(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "notes.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()

	created := bellRequest(t, h, http.MethodPost, "/api/notes", []byte(`{"content":"继续讨论旅行计划","tags":["灵感","旅行","灵感"],"conversation_id":1,"author":"user","pinned":true}`), http.StatusOK)
	n := created["note"].(map[string]interface{})
	id := int64(n["id"].(float64))
	if n["status"] != "pending" || n["pinned"] != true || len(n["tags"].([]interface{})) != 2 {
		t.Fatalf("created note = %#v", n)
	}

	bellRequest(t, h, http.MethodPost, "/api/notes", []byte(`{"content":"已经办完的事","tags":["生活"],"author":"ai"}`), http.StatusOK)
	filtered := bellRequest(t, h, http.MethodGet, "/api/notes?tag=%E6%97%85%E8%A1%8C&author=user&status=pending&q=%E8%AE%A1%E5%88%92", nil, http.StatusOK)
	if got := len(filtered["notes"].([]interface{})); got != 1 {
		t.Fatalf("filtered notes length = %d", got)
	}

	updated := bellRequest(t, h, http.MethodPut, "/api/notes/"+jsonNumber(id), []byte(`{"content":"旅行计划已整理","tags":["旅行"],"status":"archived","conversation_id":1,"author":"ai","pinned":false}`), http.StatusOK)
	updatedNote := updated["note"].(map[string]interface{})
	if updatedNote["status"] != "archived" || updatedNote["author"] != "user" {
		t.Fatalf("updated = %#v", updated)
	}
	active := bellRequest(t, h, http.MethodGet, "/api/notes", nil, http.StatusOK)
	if len(active["notes"].([]interface{})) != 1 {
		t.Fatalf("active = %#v", active)
	}
	archived := bellRequest(t, h, http.MethodGet, "/api/notes?archived=1", nil, http.StatusOK)
	if len(archived["notes"].([]interface{})) != 1 {
		t.Fatalf("archived = %#v", archived)
	}

	bellRequest(t, h, http.MethodDelete, "/api/notes/"+jsonNumber(id), nil, http.StatusOK)
	bellRequest(t, h, http.MethodGet, "/api/notes/"+jsonNumber(id), nil, http.StatusNotFound)
	var deleted int
	if err := db.DB.QueryRow(`SELECT deleted FROM notes WHERE id=?`, id).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("soft deleted=%d err=%v", deleted, err)
	}
}

func TestNotesValidation(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "notes-validation.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	for _, body := range []string{
		`{"content":"","author":"user"}`,
		`{"content":"` + strings.Repeat("字", 501) + `","author":"user"}`,
		`{"content":"内容","status":"unknown","author":"user"}`,
		`{"content":"内容","status":"pending","author":"other"}`,
		`{"content":"内容","conversation_id":999,"author":"user"}`,
		`{"content":"内容","author":"user","admin":true}`,
		`{"content":"内容","author":"user"}{}`,
	} {
		bellRequest(t, h, http.MethodPost, "/api/notes", []byte(body), http.StatusBadRequest)
	}
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = "tag" + strconv.Itoa(i)
	}
	payload, err := json.Marshal(map[string]interface{}{"content": "内容", "author": "user", "tags": tags})
	if err != nil {
		t.Fatal(err)
	}
	bellRequest(t, h, http.MethodPost, "/api/notes", payload, http.StatusBadRequest)
	bellRequest(t, h, http.MethodGet, "/api/notes?q="+strings.Repeat("a", 501), nil, http.StatusBadRequest)
}
