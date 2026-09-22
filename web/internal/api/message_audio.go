package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"myapp/internal/memory"
)

var replyAudioMu sync.Mutex

func ensureReplyAudio(ctx context.Context, conversationID, messageID int64, reply string, force bool) (string, error) {
	replyAudioMu.Lock()
	defer replyAudioMu.Unlock()
	if !force {
		if existing := edgeTTS.ExistingURL(messageID); existing != "" {
			return existing, nil
		}
	}
	if strings.TrimSpace(reply) == "" {
		message, err := memory.GetMessage(conversationID, messageID)
		if err != nil {
			return "", err
		}
		if message.Role != "assistant" {
			return "", errors.New("只能为 AI 消息生成语音")
		}
		roleContent, _ := parseRoleReply(message.Content)
		reply = roleContent.Reply
	}
	if strings.TrimSpace(reply) == "" {
		return "", errors.New("这条 AI 消息没有可朗读的文字")
	}
	return synthesizeReplyAudio(ctx, reply, messageID)
}

func handleMessageAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ConversationID int64 `json:"conversation_id"`
		MessageID      int64 `json:"message_id"`
	}
	if err := decodeAlbumJSON(w, r, &req); err != nil || req.ConversationID <= 0 || req.MessageID <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "消息参数无效"})
		return
	}
	message, err := memory.GetMessage(req.ConversationID, req.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "找不到这条消息"})
		return
	}
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if message.Role != "assistant" {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "只能为 AI 消息生成语音"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	url, err := ensureReplyAudio(ctx, req.ConversationID, req.MessageID, "", false)
	if err != nil {
		jsonResp(w, http.StatusBadGateway, map[string]string{"error": "语音生成失败：" + err.Error()})
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"audio_url": url})
}
