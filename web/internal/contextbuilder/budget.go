package contextbuilder

import (
	"encoding/json"
	"fmt"
	"unicode"
)

const DefaultTokenBudget = 32768

// A conservative estimate, not provider-reported usage. Non-ASCII text is
// charged two tokens per rune; ASCII uses one per three characters.
func EstimateTextTokens(text string) int {
	ascii, other := 0, 0
	for _, r := range text {
		if r < unicode.MaxASCII {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+2)/3 + other*2
}

func FitTokenBudget(ctx *ContextJSON, system string, budget, reserved int) error {
	if budget <= 0 {
		budget = DefaultTokenBudget
	}
	used := func() int {
		raw, _ := json.Marshal(ctx)
		return EstimateTextTokens(system) + EstimateTextTokens(string(raw)) + reserved + 128
	}
	// Layer 4: older summaries/directories, preserving stable persona/profile.
	for used() > budget && len(ctx.MemoryIndex) > 0 {
		ctx.MemoryIndex = ctx.MemoryIndex[:len(ctx.MemoryIndex)-1]
	}
	for used() > budget && len(ctx.UploadedDocuments) > 0 {
		ctx.UploadedDocuments = ctx.UploadedDocuments[:len(ctx.UploadedDocuments)-1]
	}
	for used() > budget && len(ctx.ActiveMemory) > 0 {
		ctx.ActiveMemory = ctx.ActiveMemory[:len(ctx.ActiveMemory)-1]
	}
	for used() > budget && len(ctx.CoreMemory) > 0 {
		ctx.CoreMemory = ctx.CoreMemory[:len(ctx.CoreMemory)-1]
	}
	// Layer 3: lowest-ranked evidence goes first.
	for used() > budget && len(ctx.RetrievedMemory) > 0 {
		ctx.RetrievedMemory = ctx.RetrievedMemory[:len(ctx.RetrievedMemory)-1]
	}
	for used() > budget && len(ctx.RetrievedDocChunks) > 0 {
		ctx.RetrievedDocChunks = ctx.RetrievedDocChunks[:len(ctx.RetrievedDocChunks)-1]
	}
	// Layer 2: oldest raw turns are removed last.
	for used() > budget && len(ctx.RecentMessages) > 0 {
		ctx.RecentMessages = ctx.RecentMessages[1:]
	}
	if used() > budget {
		return fmt.Errorf("fixed context, current message and reserved input exceed CONTEXT_TOKEN_BUDGET (%d)", budget)
	}
	return nil
}
