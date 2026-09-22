package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"myapp/internal/db"
)

const (
	VirtualTransferPending  = "pending"
	VirtualTransferReceived = "received"
	maxVirtualTransferCents = 100_000_000 // CNY 1,000,000.00
)

var (
	ErrVirtualTransferNotFound  = errors.New("模拟转账不存在")
	ErrVirtualTransferRecipient = errors.New("不能领取这笔模拟转账")
	ErrVirtualTransferReceived  = errors.New("这笔模拟转账已领取")
)

// VirtualTransfer represents a chat-only payment card. It never reaches a
// payment provider and is intentionally kept separate from real money data.
type VirtualTransfer struct {
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	MessageID      int64  `json:"message_id"`
	SenderRole     string `json:"sender_role"`
	RecipientRole  string `json:"recipient_role"`
	AmountCents    int64  `json:"amount_cents"`
	Note           string `json:"note,omitempty"`
	Status         string `json:"status"`
	ReceivedAt     string `json:"received_at,omitempty"`
	CreatedAt      string `json:"created_at"`
}

func CreateVirtualTransfer(conversationID int64, senderRole string, amountCents int64, note string) (VirtualTransfer, error) {
	return CreateVirtualTransferIdempotent(conversationID, senderRole, amountCents, note, "")
}

func GetPendingVirtualTransfer(conversationID int64, senderRole string, amountCents int64) (VirtualTransfer, error) {
	var id int64
	err := db.DB.QueryRow(`SELECT id FROM virtual_transfers WHERE conversation_id=? AND sender_role=? AND amount_cents=? AND status=? ORDER BY id DESC LIMIT 1`, conversationID, senderRole, amountCents, VirtualTransferPending).Scan(&id)
	if err != nil {
		return VirtualTransfer{}, err
	}
	return GetVirtualTransfer(id)
}

func CreateVirtualTransferIdempotent(conversationID int64, senderRole string, amountCents int64, note, idempotencyKey string) (VirtualTransfer, error) {
	senderRole = strings.TrimSpace(senderRole)
	if senderRole != "user" && senderRole != "assistant" {
		return VirtualTransfer{}, fmt.Errorf("转账发送方无效")
	}
	if amountCents <= 0 || amountCents > maxVirtualTransferCents {
		return VirtualTransfer{}, fmt.Errorf("转账金额须在 0.01 至 1000000.00 之间")
	}
	note = strings.TrimSpace(note)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) > 128 {
		return VirtualTransfer{}, fmt.Errorf("幂等键过长")
	}
	if len([]rune(note)) > 60 {
		return VirtualTransfer{}, fmt.Errorf("转账备注不能超过 60 字")
	}
	recipientRole := "assistant"
	if senderRole == "assistant" {
		recipientRole = "user"
	}
	if idempotencyKey != "" {
		var existingID int64
		err := db.DB.QueryRow(`SELECT id FROM virtual_transfers WHERE conversation_id=? AND sender_role=? AND idempotency_key=?`, conversationID, senderRole, idempotencyKey).Scan(&existingID)
		if err == nil {
			return GetVirtualTransfer(existingID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return VirtualTransfer{}, err
		}
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return VirtualTransfer{}, err
	}
	defer tx.Rollback()
	messageResult, err := tx.Exec(`INSERT INTO messages(conversation_id,role,content) VALUES(?,?,?)`, conversationID, senderRole, "[模拟微信转账] 正在创建虚拟转账。")
	if err != nil {
		return VirtualTransfer{}, err
	}
	messageID, err := messageResult.LastInsertId()
	if err != nil {
		return VirtualTransfer{}, err
	}
	result, err := tx.Exec(`INSERT INTO virtual_transfers(conversation_id,message_id,sender_role,recipient_role,amount_cents,note,idempotency_key) VALUES(?,?,?,?,?,?,?)`, conversationID, messageID, senderRole, recipientRole, amountCents, note, idempotencyKey)
	if err != nil {
		return VirtualTransfer{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return VirtualTransfer{}, err
	}
	if _, err := tx.Exec(`UPDATE messages SET content=? WHERE id=? AND conversation_id=?`, virtualTransferContent(id, senderRole, amountCents, note, VirtualTransferPending, GetPref("assistant_name", "助手")), messageID, conversationID); err != nil {
		return VirtualTransfer{}, err
	}
	if _, err := tx.Exec(`UPDATE conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, conversationID); err != nil {
		return VirtualTransfer{}, err
	}
	if err := tx.Commit(); err != nil {
		return VirtualTransfer{}, err
	}
	return GetVirtualTransfer(id)
}

func GetVirtualTransfer(id int64) (VirtualTransfer, error) {
	var transfer VirtualTransfer
	err := db.DB.QueryRow(`SELECT id,conversation_id,message_id,sender_role,recipient_role,amount_cents,note,status,COALESCE(received_at,''),created_at FROM virtual_transfers WHERE id=?`, id).Scan(
		&transfer.ID, &transfer.ConversationID, &transfer.MessageID, &transfer.SenderRole, &transfer.RecipientRole,
		&transfer.AmountCents, &transfer.Note, &transfer.Status, &transfer.ReceivedAt, &transfer.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return VirtualTransfer{}, ErrVirtualTransferNotFound
	}
	return transfer, err
}

func ReceiveVirtualTransfer(conversationID, transferID int64, recipientRole string) (VirtualTransfer, error) {
	transfer, err := GetVirtualTransfer(transferID)
	if err != nil {
		return VirtualTransfer{}, err
	}
	if transfer.ConversationID != conversationID || transfer.RecipientRole != recipientRole {
		return VirtualTransfer{}, ErrVirtualTransferRecipient
	}
	if transfer.Status != VirtualTransferPending {
		return VirtualTransfer{}, ErrVirtualTransferReceived
	}
	result, err := db.DB.Exec(`UPDATE virtual_transfers SET status=?,received_at=CURRENT_TIMESTAMP WHERE id=? AND status=?`, VirtualTransferReceived, transferID, VirtualTransferPending)
	if err != nil {
		return VirtualTransfer{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return VirtualTransfer{}, err
	}
	if updated == 0 {
		return VirtualTransfer{}, ErrVirtualTransferReceived
	}
	transfer, err = GetVirtualTransfer(transferID)
	if err != nil {
		return VirtualTransfer{}, err
	}
	if err := UpdateMessageContent(conversationID, transfer.MessageID, transfer.SenderRole, virtualTransferContent(transfer.ID, transfer.SenderRole, transfer.AmountCents, transfer.Note, transfer.Status, GetPref("assistant_name", "助手"))); err != nil {
		return VirtualTransfer{}, err
	}
	return transfer, nil
}

func attachVirtualTransfers(messages []Message) error {
	if len(messages) == 0 {
		return nil
	}
	byMessageID := make(map[int64]int, len(messages))
	placeholders := make([]string, 0, len(messages))
	args := make([]interface{}, 0, len(messages))
	for i := range messages {
		byMessageID[messages[i].ID] = i
		placeholders = append(placeholders, "?")
		args = append(args, messages[i].ID)
	}
	rows, err := db.DB.Query(`SELECT id,conversation_id,message_id,sender_role,recipient_role,amount_cents,note,status,COALESCE(received_at,''),created_at FROM virtual_transfers WHERE message_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var transfer VirtualTransfer
		if err := rows.Scan(&transfer.ID, &transfer.ConversationID, &transfer.MessageID, &transfer.SenderRole, &transfer.RecipientRole, &transfer.AmountCents, &transfer.Note, &transfer.Status, &transfer.ReceivedAt, &transfer.CreatedAt); err != nil {
			return err
		}
		if index, ok := byMessageID[transfer.MessageID]; ok {
			messages[index].Transfer = &transfer
		}
	}
	return rows.Err()
}

func virtualTransferContent(id int64, senderRole string, amountCents int64, note, status, assistantName string) string {
	assistantName = strings.TrimSpace(assistantName)
	if assistantName == "" {
		assistantName = "助手"
	}
	sender, recipient := "你", assistantName
	if senderRole == "assistant" {
		sender, recipient = assistantName, "你"
	}
	state := "待收款"
	if status == VirtualTransferReceived {
		state = "已收款"
	}
	text := fmt.Sprintf("[模拟微信转账 #%d] %s 向 %s 发起了 ¥%d.%02d 的虚拟转账；状态：%s。", id, sender, recipient, amountCents/100, amountCents%100, state)
	if note != "" {
		text += "备注：" + note + "。"
	}
	return text + "仅用于当前聊天互动，不涉及真实资金。"
}
