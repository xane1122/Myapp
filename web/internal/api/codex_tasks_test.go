package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestHandleCodexTasksReturnsBoundedResultTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-tasks.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE tasks (
		id INTEGER PRIMARY KEY, sender TEXT NOT NULL, prompt TEXT NOT NULL,
		result TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	result := "start-marker" + strings.Repeat("x", 30000) + "final-marker"
	if _, err := database.Exec(`INSERT INTO tasks VALUES (1,'web','fix it',?,'now','now','running')`, result); err != nil {
		t.Fatal(err)
	}
	database.Close()
	t.Setenv("CODEX_BRIDGE_DB", path)

	recorder := httptest.NewRecorder()
	handleCodexTasks(recorder, httptest.NewRequest(http.MethodGet, "/api/codex-tasks?limit=30", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "start-marker") || !strings.Contains(body, "final-marker") || !strings.Contains(body, `"result_truncated":true`) {
		t.Fatalf("response did not return a bounded result tail: %s", body)
	}
	if recorder.Body.Len() > 26000 {
		t.Fatalf("response too large: %d bytes", recorder.Body.Len())
	}
}
