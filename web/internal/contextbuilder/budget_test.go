package contextbuilder

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLayerBudgetDropsSummariesBeforeEvidenceAndRecent(t *testing.T) {
	fixed := "固定人物设定和长期偏好"
	ctx := ContextJSON{CurrentUserMessage: "今天怎么样", RecentMessages: []RecentMessage{{Role: "user", Content: "最近原文"}}, RetrievedMemory: []RetrievedMemory{{MemoryID: 1, Content: "检索证据"}}, MemoryIndex: []MemoryIndexItem{{MemoryID: 2, Summary: strings.Repeat("历史摘要", 200)}}}
	withoutSummary := ctx
	withoutSummary.MemoryIndex = nil
	raw, _ := json.Marshal(withoutSummary)
	budget := EstimateTextTokens(fixed) + EstimateTextTokens(string(raw)) + 128
	if err := FitTokenBudget(&ctx, fixed, budget, 0); err != nil {
		t.Fatal(err)
	}
	if len(ctx.MemoryIndex) != 0 || len(ctx.RetrievedMemory) != 1 || len(ctx.RecentMessages) != 1 {
		t.Fatal("layer priority violated")
	}
	if ctx.CurrentUserMessage != "今天怎么样" {
		t.Fatal("current input changed")
	}
	if err := FitTokenBudget(&ctx, strings.Repeat("稳定人设", 200), 10, 0); err == nil {
		t.Fatal("fixed oversized prompt silently trimmed")
	}
}

func TestStablePrefixDoesNotIncludeDynamicRules(t *testing.T) {
	input := Input{CurrentUserMessage: "你好", BasePrompt: "固定基础", PersonaContent: "固定人设", DynamicRules: "任务到期A", TokenBudget: 5000}
	a, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	input.DynamicRules = "任务到期B"
	input.CurrentUserMessage = "其他请求"
	b, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if a.SystemPrompt != b.SystemPrompt || strings.Contains(a.SystemPrompt, "任务到期") {
		t.Fatal("unstable cache prefix")
	}
	if a.Context.DynamicRules == b.Context.DynamicRules {
		t.Fatal("dynamic rules lost")
	}
}
