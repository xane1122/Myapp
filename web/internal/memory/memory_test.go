package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"myapp/internal/db"
)

func TestSaveMessageReplyingToPersistsValidatedSnapshot(t *testing.T) {
	setupTestDB(t)
	originalID, err := SaveMessage(1, "assistant", `{"reply":"原回答"}`)
	if err != nil {
		t.Fatal(err)
	}
	replyID, err := SaveMessageReplyingTo(1, "user", "继续说", originalID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetMessage(1, replyID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplyToMessageID != originalID || got.QuoteRole != "assistant" || got.QuoteText != `{"reply":"原回答"}` {
		t.Fatalf("unexpected quote metadata: %#v", got)
	}
	if _, err := SaveMessageReplyingTo(2, "user", "跨会话", originalID); err == nil {
		t.Fatal("expected a cross-conversation quote to be rejected")
	}
}

func TestCreateVirtualTransferIdempotentReturnsOriginal(t *testing.T) {
	setupTestDB(t)
	first, err := CreateVirtualTransferIdempotent(1, "user", 5200, "测试", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateVirtualTransferIdempotent(1, "user", 5200, "测试", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.MessageID != first.MessageID {
		t.Fatalf("duplicate request created another transfer: first=%#v second=%#v", first, second)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM virtual_transfers`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one transfer, got %d", count)
	}
}

func TestSearchMessagesRanksSpecificMatchesAboveEarlierGenericTerms(t *testing.T) {
	setupTestDB(t)
	wantID, err := SaveMessage(1, "user", "昨天写的神父角色扮演，真是美味之")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := SaveMessage(1, "user", fmt.Sprintf("昨天的普通闲聊 %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := SearchMessages(1, "昨天 神父 角色扮演 美味之", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != wantID {
		t.Fatalf("specific multi-keyword match should rank first, got %#v", got)
	}
}

func TestUpdateStickerMetadata(t *testing.T) {
	setupTestDB(t)

	sticker, err := CreateSticker("旧名字", "uploads/stickers/a.png", "/uploads/stickers/a.png", "image/png", "旧标签", "旧描述", "neutral", true)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateStickerMetadata(sticker.ID, "抱抱", "安慰,陪你", "适合难过时使用", "sad")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "抱抱" || updated.Tags != "安慰,陪你" || updated.Description != "适合难过时使用" || updated.Mood != "sad" {
		t.Fatalf("unexpected sticker metadata: %#v", updated)
	}
	if updated.NeedsReview {
		t.Fatal("expected manual update to clear needs_review")
	}
}

func TestDisableStickerHidesItFromSearch(t *testing.T) {
	setupTestDB(t)

	sticker, err := CreateSticker("抱抱", "uploads/stickers/hug.png", "/uploads/stickers/hug.png", "image/png", "安慰,抱抱", "适合安慰", "comfort", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := DisableSticker(sticker.ID); err != nil {
		t.Fatal(err)
	}
	got, err := SearchStickers("抱抱", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected disabled sticker to be hidden, got %#v", got)
	}
}

func TestListStickersShowsNewestFirstEvenWhenNeedsReview(t *testing.T) {
	setupTestDB(t)

	oldSticker, err := CreateSticker("已确认", "uploads/stickers/old.png", "/uploads/stickers/old.png", "image/png", "确认", "已整理", "neutral", false)
	if err != nil {
		t.Fatal(err)
	}
	newSticker, err := CreateSticker("待确认", "uploads/stickers/new.png", "/uploads/stickers/new.png", "image/png", "待确认", "刚上传", "neutral", true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ListStickers()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("expected at least 2 stickers, got %#v", got)
	}
	if got[0].ID != newSticker.ID || got[1].ID != oldSticker.ID {
		t.Fatalf("expected newest sticker first, got %#v", got[:2])
	}
}

func TestListUpdateDeleteChunks(t *testing.T) {
	setupTestDB(t)

	if err := SaveChunk(1, ScopeConversation, "manual", noSourceDoc(), "旧内容", "旧摘要", "旧关键词"); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(1, ScopeConversation, "manual", noSourceDoc(), "新内容", "新摘要", "新关键词"); err != nil {
		t.Fatal(err)
	}
	got, err := ListChunks(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "新内容" || got[1].Content != "旧内容" {
		t.Fatalf("expected newest chunks first, got %#v", got)
	}
	updated, err := UpdateChunk(got[0].ID, "改后内容", "改后摘要", "改后关键词")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "改后内容" || updated.Summary != "改后摘要" || updated.Keywords != "改后关键词" {
		t.Fatalf("unexpected updated chunk: %#v", updated)
	}
	if err := DeleteChunk(updated.ID); err != nil {
		t.Fatal(err)
	}
	got, err = ListChunks(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "旧内容" {
		t.Fatalf("expected only old chunk after delete, got %#v", got)
	}
}

func TestListChunksByScopeFiltersGlobalAndCurrentConversation(t *testing.T) {
	setupTestDB(t)

	if _, err := EnsureConversation(2); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(1, ScopeGlobal, "manual", noSourceDoc(), "全局内容", "全局摘要", "全局"); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(1, ScopeConversation, "manual", noSourceDoc(), "会话1内容", "会话1摘要", "会话1"); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(2, ScopeConversation, "manual", noSourceDoc(), "会话2内容", "会话2摘要", "会话2"); err != nil {
		t.Fatal(err)
	}

	global, err := ListChunksByScope(1, ScopeGlobal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(global) != 1 || global[0].Content != "全局内容" {
		t.Fatalf("expected only global chunks, got %#v", global)
	}

	current, err := ListChunksByScope(1, ScopeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0].Content != "会话1内容" {
		t.Fatalf("expected only conversation 1 chunks, got %#v", current)
	}

	combined, err := ListChunksByScope(1, "all", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(combined) != 2 {
		t.Fatalf("expected global plus current conversation chunks, got %#v", combined)
	}
}

func TestDeleteConversationRemovesShellButKeepsGlobalMemory(t *testing.T) {
	setupTestDB(t)

	if _, err := EnsureConversation(2); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMessage(2, "user", "会话2消息"); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(2, ScopeConversation, "manual", noSourceDoc(), "会话2记忆", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(1, ScopeGlobal, "manual", noSourceDoc(), "全局记忆", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SyncConversationJSONL(2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO tool_activity(conversation_id,tool_name,status) VALUES(2,'search_memory','completed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ConversationJSONLPath(2)); err != nil {
		t.Fatal(err)
	}

	if err := DeleteConversation(2); err != nil {
		t.Fatal(err)
	}
	if _, err := GetConversation(2); err != sql.ErrNoRows {
		t.Fatalf("expected conversation deleted, got %v", err)
	}
	if count, err := CountMessages(2); err != nil || count != 0 {
		t.Fatalf("messages after delete = %d err=%v", count, err)
	}
	conversationChunks, err := ListChunksByScope(2, ScopeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversationChunks) != 0 {
		t.Fatalf("expected conversation chunks deleted, got %#v", conversationChunks)
	}
	var toolActivityCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM tool_activity WHERE conversation_id=2`).Scan(&toolActivityCount); err != nil || toolActivityCount != 0 {
		t.Fatalf("tool activity after delete = %d err=%v", toolActivityCount, err)
	}
	globalChunks, err := ListChunksByScope(1, ScopeGlobal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(globalChunks) != 1 || globalChunks[0].Content != "全局记忆" {
		t.Fatalf("global memory should remain, got %#v", globalChunks)
	}
	if _, err := os.Stat(ConversationJSONLPath(2)); !os.IsNotExist(err) {
		t.Fatalf("expected JSONL removed, got %v", err)
	}
}

func TestMemoryArchiveKeepsFullHistory(t *testing.T) {
	setupTestDB(t)

	var archivedIDs []int64
	for i := 1; i <= 10; i++ {
		id, err := SaveMessage(1, "user", fmt.Sprintf("消息%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i <= 3 {
			archivedIDs = append(archivedIDs, id)
		}
	}
	oldest, err := GetUnarchivedHistory(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldest) != 3 || oldest[0].Content != "消息1" || oldest[2].Content != "消息3" {
		t.Fatalf("unexpected unarchived oldest messages: %#v", oldest)
	}
	if err := MarkMessagesMemoryArchived(1, archivedIDs); err != nil {
		t.Fatal(err)
	}
	full, err := GetHistory(1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 10 {
		t.Fatalf("archive marker should keep full history, got %d messages", len(full))
	}
	count, err := CountUnarchivedMessages(1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("unarchived count = %d, want 7", count)
	}
	next, err := GetUnarchivedHistory(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 3 || next[0].Content != "消息4" {
		t.Fatalf("expected archive marker to advance cursor, got %#v", next)
	}
}

func TestConversationChunkEditRequiresVisibleScope(t *testing.T) {
	setupTestDB(t)

	if _, err := EnsureConversation(2); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(1, ScopeConversation, "manual", noSourceDoc(), "会话1内容", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(2, ScopeConversation, "manual", noSourceDoc(), "会话2内容", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(1, ScopeGlobal, "manual", noSourceDoc(), "全局内容", "", ""); err != nil {
		t.Fatal(err)
	}
	conversationOne, err := ListChunksByScope(1, ScopeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	conversationTwo, err := ListChunksByScope(2, ScopeConversation, 10)
	if err != nil {
		t.Fatal(err)
	}
	global, err := ListChunksByScope(2, ScopeGlobal, 10)
	if err != nil {
		t.Fatal(err)
	}
	conversationOneID := conversationOne[0].ID
	conversationTwoID := conversationTwo[0].ID
	globalID := global[0].ID
	if _, err := UpdateChunkForConversation(2, conversationOneID, "误改", "", ""); err != sql.ErrNoRows {
		t.Fatalf("expected cross-conversation update to be blocked, got %v", err)
	}
	if _, err := UpdateChunkForConversation(2, conversationTwoID, "会话2已改", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateChunkForConversation(2, globalID, "全局已改", "", ""); err != nil {
		t.Fatal(err)
	}
	blocked, err := GetChunk(conversationOneID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Content != "会话1内容" {
		t.Fatalf("cross-conversation chunk was changed: %#v", blocked)
	}
	if err := DeleteChunkForConversation(2, conversationOneID); err != sql.ErrNoRows {
		t.Fatalf("expected cross-conversation delete to be blocked, got %v", err)
	}
	if err := DeleteChunkForConversation(2, conversationTwoID); err != nil {
		t.Fatal(err)
	}
	if _, err := GetChunk(conversationTwoID); err != sql.ErrNoRows {
		t.Fatalf("expected conversation 2 chunk to be deleted, got %v", err)
	}
}

func TestFindDuplicateChunkIgnoresWhitespaceAndPunctuation(t *testing.T) {
	setupTestDB(t)
	if err := SaveChunk(1, ScopeGlobal, "manual", noSourceDoc(), "用户喜欢：绿茶。", "", ""); err != nil {
		t.Fatal(err)
	}
	existing, duplicate, err := FindDuplicateChunk(1, ScopeGlobal, "manual", " 用户喜欢绿茶 ")
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate || existing.ID == 0 {
		t.Fatalf("expected punctuation-insensitive duplicate, got duplicate=%v chunk=%#v", duplicate, existing)
	}
}

func TestPersonaCreateDedupesAndActivationCanBeCleared(t *testing.T) {
	setupTestDB(t)

	first, err := CreatePersona("人物设定", "同一份 人设", "", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreatePersona("人物设定", "同一份\n\n人设", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected duplicate create to reuse existing persona id %d, got %d", first.ID, second.ID)
	}
	updated, err := GetPersona(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "同一份\n\n人设" {
		t.Fatalf("expected same-title persona save to replace content, got %q", updated.Content)
	}
	items, err := ListPersonas()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected deduped persona list, got %#v", items)
	}
	if _, ok, err := GetActivePersona(); err != nil || !ok {
		t.Fatalf("expected active persona, ok=%v err=%v", ok, err)
	}
	if err := DeactivatePersona(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := GetActivePersona(); err != nil || ok {
		t.Fatalf("expected no active persona after deactivate, ok=%v err=%v", ok, err)
	}
}

func TestDeletePersonaRemovesIt(t *testing.T) {
	setupTestDB(t)

	p, err := CreatePersona("临时人设", "马上删除", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeletePersona(p.ID); err != nil {
		t.Fatal(err)
	}
	items, err := ListPersonas()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected deleted persona to disappear, got %#v", items)
	}
}

func TestDeletePersonaRemovesSameTitleGroup(t *testing.T) {
	setupTestDB(t)

	if _, err := db.DB.Exec(
		`INSERT INTO persona_profiles (title, content, active) VALUES (?, ?, ?), (?, ?, ?)`,
		"人物设定", "旧内容", 0,
		"人物设定", "新内容", 1,
	); err != nil {
		t.Fatal(err)
	}
	items, err := ListPersonas()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected duplicate title group to render once, got %#v", items)
	}
	if err := DeletePersona(items[0].ID); err != nil {
		t.Fatal(err)
	}
	items, err = ListPersonas()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected whole persona title group to be deleted, got %#v", items)
	}
}

func TestSyncConversationJSONLWritesConversationAndMessages(t *testing.T) {
	setupTestDB(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	c, err := CreateConversation("测试会话")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMessage(c.ID, "user", "你好"); err != nil {
		t.Fatal(err)
	}
	if err := SyncConversationJSONL(c.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ConversationJSONLPath(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `+08:00`) {
		t.Fatalf("conversation JSONL timestamps are not Beijing RFC3339: %s", raw)
	}
	text := string(raw)
	for _, want := range []string{`"type":"conversation"`, `"title":"测试会话"`, `"type":"message"`, `"content":"你好"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("jsonl missing %q: %s", want, text)
		}
	}
}

func noSourceDoc() sql.NullInt64 {
	return sql.NullInt64{}
}

func TestStructuredMemoryCorrectionAndSearchPriority(t *testing.T) {
	setupTestDB(t)
	if err := SaveChunkWithMetadata(1, ScopeConversation, "conversation", noSourceDoc(), "用户纠正：不喜欢咖啡", "用户不喜欢咖啡", "咖啡,不喜欢", ChunkMetadata{TopicLabel: "饮品偏好", Emotion: "neutral", Correction: "不是喜欢咖啡，而是不喜欢", IsCorrection: true, TimeStart: "2026-07-01 10:00:00", TimeEnd: "2026-07-01 10:05:00"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunkWithMetadata(1, ScopeConversation, "conversation", noSourceDoc(), "用户以前喜欢咖啡", "用户喜欢咖啡", "咖啡,喜欢", ChunkMetadata{TopicLabel: "饮品偏好", TimeStart: "2026-07-01 10:01:00", TimeEnd: "2026-07-01 10:04:00"}); err != nil {
		t.Fatal(err)
	}
	items, err := SearchChunks(1, "咖啡", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].IsCorrection {
		t.Fatalf("expected correction first, got %#v", items)
	}
	if err := NormalizeCorrections(1); err != nil {
		t.Fatal(err)
	}
}

func TestSearchChunksUsesRecentConversationAndGlobalScope(t *testing.T) {
	setupTestDB(t)
	other, err := CreateConversation("other")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveChunkWithMetadata(1, ScopeGlobal, "conversation", noSourceDoc(), "用户的猫咪叫团子", "猫咪名字是团子", "猫咪,团子", ChunkMetadata{TopicLabel: "宠物信息", Importance: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMessage(other.ID, "user", "刚才说到猫咪团子最近不爱吃饭"); err != nil {
		t.Fatal(err)
	}
	items, err := SearchChunks(other.ID, "那个情况怎么样", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].Scope != ScopeGlobal || items[0].TopicLabel != "宠物信息" {
		t.Fatalf("recent-context global recall failed: %#v", items)
	}
}

func TestSearchChunksByQueryOnlyRejectsUnrelatedCorrection(t *testing.T) {
	setupTestDB(t)
	if err := SaveChunkWithMetadata(1, ScopeGlobal, "conversation", noSourceDoc(), "校园场景必须使用第二人称。", "纠正校园场景的叙述视角。", "校园,视角纠正", ChunkMetadata{Importance: "high", IsCorrection: true}); err != nil {
		t.Fatal(err)
	}
	if err := SaveChunkWithMetadata(1, ScopeGlobal, "auto_global", noSourceDoc(), "用户有双相情感障碍。", "用户患有双相情感障碍。", "健康,疾病,诊断,双相情感障碍", ChunkMetadata{Importance: "high"}); err != nil {
		t.Fatal(err)
	}
	items, err := SearchChunksByQueryOnly(1, "健康状况 疾病 病史 诊断 双相 情感障碍 心理问题 自残 治疗 用药", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || !strings.Contains(items[0].Summary, "双相情感障碍") {
		t.Fatalf("direct health fact was not ranked first: %#v", items)
	}
	for _, item := range items {
		if item.IsCorrection {
			t.Fatalf("unrelated correction leaked into query-only results: %#v", item)
		}
	}
}

func TestSearchChunksExplicitQueryRejectsRecentContextOnlyMatches(t *testing.T) {
	setupTestDB(t)
	if err := SaveChunkWithMetadata(1, ScopeGlobal, "conversation", noSourceDoc(), "世界书框架和记忆标签", "讨论世界书框架", "世界书,记忆标签", ChunkMetadata{Importance: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMessage(1, "user", "刚才还在讨论世界书和记忆标签"); err != nil {
		t.Fatal(err)
	}
	items, err := SearchChunks(1, "0.02 0.05 token价格 上下文cache 不安心", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("recent context alone must not satisfy an explicit query: %#v", items)
	}
}

func TestArchiveInactiveConversationKeepsActive(t *testing.T) {
	setupTestDB(t)
	other, err := CreateConversation("old")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := SaveMessage(other.ID, "user", fmt.Sprintf("old-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SaveMessage(1, "user", "active"); err != nil {
		t.Fatal(err)
	}
	if err := ArchiveInactiveConversations(1, 1); err != nil {
		t.Fatal(err)
	}
	var live, archived int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=?`, other.ID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM messages_archive WHERE conversation_id=?`, other.ID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if live != 0 || archived != 3 {
		t.Fatalf("live=%d archived=%d", live, archived)
	}
	history, err := GetHistory(other.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history=%d", len(history))
	}
}

func TestAttachmentHashReuseAndOrphanCleanup(t *testing.T) {
	setupTestDB(t)
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := CreateAttachmentWithHash(1, "image.png", path, "/uploads/image.png", "image/png", 3, "same-hash")
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := FindReusableAttachment("same-hash")
	if err != nil || !ok || found.ID != a.ID {
		t.Fatalf("found=%#v ok=%v err=%v", found, ok, err)
	}
	if _, err := db.DB.Exec(`UPDATE message_attachments SET created_at='2020-01-01 00:00:00' WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := CleanupOrphanAttachments(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed=%v", removed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan file still exists: %v", err)
	}
}

func TestRegenerateHistoryAndReplaceAssistant(t *testing.T) {
	setupTestDB(t)
	firstUser, err := SaveMessage(1, "user", "问题 A")
	if err != nil {
		t.Fatal(err)
	}
	firstAssistant, err := SaveMessage(1, "assistant", "回答 A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMessage(1, "user", "问题 B"); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveMessage(1, "assistant", "回答 B"); err != nil {
		t.Fatal(err)
	}

	before, err := GetHistoryBefore(1, firstAssistant, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].ID != firstUser || before[0].Content != "问题 A" {
		t.Fatalf("unexpected regeneration history: %#v", before)
	}
	if err := UpdateMessageContent(1, firstAssistant, "assistant", "回答 A（新版）"); err != nil {
		t.Fatal(err)
	}
	target, err := GetMessage(1, firstAssistant)
	if err != nil {
		t.Fatal(err)
	}
	if target.Content != "回答 A（新版）" {
		t.Fatalf("assistant content was not replaced: %#v", target)
	}
	history, err := GetHistory(1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("regeneration must not append duplicate messages, got %d", len(history))
	}
}

func TestFindDuplicateChunkRespectsConversationScope(t *testing.T) {
	setupTestDB(t)
	first, err := EnsureConversation(0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateConversation("second")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveChunk(first, ScopeConversation, "manual", sql.NullInt64{}, "用户喜欢绿茶", "偏好", "绿茶"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := FindDuplicateChunk(first, ScopeConversation, "manual", "  用户喜欢绿茶  "); err != nil || !found {
		t.Fatalf("expected duplicate: found=%v err=%v", found, err)
	}
	if _, found, err := FindDuplicateChunk(second.ID, ScopeConversation, "manual", "用户喜欢绿茶"); err != nil || found {
		t.Fatalf("conversation memory leaked: found=%v err=%v", found, err)
	}
}

func setupTestDB(t *testing.T) {
	t.Helper()
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() {
		if db.DB != nil {
			_ = db.DB.Close()
			db.DB = nil
		}
	})
}
