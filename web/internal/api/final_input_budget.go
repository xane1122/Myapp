package api

import (
	"context"
	"encoding/json"
	"myapp/internal/observability"
	"strings"
)

// Fit the complete request after channel rules, tools and tool results are added.
// Only optional structured context is reduced. Fixed instructions, current input,
// images, tool schemas and complete tool-call/result pairs remain untouched.
func fitFinalModelInput(ctx context.Context, messages []ChatMessage, tools []Tool, priority upstreamPriority) []ChatMessage {
	if _, ok := ctx.Value(statsContextKey{}).(*chatStats); !ok {
		return messages
	}
	before, budget := estimateModelInput(messages, tools), contextTokenBudget()
	if before <= budget {
		return messages
	}
	result := append([]ChatMessage(nil), messages...)
	for i := range result {
		if result[i].Role != "user" {
			continue
		}
		switch content := result[i].Content.(type) {
		case string:
			fitStructuredContextText(content, func(text string) { result[i].Content = text }, func() bool { return estimateModelInput(result, tools) <= budget })
		case []ContentPart:
			parts := append([]ContentPart(nil), content...)
			result[i].Content = parts
			for j := range parts {
				if parts[j].Type == "text" {
					fitStructuredContextText(parts[j].Text, func(text string) { parts[j].Text = text }, func() bool { return estimateModelInput(result, tools) <= budget })
				}
			}
		}
	}
	after := estimateModelInput(result, tools)
	if after < before {
		observability.Event("chat.final_input_fitted", map[string]interface{}{"priority": priority, "before_tokens": before, "after_tokens": after, "budget": budget})
	}
	// The existing guard still refuses an irreducible oversized request; never
	// silently shorten current input, identity, a result or an attachment.
	return result
}

func fitStructuredContextText(text string, replace func(string), fits func() bool) {
	const open, close = "<context_json>", "</context_json>"
	start := strings.Index(text, open)
	end := strings.LastIndex(text, close)
	if start < 0 || end < start+len(open) || fits() {
		return
	}
	start += len(open)
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(text[start:end])), &fields) != nil || fields == nil {
		return
	}
	// Reject unrelated JSON and preserve every unknown field verbatim in value.
	var version, current string
	if json.Unmarshal(fields["context_version"], &version) != nil || version == "" || json.Unmarshal(fields["current_user_message"], &current) != nil || strings.TrimSpace(current) == "" {
		return
	}
	for _, key := range []string{"memory_index", "uploaded_documents", "active_memory", "core_memory", "retrieved_memory", "retrieved_doc_chunks", "recent_messages"} {
		var items []json.RawMessage
		if json.Unmarshal(fields[key], &items) != nil {
			continue
		}
		for len(items) > 0 && !fits() {
			if key == "recent_messages" {
				items = items[1:]
			} else {
				items = items[:len(items)-1]
			}
			fields[key], _ = json.Marshal(items)
			raw, err := json.Marshal(fields)
			if err != nil {
				return
			}
			replace(text[:start]+"\n"+string(raw)+"\n"+text[end:])
		}
	}
}
