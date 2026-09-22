package api

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func TestMemoryWorkerAssessmentReceivesCompleteContext(t *testing.T) {
	setupAPITestDB(t)
	if err := memory.SaveChunkWithMetadata(1, memory.ScopeGlobal, "manual", sql.NullInt64{}, "用户不喝咖啡。", "用户不喝咖啡。", "饮品偏好,咖啡,不喝", memory.ChunkMetadata{TopicLabel: "饮品偏好", Importance: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(1, "user", "请别给我推荐咖啡"); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.SaveMessage(1, "assistant", "好，我会避开咖啡。"); err != nil {
		t.Fatal(err)
	}
	originalCall := callMemoryAssessmentModel
	t.Cleanup(func() { callMemoryAssessmentModel = originalCall })
	callMemoryAssessmentModel = func(_, _, _ string, messages []ChatMessage, _ int) (string, error) {
		var contextBuilder strings.Builder
		for _, message := range messages {
			contextBuilder.WriteString(message.Content.(string))
			contextBuilder.WriteByte('\n')
		}
		contextText := contextBuilder.String()
		for _, field := range []string{"context_json", `"memory_index"`, `"active_memory"`, `"core_memory"`, `"user_profile"`, `"uploaded_documents"`, `"conversation_state"`} {
			if !strings.Contains(contextText, field) {
				t.Fatalf("memory worker context missing %s: %s", field, contextText)
			}
		}
		if !strings.Contains(contextText, "用户不喝咖啡") {
			t.Fatalf("memory worker did not receive existing global memory: %s", contextText)
		}
		return `{"memories":[]}`, nil
	}
	assessment, err := assessGlobalMemoryTurns(1, "key", "https://memory.example/v1", "memory-model", []queuedGlobalMemoryTurn{{ConversationID: 1, UserMessage: "我最近改喝茶了", AssistantMessage: "知道了"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(assessment.Memories) != 0 {
		t.Fatalf("unexpected memories: %#v", assessment.Memories)
	}
}

func TestExplicitlyRequestsMemory(t *testing.T) {
	for _, text := range []string{"记住这个", "请记住：我不喝咖啡", "这件事帮我记住"} {
		if !explicitlyRequestsMemory(text) {
			t.Fatalf("expected explicit memory request for %q", text)
		}
	}
	if explicitlyRequestsMemory("你还记住这个吗") {
		t.Fatal("a recall question should not be treated as a write request")
	}
}

func TestParseGlobalMemoryAssessmentFiltersInvalidEntries(t *testing.T) {
	raw := `{"memories":[
		{"summary":"用户纠正：不喝咖啡","category":"correction","keywords":["咖啡"],"emotion":"angry","correction":"从喝咖啡改为不喝"},
		{"summary":"普通闲聊","category":"small_talk","keywords":[]},
		{"summary":"   ","category":"important_event","keywords":[]}
	]}`
	result, err := parseGlobalMemoryAssessment(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Memories) != 1 || result.Memories[0].Category != "correction" {
		t.Fatalf("unexpected filtered assessment: %#v", result)
	}
}

func TestSaveGlobalMemoryCandidateDeduplicates(t *testing.T) {
	setupAPITestDB(t)
	candidate := globalMemoryCandidate{
		Summary:    "用户不喝咖啡。",
		Category:   "correction",
		Keywords:   []string{"咖啡", "不喝"},
		Emotion:    "neutral",
		Correction: "从喝咖啡改为不喝咖啡",
	}
	if err := saveGlobalMemoryCandidate(1, candidate, "2026-07-19 10:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := saveGlobalMemoryCandidate(1, candidate, "2026-07-19 10:00:00"); err != nil {
		t.Fatal(err)
	}
	chunks, err := memory.ListChunksByScope(1, memory.ScopeGlobal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one deduplicated global memory, got %#v", chunks)
	}
	if chunks[0].SourceType != automaticGlobalMemorySource || !chunks[0].IsCorrection || chunks[0].Importance != "high" {
		t.Fatalf("unexpected global memory metadata: %#v", chunks[0])
	}
}

func TestMemoryTextSimilarityUsesFirstHundredRunes(t *testing.T) {
	common := strings.Repeat("相同内容", 25)
	if got := memoryTextSimilarity(common+"甲", common+"乙"); got < 0.80 {
		t.Fatalf("similarity = %v, want >= 0.80", got)
	}
	if got := memoryTextSimilarity("用户喜欢绿茶", "用户讨厌下雨"); got >= 0.80 {
		t.Fatalf("unrelated similarity = %v", got)
	}
}

func TestForcedMemorySurvivesAssessmentFailure(t *testing.T) {
	setupAPITestDB(t)
	err := assessAndSaveGlobalMemory(1, "", "", cheapGlobalMemoryModel(), "请记住：我不喜欢被反问。", "我记住了。", "2026-07-19 10:00:00")
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := memory.ListChunksByScope(1, memory.ScopeGlobal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "请记住：我不喜欢被反问。" {
		t.Fatalf("forced memory was not persisted: %#v", chunks)
	}
}

func TestExplicitMemorySkipsCandidateQueue(t *testing.T) {
	setupAPITestDB(t)
	if err := queueAndMaybeProcessGlobalMemory(1, "", "", "test-model", "请记住：我不喝咖啡。", "记住了。", "2026-07-20 10:00:00"); err != nil {
		t.Fatal(err)
	}
	var candidates int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM global_memory_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("explicit memory queued %d candidates, want 0", candidates)
	}
	chunks, err := memory.ListChunksByScope(1, memory.ScopeGlobal, 10)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("explicit memory chunks=%#v err=%v", chunks, err)
	}
}

func TestMemoryTurnQueueStoresOneIndependentTask(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(memoryChannel, modelChannelInput{Model: modelChannelString("memory-model"), APIKey: modelChannelString("memory-key")}); err != nil {
		t.Fatal(err)
	}
	enqueueMemoryTurn(1, "普通消息", "普通回复")
	var pending, attempts int
	var storedModel string
	if err := db.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(attempts),0),COALESCE(MAX(model),'') FROM global_memory_candidates WHERE status='pending'`).Scan(&pending, &attempts, &storedModel); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 0 || storedModel != "memory-model" {
		t.Fatalf("pending=%d attempts=%d model=%q", pending, attempts, storedModel)
	}
}

func TestGlobalMemoryBatchModelsAreIndependentFromChatModel(t *testing.T) {
	t.Setenv("GLOBAL_MEMORY_CHEAP_MODEL", "memory-model")
	t.Setenv("CHEAP_MODEL", "cheap-model")
	t.Setenv("GLOBAL_MEMORY_RESCAN_MODEL", "strong-model")
	models := globalMemoryBatchModels("chat-model")
	want := []string{"memory-model", "cheap-model", "strong-model", "chat-model"}
	if strings.Join(models, ",") != strings.Join(want, ",") {
		t.Fatalf("models=%q want=%q", models, want)
	}
}

func TestGlobalMemoryBatchModelsDeduplicateFallbacks(t *testing.T) {
	t.Setenv("GLOBAL_MEMORY_CHEAP_MODEL", "same-model")
	t.Setenv("CHEAP_MODEL", " SAME-MODEL ")
	t.Setenv("GLOBAL_MEMORY_RESCAN_MODEL", "same-model")
	models := globalMemoryBatchModels("other-model")
	want := []string{"same-model", "other-model"}
	if strings.Join(models, ",") != strings.Join(want, ",") {
		t.Fatalf("models=%q want=%q", models, want)
	}
}

func TestFailedGlobalMemoryBatchRemainsPending(t *testing.T) {
	setupAPITestDB(t)
	for i := 0; i < globalMemoryBatchSize; i++ {
		if _, err := db.DB.Exec(`INSERT INTO global_memory_candidates(conversation_id,user_message,assistant_message) VALUES(1,'用户','助手')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := processGlobalMemoryCandidateBatch(1, "", "", false); err == nil {
		t.Fatal("expected missing API key error")
	}
	var pending, attempted int
	if err := db.DB.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN attempts=1 AND last_error<>'' THEN 1 ELSE 0 END) FROM global_memory_candidates WHERE status='pending'`).Scan(&pending, &attempted); err != nil {
		t.Fatal(err)
	}
	if pending != globalMemoryBatchSize || attempted != globalMemoryBatchSize {
		t.Fatalf("pending=%d attempted=%d", pending, attempted)
	}
}

func TestGlobalMemoryBatchFailureBacksOffThenDeadLetters(t *testing.T) {
	setupAPITestDB(t)
	result, err := db.DB.Exec(`INSERT INTO global_memory_candidates
		(conversation_id,user_message,assistant_message,status,attempts) VALUES(1,'用户','助手','processing',1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	markGlobalMemoryBatchFailure([]interface{}{id}, []string{"?"}, errors.New("failure"))
	var status string
	var nextAttempt, failedAt interface{}
	if err := db.DB.QueryRow(`SELECT status,next_attempt_at,failed_at FROM global_memory_candidates WHERE id=?`, id).Scan(&status, &nextAttempt, &failedAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || nextAttempt == nil || failedAt != nil {
		t.Fatalf("status=%q next=%v failed=%v", status, nextAttempt, failedAt)
	}
	if _, err := db.DB.Exec(`UPDATE global_memory_candidates SET status='processing',attempts=? WHERE id=?`, globalMemoryMaxAttempts, id); err != nil {
		t.Fatal(err)
	}
	markGlobalMemoryBatchFailure([]interface{}{id}, []string{"?"}, errors.New("permanent failure"))
	if err := db.DB.QueryRow(`SELECT status,next_attempt_at,failed_at FROM global_memory_candidates WHERE id=?`, id).Scan(&status, &nextAttempt, &failedAt); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || nextAttempt != nil || failedAt == nil {
		t.Fatalf("status=%q next=%v failed=%v", status, nextAttempt, failedAt)
	}
}

func TestCorrectionSupersedesActiveGlobalMemory(t *testing.T) {
	setupAPITestDB(t)
	old := globalMemoryCandidate{Summary: "用户喜欢咖啡。", Category: "preference_boundary", TopicLabel: "饮品偏好"}
	if err := saveGlobalMemoryCandidate(1, old, "2026-07-19 10:00:00"); err != nil {
		t.Fatal(err)
	}
	correction := globalMemoryCandidate{Summary: "用户现在不喝咖啡。", Category: "correction", TopicLabel: "饮品偏好", Correction: "喜欢咖啡改为不喝咖啡"}
	if err := saveGlobalMemoryCandidate(1, correction, "2026-07-20 10:00:00"); err != nil {
		t.Fatal(err)
	}
	chunks, err := memory.ListChunksByScope(1, memory.ScopeGlobal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != correction.Summary || !chunks[0].Active {
		t.Fatalf("active chunks=%#v", chunks)
	}
	var inactive, superseded int
	if err := db.DB.QueryRow(`SELECT COUNT(*),COUNT(superseded_by) FROM memory_chunks WHERE active=0`).Scan(&inactive, &superseded); err != nil {
		t.Fatal(err)
	}
	if inactive != 1 || superseded != 1 {
		t.Fatalf("inactive=%d superseded=%d", inactive, superseded)
	}
}
