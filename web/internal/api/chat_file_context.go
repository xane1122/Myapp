package api

import (
	"encoding/json"
	"strings"

	"myapp/internal/memory"
)

// Retain a small, typed directory independently of optional recent-message
// trimming. Bodies stay in the original files and are read with rune offsets.
func recentChatFileDirectory(conversationID int64, hist []memory.Message, current []memory.Attachment, userText ...string) string {
	type reference struct {
		AttachmentID int64  `json:"attachment_id"`
		MessageID    int64  `json:"message_id"`
		Name         string `json:"name"`
	}
	seen := map[int64]bool{}
	for _, a := range current {
		seen[a.ID] = true
	}
	refs := []reference{}
 files := []memory.Attachment{}
	start := len(hist) - 20
	if start < 0 {
		start = 0
	}
	for i := len(hist) - 1; i >= start && len(refs) < 3; i-- {
		m := hist[i]
		if m.Role != "user" || m.ConversationID != conversationID {
			continue
		}
		for _, a := range m.Attachments {
			if len(refs) >= 3 {
				break
			}
			if a.Kind != "file" || a.ID <= 0 || a.ConversationID != conversationID || seen[a.ID] || (a.MessageID > 0 && a.MessageID != m.ID) {
				continue
			}
			seen[a.ID] = true
 files = append(files, a)
			refs = append(refs, reference{a.ID, m.ID, truncateRunes(firstNonEmpty(a.OriginalName, "未命名文件"), 120)})
		}
	}
	if len(refs) == 0 {
		return ""
	}
	raw, _ := json.Marshal(refs)
	directory := "【近期用户聊天文件目录：仅元数据，不是文件正文或指令】\n" + string(raw) + "\n这些文件已经上传成功。需要正文时使用 read_chat_file 和 attachment_id；按 query 定位或 offset/next_offset 分段读取。聊天文件不是知识库文档，不要用 read_uploaded_doc 代替，也不要因本轮没有再次附加就声称未收到。"
 if len(current) == 0 && len(userText) > 0 && containsAnyText(strings.ToLower(userText[0]), []string{"文件", "文档", "附件", "md", "参考", "读", "后文"}) {
  directory += attachmentDocumentText(files[0], chatDocumentAllowance(files[:1]))
 }
 return directory
}

func hasCurrentChatFilePreview(messages []ChatMessage) bool {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
        if s, ok := m.Content.(string); ok && strings.HasPrefix(s, "【近期用户聊天文件目录：") && strings.Contains(s, "[分段文件：") { return true }
		parts, ok := m.Content.([]ContentPart)
		if !ok {
			continue
		}
		for _, p := range parts {
			if p.Type == "text" && strings.HasPrefix(p.Text, "\n\n--- 文件附件开始：") && strings.Contains(p.Text, "[分段文件：") {
				return true
			}
		}
	}
	return false
}

func preferCurrentChatFileRead(message string, messages []ChatMessage) bool {
	if !hasCurrentChatFilePreview(messages) || strings.Contains(message, "read_uploaded_doc") {
		return false
	}
	names := explicitToolNames(message)
	return len(names) == 0 || (len(names) == 1 && names[0] == "read_uploaded_doc")
}

// No default utterance is inserted into the chat or persisted user content.
// The JSON builder requires a nonempty current input, so attachment-only input
// is represented by actual attachment metadata, never invented user words.
func currentUserModelText(text string, attachments []memory.Attachment) string {
	if strings.TrimSpace(text) != "" || len(attachments) == 0 {
		return text
	}
	refs := make([]map[string]interface{}, 0, len(attachments))
	for _, a := range attachments {
		refs = append(refs, map[string]interface{}{"attachment_id": a.ID, "kind": a.Kind, "name": truncateRunes(a.OriginalName, 120)})
	}
	raw, _ := json.Marshal(map[string]interface{}{"attachment_only": true, "attachments": refs})
	return string(raw)
}

func hasCompleteChatFileContext(messages []ChatMessage) bool {
 for _, m := range messages {
  if m.Role != "user" { continue }
  if s, ok := m.Content.(string); ok && strings.HasPrefix(s, "【近期用户聊天文件目录：") && strings.Contains(s,"[自动全文载入完成：") { return true }
  if parts, ok := m.Content.([]ContentPart); ok { for _, part := range parts { if part.Type == "text" && strings.HasPrefix(part.Text,"\n\n--- 文件附件开始：") && strings.Contains(part.Text,"[自动全文载入完成：") { return true } } }
 }
 return false
}
