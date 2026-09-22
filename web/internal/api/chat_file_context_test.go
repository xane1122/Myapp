package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/memory"
)

func TestChatFileContextDirectoryIsBoundedAndIsolated(t *testing.T) {
	hist := []memory.Message{}
	for i := int64(1); i <= 25; i++ {
		hist = append(hist, memory.Message{ID: i, ConversationID: 1, Role: "user", Attachments: []memory.Attachment{{ID: i, MessageID: i, ConversationID: 1, Kind: "file", OriginalName: fmt.Sprintf("ref%d.md", i), FilePath: "/private/path"}}})
	}
	hist = append(hist, memory.Message{ID: 26, ConversationID: 2, Role: "user", Attachments: []memory.Attachment{{ID: 999, ConversationID: 2, Kind: "file", OriginalName: "foreign.md"}}})
	d := recentChatFileDirectory(1, hist, []memory.Attachment{{ID: 25}})
	if strings.Count(d, `"attachment_id"`) != 3 || !strings.Contains(d, `"attachment_id":24`) || strings.Contains(d, `"attachment_id":25`) || strings.Contains(d, "foreign.md") || strings.Contains(d, "/private/path") || strings.Contains(d, "ref1.md") {
		t.Fatal("directory scope, deduplication or boundedness failed")
	}
	if recentChatFileDirectory(2, hist[:25], nil) != "" {
		t.Fatal("foreign conversation files included")
	}
	old := hist[:1]
	for i := 0; i < 20; i++ {
		old = append(old, memory.Message{ConversationID: 1, Role: "assistant"})
	}
	if recentChatFileDirectory(1, old, nil) != "" {
		t.Fatal("stale file outside recent window included")
	}
}

func TestChatFileContextFirstReadWinsOverGenericMemorySearch(t *testing.T) {
	setupAPITestDB(t)
	path := filepath.Join(t.TempDir(), "reference.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("参考正文", 1000)), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := memory.CreateFileAttachment(1, "reference.md", path, "", "text/markdown", 12000, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	tools := chatToolsForConversation(1)
	msgs := []ChatMessage{{Role: "user", Content: []ContentPart{{Type: "text", Text: "请读取这些文件附件。"}, {Type: "text", Text: attachmentDocumentText(a)}}}}
	if !hasCurrentChatFilePreview(msgs) || hasCurrentChatFilePreview([]ChatMessage{{Role: "user", Content: recentChatFileDirectory(1, nil, nil)}}) {
		t.Fatal("new upload classification failed")
	}
	original := callMemoryToolModel
	defer func() { callMemoryToolModel = original }()
	calls := 0
	callMemoryToolModel = func(_ context.Context, _, _, _ string, _ []ChatMessage, _ int, _ []Tool, choice interface{}, _ func(string) error, _ upstreamPriority) (ChatResult, error) {
		calls++
		raw, _ := json.Marshal(choice)
		if !strings.Contains(string(raw), `"read_chat_file"`) || strings.Contains(string(raw), `"search_memory"`) {
			t.Fatal("fresh file did not receive first-read tool choice")
		}
		return ChatResult{Content: "NO_TOOL"}, nil
	}
	_, err = collectToolSummaryWithModelChannel(modelChannelConfig{APIKey: "mock", Model: "mock"}, "请读取这些文件附件。", 1, msgs, tools, true, false, nil, nil, nil)
	if err != nil || calls != 1 {
		t.Fatal("unexpected decision retries or error")
	}
}

func TestChatFileContextDirectorySurvivesBudgetTrimming(t *testing.T) {
	t.Setenv("CONTEXT_TOKEN_BUDGET", "32768")
	msgs, tools := finalBudgetFixture(t, 36891, true)
	d := recentChatFileDirectory(1, []memory.Message{{ID: 7, ConversationID: 1, Role: "user", Attachments: []memory.Attachment{{ID: 23, ConversationID: 1, Kind: "file", OriginalName: "reference.md"}}}}, nil)
	msgs = append(msgs, ChatMessage{Role: "user", Content: d})
	fitted := fitFinalModelInput(finalBudgetContext(), msgs, tools, upstreamTool)
	if fitted[len(fitted)-1].Content != d || checkModelInputBudget(finalBudgetContext(), fitted, tools) != nil {
		t.Fatal("file reference lost during optional history trimming")
	}
}

func TestChatFileContextAttachmentOnlyHasNoInventedUserText(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("CONTEXT_TOKEN_BUDGET", "65536")
	path := filepath.Join(t.TempDir(), "reference.md")
	if err := os.WriteFile(path, []byte("完整的参考文件正文"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := memory.CreateFileAttachment(1, "reference.md", path, "", "text/markdown", 30, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []chatReq{{AttachmentIDs: []int64{a.ID}}, {Messages: []chatInputMessage{{Content: " ", AttachmentIDs: []int64{a.ID}}}}} {
		batch := normalizeChatInputMessages(req)
		if len(batch) != 1 || batch[0].Content != "" {
			t.Fatal("attachment-only input was discarded or given default text")
		}
	}
	if len(normalizeChatInputMessages(chatReq{})) != 0 {
		t.Fatal("empty input without attachment accepted")
	}
	_, id, err := saveChatInputMessages(1, normalizeChatInputMessages(chatReq{AttachmentIDs: []int64{a.ID}}))
	if err != nil {
		t.Fatal(err)
	}
	m, err := memory.GetMessage(1, id)
	if err != nil || m.Content != "" || len(m.Attachments) != 1 {
		t.Fatal("saved attachment-only input is not faithfully empty")
	}
	built, err := buildJSONContextMessagesWithChunks(1, "", nil, []memory.Attachment{a}, nil)
	if err != nil || !hasCompleteChatFileContext(built.Messages) {
		t.Fatal("attachment-only JSON context invalid")
	}
	var input map[string]interface{}
	if json.Unmarshal([]byte(built.Debug.FinalContextJSON.CurrentUserMessage), &input) != nil || input["attachment_only"] != true {
		t.Fatal("attachment-only model input lacks typed metadata")
	}
	if currentUserModelText("用户原话", []memory.Attachment{a}) != "用户原话" || currentUserModelText("", nil) != "" {
		t.Fatal("user words rewritten")
	}
}
