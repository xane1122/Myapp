package contextbuilder

import (
	"encoding/json"
	"fmt"
	"strings"
)

func AssembleUserContext(ctx ContextJSON) (string, error) {
	raw, err := json.Marshal(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`请使用以下结构化上下文回答。current_user_message、用户当前意愿和安全边界优先于旧信息；具体行为统一遵循 runtime_rules、tool_policy 和 memory_policy。索引字段只用于定位，不能当作正文或完整事实。回答要自然，不要暴露字段名，除非用户正在调试系统。
<context_json>
%s
</context_json>`, string(raw)), nil
}

func Validate(ctx ContextJSON) error {
	if strings.TrimSpace(ctx.CurrentUserMessage) == "" {
		return fmt.Errorf("current_user_message is required")
	}
	for _, item := range ctx.MemoryIndex {
		if strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("memory_index item %d missing summary", item.MemoryID)
		}
		if !validSensitivity(item.Sensitivity) {
			return fmt.Errorf("memory_index item %d has invalid sensitivity %q", item.MemoryID, item.Sensitivity)
		}
	}
	for _, doc := range ctx.UploadedDocuments {
		if strings.TrimSpace(doc.Title) == "" {
			return fmt.Errorf("uploaded document %d missing title", doc.DocID)
		}
		if !validSensitivity(doc.Sensitivity) {
			return fmt.Errorf("uploaded document %d has invalid sensitivity %q", doc.DocID, doc.Sensitivity)
		}
	}
	return nil
}

func validSensitivity(s Sensitivity) bool {
	return s == SensitivityLow || s == SensitivityMedium || s == SensitivityHigh
}

func EstimateTokens(ctx ContextJSON) int {
	raw, _ := json.Marshal(ctx)
	if len(raw) == 0 {
		return 0
	}
	return EstimateTextTokens(string(raw))
}
