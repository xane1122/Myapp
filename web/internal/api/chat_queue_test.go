package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/memory"
)

func TestNormalizeChatInputMessagesKeepsSingleMessageCompatibility(t *testing.T) {
	got := normalizeChatInputMessages(chatReq{Message: "  hello  ", AttachmentIDs: []int64{7}})
	if len(got) != 1 || got[0].Content != "hello" || len(got[0].AttachmentIDs) != 1 || got[0].AttachmentIDs[0] != 7 {
		t.Fatalf("normalized input = %#v", got)
	}
	if combinedChatInput(got) != "hello" {
		t.Fatalf("single input was unexpectedly wrapped: %q", combinedChatInput(got))
	}
}

func TestCombinedChatInputIncludesEveryQueuedMessageOnce(t *testing.T) {
	batch := normalizeChatInputMessages(chatReq{Messages: []chatInputMessage{
		{Content: "消息 A", AttachmentIDs: []int64{11}},
		{Content: "消息 B", AttachmentIDs: []int64{22, 23}},
	}})
	got := combinedChatInput(batch)
	first := "消息 1：消息 A"
	second := "消息 2：消息 B"
	if !strings.Contains(got, "一条回复中逐项回应") {
		t.Fatalf("combined input is missing the batch instruction: %q", got)
	}
	if strings.Count(got, first) != 1 || strings.Count(got, second) != 1 {
		t.Fatalf("combined input must retain each message exactly once: %q", got)
	}
	if strings.Index(got, first) >= strings.Index(got, second) {
		t.Fatalf("combined input changed message order: %q", got)
	}
	if ids := flattenChatInputAttachmentIDs(batch); len(ids) != 3 || ids[0] != 11 || ids[1] != 22 || ids[2] != 23 {
		t.Fatalf("flattened attachment IDs = %#v", ids)
	}
}

func TestSaveChatInputMessagesKeepsAttachmentsWithTheirUserMessage(t *testing.T) {
	setupAPITestDB(t)
	conversationID, err := memory.EnsureConversation(0)
	if err != nil {
		t.Fatal(err)
	}
	firstAttachment, err := memory.CreateAttachment(conversationID, "a.png", filepath.Join(t.TempDir(), "a.png"), "/uploads/a.png", "image/png", 1)
	if err != nil {
		t.Fatal(err)
	}
	secondAttachment, err := memory.CreateAttachment(conversationID, "b.png", filepath.Join(t.TempDir(), "b.png"), "/uploads/b.png", "image/png", 1)
	if err != nil {
		t.Fatal(err)
	}

	ids, lastID, err := saveChatInputMessages(conversationID, []chatInputMessage{
		{Content: "消息 A", AttachmentIDs: []int64{firstAttachment.ID}},
		{Content: "消息 B", AttachmentIDs: []int64{secondAttachment.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || lastID != ids[1] {
		t.Fatalf("saved message IDs = %#v, last = %d", ids, lastID)
	}
	firstMessage, err := memory.GetMessage(conversationID, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	secondMessage, err := memory.GetMessage(conversationID, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(firstMessage.Attachments) != 1 || firstMessage.Attachments[0].ID != firstAttachment.ID {
		t.Fatalf("first message attachments = %#v", firstMessage.Attachments)
	}
	if len(secondMessage.Attachments) != 1 || secondMessage.Attachments[0].ID != secondAttachment.ID {
		t.Fatalf("second message attachments = %#v", secondMessage.Attachments)
	}
}

func TestChatFrontendQueuesConsecutiveMessagesIntoOneRequest(t *testing.T) {
	source, err := os.ReadFile("../../static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"const CHAT_BATCH_WINDOW_MS=0",
		"batch.entries.push(entry)",
		"messages:payloadMessages",
		"user_message_ids",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("chat frontend is missing batching contract %q", want)
		}
	}
	start := strings.Index(text, "async function flushChatBatch(batch)")
	end := strings.Index(text[start:], "\nfunction send()")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate flushChatBatch frontend implementation")
	}
	flushSource := text[start : start+end]
	if got := strings.Count(flushSource, "fetch('/api/chat'"); got != 1 {
		t.Fatalf("flushChatBatch issues %d chat requests, want exactly 1", got)
	}
}
