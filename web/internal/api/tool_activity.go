package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"myapp/internal/db"
)

const maxToolActivityResultRunes = 12000

type toolActivityEntry struct {
	ID                 int64  `json:"id"`
	ConversationID     int64  `json:"conversation_id"`
	AssistantMessageID int64  `json:"assistant_message_id"`
	CallID             string `json:"call_id"`
	ToolName           string `json:"tool_name"`
	ModelName          string `json:"model_name"`
	Arguments          string `json:"arguments"`
	ResultSummary      string `json:"result_summary"`
	Status             string `json:"status"`
	StartedAt          string `json:"started_at"`
	CompletedAt        string `json:"completed_at"`
}

func startToolActivity(conversationID int64, modelName string, call ToolCall) int64 {
	arguments := truncateActivityText(redactToolJSON(call.Function.Arguments), 2000)
	result, err := db.DB.Exec(`INSERT INTO tool_activity(conversation_id,assistant_message_id,call_id,tool_name,model_name,arguments) VALUES(?,0,?,?,?,?)`, conversationID, call.ID, call.Function.Name, strings.TrimSpace(modelName), arguments)
	if err != nil {
		return 0
	}
	id, _ := result.LastInsertId()
	return id
}

func bindToolActivitiesToMessage(conversationID, assistantMessageID int64, activityIDs []int64) {
	if conversationID <= 0 || assistantMessageID <= 0 || len(activityIDs) == 0 {
		return
	}
	seen := make(map[int64]bool, len(activityIDs))
	for _, activityID := range activityIDs {
		if activityID <= 0 || seen[activityID] {
			continue
		}
		seen[activityID] = true
		_, _ = db.DB.Exec(`UPDATE tool_activity SET assistant_message_id=? WHERE id=? AND conversation_id=? AND assistant_message_id=0`, assistantMessageID, activityID, conversationID)
	}
}

func completeToolActivity(id int64, toolName, result string) {
	if id <= 0 {
		return
	}
	status := "completed"
	if toolResultHasError(toolName, result) {
		status = "failed"
	}
	_, _ = db.DB.Exec(`UPDATE tool_activity SET result_summary=?,status=?,completed_at=CURRENT_TIMESTAMP WHERE id=?`, truncateActivityText(redactToolJSON(result), maxToolActivityResultRunes), status, id)
}

func handleToolActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	conversationID, _ := strconv.ParseInt(r.URL.Query().Get("conversation_id"), 10, 64)
	assistantMessageID, _ := strconv.ParseInt(r.URL.Query().Get("assistant_message_id"), 10, 64)
	boundOnly := r.URL.Query().Get("bound_only") == "1"
	query := `SELECT id,conversation_id,assistant_message_id,call_id,tool_name,model_name,arguments,result_summary,status,started_at,COALESCE(completed_at,'') FROM tool_activity`
	args := []interface{}{}
	conditions := []string{}
	if conversationID > 0 {
		conditions = append(conditions, "conversation_id=?")
		args = append(args, conversationID)
	}
	if assistantMessageID > 0 {
		conditions = append(conditions, "assistant_message_id=?")
		args = append(args, assistantMessageID)
	}
	if boundOnly {
		conditions = append(conditions, "assistant_message_id>0 AND model_name<>''")
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY started_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "读取工具活动失败"})
		return
	}
	defer rows.Close()
	items := []toolActivityEntry{}
	for rows.Next() {
		var item toolActivityEntry
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.AssistantMessageID, &item.CallID, &item.ToolName, &item.ModelName, &item.Arguments, &item.ResultSummary, &item.Status, &item.StartedAt, &item.CompletedAt); err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "读取工具活动失败"})
			return
		}
		item.StartedAt = db.BeijingTimestamp(item.StartedAt)
		if item.CompletedAt != "" {
			item.CompletedAt = db.BeijingTimestamp(item.CompletedAt)
		}
		items = append(items, item)
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"items": items})
}

func redactToolJSON(raw string) string {
	var value interface{}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return raw
	}
	redactToolValue(value)
	data, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(data)
}

func redactToolValue(value interface{}) {
	object, ok := value.(map[string]interface{})
	if !ok {
		return
	}
	for key, child := range object {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "api_key") {
			object[key] = "[redacted]"
			continue
		}
		redactToolValue(child)
	}
}

func truncateActivityText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
