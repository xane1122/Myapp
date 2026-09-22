package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"myapp/internal/memory"
)

type virtualTransferRequest struct {
	ConversationID int64   `json:"conversation_id"`
	Amount         float64 `json:"amount"`
	Note           string  `json:"note"`
}

func handleVirtualTransfers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req virtualTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "转账信息无效"})
		return
	}
	amountCents, err := virtualTransferAmountCents(req.Amount)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	conversationID, err := memory.EnsureConversation(req.ConversationID)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	transfer, err := memory.CreateVirtualTransferIdempotent(conversationID, "user", amountCents, req.Note, r.Header.Get("Idempotency-Key"))
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	message, err := memory.GetMessage(conversationID, transfer.MessageID)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := memory.SyncConversationJSONL(conversationID); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, http.StatusCreated, map[string]interface{}{"transfer": transfer, "message": message})
}

func handleVirtualTransferRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/virtual-transfers/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "receive" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	transferID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || transferID <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "转账不存在"})
		return
	}
	var req struct {
		ConversationID int64 `json:"conversation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "会话信息无效"})
		return
	}
	conversationID, err := memory.EnsureConversation(req.ConversationID)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	transfer, err := memory.ReceiveVirtualTransfer(conversationID, transferID, "user")
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, memory.ErrVirtualTransferNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, memory.ErrVirtualTransferReceived) {
			status = http.StatusConflict
		}
		jsonResp(w, status, map[string]string{"error": err.Error()})
		return
	}
	if err := memory.SyncConversationJSONL(conversationID); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"transfer": transfer})
}

func virtualTransferAmountCents(amount float64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, errors.New("请输入有效金额")
	}
	centsFloat := amount * 100
	cents := math.Round(centsFloat)
	if math.Abs(centsFloat-cents) > 0.000001 {
		return 0, errors.New("金额最多保留两位小数")
	}
	if cents <= 0 || cents > 100_000_000 {
		return 0, errors.New("转账金额须在 0.01 至 1000000.00 之间")
	}
	return int64(cents), nil
}

func runVirtualTransferTool(arguments string, conversationID int64, generatedTransferIDs *[]int64) string {
	var args struct {
		Amount json.RawMessage `json:"amount"`
		Note   string          `json:"note"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error":"参数格式错误"}`
	}
	amount, err := parseVirtualTransferAmount(args.Amount)
	if err != nil {
		encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(encoded)
	}
	amountCents, err := virtualTransferAmountCents(amount)
	if err != nil {
		encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(encoded)
	}
	if generatedTransferIDs != nil {
		for _, id := range *generatedTransferIDs {
			existing, getErr := memory.GetVirtualTransfer(id)
			if getErr == nil && existing.ConversationID == conversationID && existing.SenderRole == "assistant" && existing.AmountCents == amountCents && strings.TrimSpace(existing.Note) == strings.TrimSpace(args.Note) {
				encoded, _ := json.Marshal(map[string]interface{}{"ok": true, "duplicate": true, "transfer": existing, "message": "相同转账已在本轮发送，不再重复创建。"})
				return string(encoded)
			}
		}
	}
	transfer, err := memory.CreateVirtualTransfer(conversationID, "assistant", amountCents, args.Note)
	if err != nil {
		encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(encoded)
	}
	if generatedTransferIDs != nil {
		*generatedTransferIDs = append(*generatedTransferIDs, transfer.ID)
	}
	_ = memory.SyncConversationJSONL(conversationID)
	encoded, _ := json.Marshal(map[string]interface{}{"ok": true, "transfer": transfer, "message": "虚拟转账卡片已发送。最终回复只需自然说明，不要重复伪造卡片。"})
	return string(encoded)
}

func containsTransferID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// Models occasionally serialize numeric tool arguments as strings. Accept both
// representations while keeping the canonical amount validation in one place.
func parseVirtualTransferAmount(raw json.RawMessage) (float64, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return 0, errors.New("请输入有效金额")
	}
	var amount float64
	if err := json.Unmarshal(raw, &amount); err == nil {
		return amount, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
		return 0, errors.New("请输入有效金额")
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0, errors.New("请输入有效金额")
	}
	return amount, nil
}

func runReceiveVirtualTransferTool(arguments string, conversationID int64) string {
	var args struct {
		TransferID int64 `json:"transfer_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || args.TransferID <= 0 {
		return `{"error":"transfer_id 无效"}`
	}
	transfer, err := memory.ReceiveVirtualTransfer(conversationID, args.TransferID, "assistant")
	if err != nil {
		encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(encoded)
	}
	_ = memory.SyncConversationJSONL(conversationID)
	encoded, _ := json.Marshal(map[string]interface{}{"ok": true, "transfer": transfer, "message": "已领取用户发来的虚拟转账。"})
	return string(encoded)
}
