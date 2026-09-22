package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"myapp/internal/db"
	"myapp/internal/memory"
	"myapp/internal/observability"
)

const (
	nativeReplyClientHeader = "X-MyApp-Client"
	nativeReplyTokenHeader  = "X-MyApp-Reply-Token"
	nativeReplyOrigin       = "https://xanelove.com"
	maxNativeReplyBody      = 2 << 20
)

type nativeReplyTicket struct {
	JobID          string `json:"job_id"`
	ConversationID int64  `json:"conversation_id"`
	Assistant      string `json:"assistant,omitempty"`
	ResultURL      string `json:"result_url"`
	ResultToken    string `json:"result_token"`
}

type nativeReplyResult struct {
	JobID              string  `json:"job_id"`
	Status             string  `json:"status"`
	ConversationID     int64   `json:"conversation_id,string,omitempty"`
	AssistantMessageID int64   `json:"message_id,string,omitempty"`
	UserMessageID      int64   `json:"user_message_id,omitempty"`
	UserMessageIDs     []int64 `json:"user_message_ids,omitempty"`
	Assistant          string  `json:"assistant,omitempty"`
	Preview            string  `json:"preview,omitempty"`
	Error              string  `json:"error,omitempty"`
}

type chatFailureNotice struct {
	ID            int64  `json:"id"`
	UserMessageID int64  `json:"user_message_id"`
	Error         string `json:"error"`
	UpdatedAt     string `json:"updated_at"`
}

func handleChatFailures(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/api/chat-failures" {
		http.NotFound(w, r)
		return
	}
	conversationID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("conversation_id")), 10, 64)
	if err != nil || conversationID <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "conversation_id required"})
		return
	}
	rows, err := db.DB.Query(`
		SELECT failure_id,user_message_id,updated_at,error_text
		FROM (
			SELECT failed.rowid AS failure_id,failed.user_message_id,failed.updated_at,failed.error_text
			FROM native_reply_jobs AS failed
			WHERE failed.conversation_id=? AND failed.status='failed' AND failed.user_message_id>0
			  AND NOT EXISTS (
				SELECT 1 FROM native_reply_jobs AS completed
				WHERE completed.conversation_id=failed.conversation_id
				  AND completed.user_message_id=failed.user_message_id
				  AND completed.status='completed' AND completed.assistant_message_id>0
				  AND completed.updated_at>=failed.updated_at
			  )
			ORDER BY failed.rowid DESC
			LIMIT 20
		)
		ORDER BY failure_id`, conversationID)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "failure history unavailable"})
		return
	}
	defer rows.Close()
	failures := make([]chatFailureNotice, 0)
	for rows.Next() {
		var notice chatFailureNotice
		var failureText string
		if err := rows.Scan(&notice.ID, &notice.UserMessageID, &notice.UpdatedAt, &failureText); err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "failure history unavailable"})
			return
		}
		notice.Error = nativeReplyFailureMessage(failureText)
		failures = append(failures, notice)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "failure history unavailable"})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"conversation_id": conversationID, "failures": failures})
}

func validNativeReplyClient(value string) bool {
	const prefix = "MyAppBeta/"
	if !strings.HasPrefix(value, prefix) || strings.TrimSpace(value) != value {
		return false
	}
	build, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	return err == nil && build >= 15
}

func nativeReplyHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func newNativeReplyCredentials() (string, string, error) {
	jobBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(jobBytes); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	jobBytes[6] = (jobBytes[6] & 0x0f) | 0x40
	jobBytes[8] = (jobBytes[8] & 0x3f) | 0x80
	jobID := fmt.Sprintf("%x-%x-%x-%x-%x", jobBytes[0:4], jobBytes[4:6], jobBytes[6:8], jobBytes[8:10], jobBytes[10:16])
	return jobID, base64.RawURLEncoding.EncodeToString(tokenBytes), nil
}

func prepareNativeReplyBody(raw []byte) (chatReq, []byte, error) {
	var request chatReq
	if err := json.Unmarshal(raw, &request); err != nil {
		return request, nil, fmt.Errorf("消息格式无效")
	}
	if request.ConversationID <= 0 {
		return request, nil, fmt.Errorf("会话 ID 无效")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return request, nil, fmt.Errorf("消息格式无效")
	}
	payload["stream"] = false
	normalized, err := json.Marshal(payload)
	return request, normalized, err
}

func handleNativeReplies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/api/native-replies" {
		http.NotFound(w, r)
		return
	}
	if !validNativeReplyClient(r.Header.Get(nativeReplyClientHeader)) || r.Header.Get("Origin") != nativeReplyOrigin {
		jsonResp(w, http.StatusForbidden, map[string]string{"error": "native client required"})
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 16 || len(idempotencyKey) > 100 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "invalid idempotency key"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNativeReplyBody))
	if err != nil {
		jsonResp(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
		return
	}
	request, normalized, err := prepareNativeReplyBody(raw)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var active int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM native_reply_jobs WHERE status IN ('queued','running') AND expires_at>CURRENT_TIMESTAMP`).Scan(&active); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "native reply storage unavailable"})
		return
	}
	if active >= 4 {
		jsonResp(w, http.StatusTooManyRequests, map[string]string{"error": "too many native replies"})
		return
	}
	jobID, token, err := newNativeReplyCredentials()
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "could not create native reply"})
		return
	}
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	_, err = db.DB.Exec(`INSERT INTO native_reply_jobs(job_id,idempotency_key_hash,token_hash,status,conversation_id,assistant,expires_at) VALUES(?,?,?,'queued',?,?,?)`,
		jobID, nativeReplyHash(idempotencyKey), nativeReplyHash(token), request.ConversationID, normalizeAssistant(request.Assistant), expiresAt)
	if err != nil {
		jsonResp(w, http.StatusConflict, map[string]string{"error": "native reply request already exists"})
		return
	}
	ticket := nativeReplyTicket{JobID: jobID, ConversationID: request.ConversationID, Assistant: normalizeAssistant(request.Assistant),
		ResultURL: nativeReplyOrigin + "/api/native-replies/" + jobID + "/result", ResultToken: token}
	go runNativeReplyJob(jobID, normalized)
	jsonResp(w, http.StatusAccepted, ticket)
}

type nativeReplyJobContextKey struct{}

// Only the in-process job runner can attach this context; browser headers cannot.
func acknowledgeNativeReplyInput(r *http.Request, conversationID, userID int64, ids []int64) error {
	jobID, _ := r.Context().Value(nativeReplyJobContextKey{}).(string)
	if jobID == "" {
		return nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = db.DB.Exec(`UPDATE native_reply_jobs SET user_message_id=?,user_message_ids_json=?,updated_at=CURRENT_TIMESTAMP WHERE job_id=? AND conversation_id=? AND status='running'`, userID, string(encoded), jobID, conversationID)
	return err
}

func runNativeReplyJob(jobID string, body []byte) {
	_, _ = db.DB.Exec(`UPDATE native_reply_jobs SET status='running',updated_at=CURRENT_TIMESTAMP WHERE job_id=? AND status='queued'`, jobID)
	req, err := http.NewRequest(http.MethodPost, "/api/chat", bytes.NewReader(body))
	if err != nil {
		failNativeReplyJob(jobID, err)
		return
	}
	req = req.WithContext(context.WithValue(req.Context(), nativeReplyJobContextKey{}, jobID))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MyAppBeta/15")
	req.Header.Set(nativeReplyClientHeader, "MyAppBeta/15")
	recorder := &nativeReplyRecorder{header: make(http.Header)}
	handleChat(recorder, req)
	var response chatResp
	decodeErr := json.Unmarshal(recorder.body.Bytes(), &response)
	if err := nativeReplyResponseFailure(recorder.statusCode(), response, decodeErr); err != nil {
		failNativeReplyJob(jobID, err)
		return
	}
	userIDs, _ := json.Marshal(response.UserMessageIDs)
	_, err = db.DB.Exec(`UPDATE native_reply_jobs SET status='completed',conversation_id=?,assistant_message_id=?,user_message_id=?,user_message_ids_json=?,updated_at=CURRENT_TIMESTAMP WHERE job_id=?`,
		response.ConversationID, response.AssistantMessageID, response.UserMessageID, string(userIDs), jobID)
	if err != nil {
		failNativeReplyJob(jobID, err)
		return
	}
	observability.Event("native_reply.completed", map[string]interface{}{"job_id": jobID, "conversation_id": response.ConversationID, "message_id": response.AssistantMessageID})
}

func failNativeReplyJob(jobID string, err error) {
	text := "native reply failed"
	if err != nil {
		text = truncateRunes(err.Error(), 500)
	}
	_, _ = db.DB.Exec(`UPDATE native_reply_jobs SET status='failed',error_text=?,updated_at=CURRENT_TIMESTAMP WHERE job_id=?`, text, jobID)
	observability.Event("native_reply.failed", map[string]interface{}{"job_id": jobID, "error": text})
}

func nativeReplyJobIDFromPath(path string) (string, bool) {
	const prefix = "/api/native-replies/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/result") {
		return "", false
	}
	jobID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/result")
	parts := strings.Split(jobID, "-")
	wantedLengths := []int{8, 4, 4, 4, 12}
	if len(parts) != len(wantedLengths) || len(jobID) != 36 {
		return "", false
	}
	for index, part := range parts {
		if len(part) != wantedLengths[index] {
			return "", false
		}
		for _, char := range part {
			if !strings.ContainsRune("0123456789abcdef", char) {
				return "", false
			}
		}
	}
	return jobID, true
}

func handleNativeReplyRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobID, ok := nativeReplyJobIDFromPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	providedToken := r.Header.Get(nativeReplyTokenHeader)
	if len(providedToken) != 43 {
		http.NotFound(w, r)
		return
	}
	var storedHash string
	if err := db.DB.QueryRow(`SELECT token_hash FROM native_reply_jobs WHERE job_id=? AND expires_at>CURRENT_TIMESTAMP`, jobID).Scan(&storedHash); err != nil ||
		subtle.ConstantTimeCompare([]byte(storedHash), []byte(nativeReplyHash(providedToken))) != 1 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		var result nativeReplyResult
		var userIDsJSON, failureText string
		err := db.DB.QueryRow(`SELECT status,conversation_id,assistant_message_id,user_message_id,user_message_ids_json,assistant,error_text FROM native_reply_jobs WHERE job_id=? AND expires_at>CURRENT_TIMESTAMP`, jobID).
			Scan(&result.Status, &result.ConversationID, &result.AssistantMessageID, &result.UserMessageID, &userIDsJSON, &result.Assistant, &failureText)
		result.JobID = jobID
		if err != nil {
			result.Status = "failed"
		}
		_ = json.Unmarshal([]byte(userIDsJSON), &result.UserMessageIDs)
		if result.Status == "completed" || result.Status == "failed" || r.URL.Query().Get("progress") == "1" {
			if result.Status == "failed" {
				result.Error = nativeReplyFailureMessage(failureText)
			}
			if result.Status == "completed" {
				if message, messageErr := memory.GetMessage(result.ConversationID, result.AssistantMessageID); messageErr == nil {
					if legacyInvalidReplyFallback(message.Content) {
						result.Status = "failed"
						result.Error = invalidChatReplyError
					} else {
						role, _ := parseRoleReply(message.Content)
						result.Preview = truncateRunes(strings.TrimSpace(role.Reply), 240)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			_, _ = io.WriteString(w, " \n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

type nativeReplyRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *nativeReplyRecorder) Header() http.Header { return r.header }
func (r *nativeReplyRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *nativeReplyRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}
func (r *nativeReplyRecorder) Flush() {}
func (r *nativeReplyRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// Return a safe reason without exposing provider URLs, credentials or SQL errors.
func nativeReplyFailureMessage(failure string) string {
	if failure == invalidNativeReplyOutput {
		return invalidChatReplyError
	}
	if failure == "chat status 422" {
		return "本次输入与固定上下文超过当前预算，任务已失败。请减少附件或引用内容后重新发送；刷新聊天记录不会重试失败任务。"
	}
	return "后台回复生成失败，请稍后重试。"
}
