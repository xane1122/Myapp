package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"myapp/internal/db"
)

func galgameRequest(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handleGalgame(res, req)
	return res
}

func TestGalgameStoryLifecycle(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "galgame.sqlite"))
	created := galgameRequest(t, http.MethodPost, "/api/galgame/stories", `{"title":"测试故事","start_node_key":"start"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var story struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &story); err != nil || story.ID == 0 {
		t.Fatalf("created story: %#v err=%v", story, err)
	}

	imported := galgameRequest(t, http.MethodPost, "/api/galgame/stories/1/nodes/import", `{"nodes":[{"node_key":"start","content":"开场","options":[{"id":1,"label":"前进","next_node_key":"end"}]},{"node_key":"end","content":"结局","is_ending":true,"ending_name":"好结局"}]}`)
	if imported.Code != http.StatusOK {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	played := galgameRequest(t, http.MethodGet, "/api/galgame/stories/1/play", "")
	if played.Code != http.StatusOK || !bytes.Contains(played.Body.Bytes(), []byte("开场")) {
		t.Fatalf("play: %d %s", played.Code, played.Body.String())
	}
	chosen := galgameRequest(t, http.MethodPost, "/api/galgame/stories/1/choose", `{"node_key":"start","option_id":1,"flags":{}}`)
	if chosen.Code != http.StatusOK || !bytes.Contains(chosen.Body.Bytes(), []byte("结局")) {
		t.Fatalf("choose: %d %s", chosen.Code, chosen.Body.String())
	}
	saved := galgameRequest(t, http.MethodPost, "/api/galgame/stories/1/saves", `{"node_key":"end","flags":{}}`)
	if saved.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	endings := galgameRequest(t, http.MethodGet, "/api/galgame/stories/1/endings", "")
	if endings.Code != http.StatusOK || !bytes.Contains(endings.Body.Bytes(), []byte(`"unlocked":true`)) {
		t.Fatalf("endings: %d %s", endings.Code, endings.Body.String())
	}
	listed := galgameRequest(t, http.MethodGet, "/api/galgame/stories", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte("测试故事")) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
}
