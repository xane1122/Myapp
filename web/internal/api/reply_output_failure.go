package api

import (
	"fmt"
	"net/http"
)

const invalidChatReplyError = "模型没有返回可显示的聊天回复，本次任务已失败；刷新只读取记录，不会重新调用模型。"
const invalidNativeReplyOutput = "chat invalid visible reply"

func nativeReplyResponseFailure(status int, resp chatResp, decodeErr error) error {
	if decodeErr == nil && resp.Error == invalidChatReplyError {
		return fmt.Errorf("%s", invalidNativeReplyOutput)
	}
	if status != http.StatusOK || decodeErr != nil || resp.AssistantMessageID <= 0 {
		return fmt.Errorf("chat status %d", status)
	}
	return nil
}
func legacyInvalidReplyFallback(content string) bool {
	return content == assistantFallbackMessage("模型返回了无效的内部草稿，请重新发送。")
}
