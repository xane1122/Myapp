package api

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func TestParseVirtualTransferAmount(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want float64
		ok   bool
	}{
		{name: "number", raw: "52.5", want: 52.5, ok: true},
		{name: "numeric string", raw: `"52.5"`, want: 52.5, ok: true},
		{name: "blank", raw: `""`},
		{name: "invalid string", raw: `"52元"`},
		{name: "null", raw: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			amount, err := parseVirtualTransferAmount(json.RawMessage(tt.raw))
			if (err == nil) != tt.ok {
				t.Fatalf("parse(%s) error = %v, want success %v", tt.raw, err, tt.ok)
			}
			if tt.ok && amount != tt.want {
				t.Fatalf("parse(%s) = %v, want %v", tt.raw, amount, tt.want)
			}
		})
	}
}

func TestRunVirtualTransferToolDeduplicatesSameCallInOneReply(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "transfer.db"))
	t.Cleanup(func() { _ = db.DB.Close() })
	conversation, err := memory.CreateConversation("转账幂等")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	first := runVirtualTransferTool(`{"amount":52,"note":"晚餐"}`, conversation.ID, &ids)
	second := runVirtualTransferTool(`{"amount":52,"note":"晚餐"}`, conversation.ID, &ids)
	if len(ids) != 1 {
		t.Fatalf("expected one generated transfer, got %v; first=%s second=%s", ids, first, second)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(second), &result); err != nil {
		t.Fatal(err)
	}
	if result["duplicate"] != true {
		t.Fatalf("expected duplicate result, got %s", second)
	}
}

func TestWithoutToolsRemovesBothVirtualTransferActions(t *testing.T) {
	tools := defaultChatTools()
	filtered := withoutTools(tools, "send_virtual_transfer", "receive_virtual_transfer")
	for _, tool := range filtered {
		if isVirtualTransferTool(tool.Function.Name) {
			t.Fatalf("transfer tool remained available: %s", tool.Function.Name)
		}
	}
	if len(filtered) != len(tools)-2 {
		t.Fatalf("filtered tool count = %d, want %d", len(filtered), len(tools)-2)
	}
	if len(defaultChatTools()) != len(tools) {
		t.Fatal("filter mutated the source tool list")
	}
}
