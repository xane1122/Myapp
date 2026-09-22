package api

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

const defaultCodexTaskDBPath = "/opt/myapp/codex_tasks.db"

type codexTaskSummary struct {
	ID              int64  `json:"id"`
	Sender          string `json:"sender"`
	Prompt          string `json:"prompt"`
	Result          string `json:"result"`
	ResultTruncated bool   `json:"result_truncated,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	Status          string `json:"status"`
}

func codexTaskDBPath() string {
	if path := strings.TrimSpace(os.Getenv("CODEX_BRIDGE_DB")); path != "" {
		return path
	}
	return defaultCodexTaskDBPath
}

func handleCodexTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 30
	}
	if limit > 50 {
		limit = 50
	}
	database, err := sql.Open("sqlite3", "file:"+codexTaskDBPath()+"?mode=ro&_busy_timeout=3000")
	if err != nil {
		jsonResp(w, http.StatusServiceUnavailable, map[string]interface{}{"error": "Codex 任务库暂时不可用"})
		return
	}
	defer database.Close()
	rows, err := database.Query(`SELECT id,sender,prompt,
		CASE WHEN length(result)>24000 THEN substr(result,-24000) ELSE result END,
		length(result)>24000,created_at,updated_at,status
		FROM tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		jsonResp(w, http.StatusServiceUnavailable, map[string]interface{}{"error": "Codex 任务读取失败"})
		return
	}
	defer rows.Close()
	tasks := make([]codexTaskSummary, 0, limit)
	for rows.Next() {
		var task codexTaskSummary
		if err := rows.Scan(&task.ID, &task.Sender, &task.Prompt, &task.Result, &task.ResultTruncated, &task.CreatedAt, &task.UpdatedAt, &task.Status); err != nil {
			jsonResp(w, http.StatusServiceUnavailable, map[string]interface{}{"error": "Codex 任务解析失败"})
			return
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, http.StatusServiceUnavailable, map[string]interface{}{"error": "Codex 任务读取失败"})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"tasks": tasks})
}
