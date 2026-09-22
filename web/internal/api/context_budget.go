package api

import (
	"context"
	"encoding/json"
	"fmt"
	"myapp/internal/contextbuilder"
	"myapp/internal/memory"
	"os"
	"strconv"
	"strings"
)

func contextTokenBudget() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CONTEXT_TOKEN_BUDGET"))); err == nil && n > 0 {
		return n
	}
	return contextbuilder.DefaultTokenBudget
}

func estimateModelInput(messages []ChatMessage, tools []Tool) int {
	cost := 64
	for _, m := range messages {
		cost += 8
		switch c := m.Content.(type) {
		case string:
			cost += contextbuilder.EstimateTextTokens(c)
		case []ContentPart:
			for _, p := range c {
				if p.Type == "image_url" {
					cost += 4096
				} else {
					cost += contextbuilder.EstimateTextTokens(p.Text)
				}
			}
		default:
			raw, _ := json.Marshal(c)
			cost += contextbuilder.EstimateTextTokens(string(raw))
		}
		raw, _ := json.Marshal(m.ToolCalls)
		cost += contextbuilder.EstimateTextTokens(string(raw)) + contextbuilder.EstimateTextTokens(m.ToolCallID)
	}
	raw, _ := json.Marshal(tools)
	return cost + contextbuilder.EstimateTextTokens(string(raw))
}

func checkModelInputBudget(ctx context.Context, messages []ChatMessage, tools []Tool) error {
	if _, ok := ctx.Value(statsContextKey{}).(*chatStats); !ok {
		return nil
	}
	if used := estimateModelInput(messages, tools); used > contextTokenBudget() {
		return fmt.Errorf("model input exceeds CONTEXT_TOKEN_BUDGET: estimated %d > %d", used, contextTokenBudget())
	}
	return nil
}

func contextReservedTokens(conversation int64, userText string, hist []memory.Message, attachments []memory.Attachment) int {
	reserve := estimateModelInput(nil, chatToolsForConversation(conversation)) + 1024
	reserve += contextbuilder.EstimateTextTokens(recentChatFileDirectory(conversation, hist, attachments, userText))
	hasFile := false
	for _, a := range attachments {
		if a.Kind == "file" {
			hasFile = true
			reserve += contextbuilder.EstimateTextTokens(attachmentDocumentText(a, chatDocumentAllowance(attachments)))
		} else {
			reserve += 4096
		}
	}
	if hasFile {
		reserve += 2048
	} // Leave room for bounded file tool results.
	reserve += estimateModelInput(recentUserStickerVisionMessages(hist), nil)
	if entries, err := listWorldBookEntries(true); err == nil {
		cfg := loadWorldBookConfig()
		matched, _ := evaluateWorldBookEntries(entries, worldBookScanText(userText, hist, cfg.ScanDepth), cfg, loadWorldBookActivationStates(conversation), worldBookTurn(hist))
		for _, entry := range matched {
			reserve += contextbuilder.EstimateTextTokens(formatWorldBookEntry(entry)) + 16
		}
	}
	// Tail's role suffix is fixed, but appended after Build by existing callers.
	if memory.ConversationAssistant(conversation) == "grok" {
		reserve += 512
	}
	return reserve
}

func keepFixedSystemPrefix(messages []ChatMessage, fixed string) []ChatMessage {
	for i, m := range messages {
		if text, ok := m.Content.(string); m.Role == "system" && ok && text == fixed {
			if i == 0 {
				return messages
			}
			out := make([]ChatMessage, 0, len(messages))
			out = append(out, m)
			out = append(out, messages[:i]...)
			return append(out, messages[i+1:]...)
		}
	}
	return messages
}
