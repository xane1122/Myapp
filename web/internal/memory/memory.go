package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"myapp/internal/db"
)

const (
	ScopeConversation    = "conversation"
	ScopeGlobal          = "global"
	conversationJSONLDir = "data/conversations"
)

type Message struct {
	ID               int64            `json:"id,omitempty"`
	ConversationID   int64            `json:"conversation_id,omitempty"`
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	CreatedAt        string           `json:"created_at,omitempty"`
	ReplyToMessageID int64            `json:"reply_to_message_id,omitempty"`
	QuoteRole        string           `json:"quote_role,omitempty"`
	QuoteText        string           `json:"quote_text,omitempty"`
	Attachments      []Attachment     `json:"attachments,omitempty"`
	Transfer         *VirtualTransfer `json:"transfer,omitempty"`
}

type Chunk struct {
	ID             int64         `json:"id"`
	ConversationID int64         `json:"conversation_id"`
	Assistant      string        `json:"assistant,omitempty"`
	Scope          string        `json:"scope"`
	SourceDocID    sql.NullInt64 `json:"-"`
	SourceType     string        `json:"source_type"`
	Content        string        `json:"content"`
	Summary        string        `json:"summary,omitempty"`
	Keywords       string        `json:"keywords,omitempty"`
	TimeStart      string        `json:"time_start,omitempty"`
	TimeEnd        string        `json:"time_end,omitempty"`
	Emotion        string        `json:"emotion,omitempty"`
	Correction     string        `json:"correction,omitempty"`
	TopicLabel     string        `json:"topic_label,omitempty"`
	IsCorrection   bool          `json:"is_correction"`
	Importance     string        `json:"importance"`
	Active         bool          `json:"active"`
	Version        int           `json:"version"`
	SupersededBy   sql.NullInt64 `json:"-"`
	LastAccessedAt string        `json:"last_accessed_at,omitempty"`
	CreatedAt      string        `json:"created_at"`
 Status string `json:"status"`
 SourceConversationID sql.NullInt64 `json:"-"`
 MemoryType string `json:"memory_type"`
 Confidence float64 `json:"confidence"`
 UpdatedAt string `json:"updated_at"`
 SourceDate string `json:"source_date,omitempty"`
 OriginalSnippet string `json:"original_snippet,omitempty"`
 MatchScore float64 `json:"match_score"`
 RankMethod string `json:"rank_method,omitempty"`
}

type ChunkMetadata struct {
 MemoryType string
 Confidence *float64
	TimeStart    string
	TimeEnd      string
	Emotion      string
	Correction   string
	TopicLabel   string
	IsCorrection bool
	Importance   string
}

type MergeCandidate struct {
	A                 Chunk   `json:"a"`
	B                 Chunk   `json:"b"`
	KeywordSimilarity float64 `json:"keyword_similarity"`
	SummarySimilarity float64 `json:"summary_similarity"`
}

type Conversation struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Assistant string `json:"assistant"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PersonaProfile struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	SourcePath string `json:"source_path,omitempty"`
	Active     bool   `json:"active"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type KnowledgeDoc struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	SourcePath  string `json:"source_path,omitempty"`
	ContentText string `json:"content_text,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Attachment struct {
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	MessageID      int64  `json:"message_id,omitempty"`
	Kind           string `json:"kind"`
	OriginalName   string `json:"original_name,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	URL            string `json:"url"`
	MimeType       string `json:"mime_type"`
	SizeBytes      int64  `json:"size_bytes"`
	ContentHash    string `json:"content_hash,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type Sticker struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FilePath    string `json:"file_path,omitempty"`
	URL         string `json:"url"`
	MimeType    string `json:"mime_type"`
	Tags        string `json:"tags,omitempty"`
	Description string `json:"description,omitempty"`
	Mood        string `json:"mood,omitempty"`
	Enabled     bool   `json:"enabled"`
	NeedsReview bool   `json:"needs_review"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func SaveMessage(conversationID int64, role, content string) (int64, error) {
	return SaveMessageReplyingTo(conversationID, role, content, 0)
}

func SaveMessageReplyingTo(conversationID int64, role, content string, replyToMessageID int64) (int64, error) {
	var quoteRole, quoteText string
	if replyToMessageID > 0 {
		if err := db.DB.QueryRow(`SELECT role,content FROM all_messages WHERE conversation_id=? AND id=?`, conversationID, replyToMessageID).Scan(&quoteRole, &quoteText); err != nil {
			return 0, fmt.Errorf("引用消息无效: %w", err)
		}
	}
	res, err := db.DB.Exec(
		`INSERT INTO messages (conversation_id, role, content, reply_to_message_id, quote_role, quote_text) VALUES (?, ?, ?, NULLIF(?,0), ?, ?)`,
		conversationID, role, content, replyToMessageID, quoteRole, quoteText,
	)
	if err != nil {
		return 0, err
	}
	_ = TouchConversation(conversationID)
	return res.LastInsertId()
}

func GetMessage(conversationID, messageID int64) (Message, error) {
	var m Message
	err := db.DB.QueryRow(
		`SELECT id,conversation_id,role,content,created_at,COALESCE(reply_to_message_id,0),quote_role,quote_text FROM all_messages WHERE conversation_id=? AND id=? LIMIT 1`,
		conversationID, messageID,
	).Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt, &m.ReplyToMessageID, &m.QuoteRole, &m.QuoteText)
	if err != nil {
		return Message{}, err
	}
	msgs := []Message{m}
	if err := attachMessages(msgs); err != nil {
		return Message{}, err
	}
	return msgs[0], nil
}

func GetHistoryBefore(conversationID, beforeID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.DB.Query(
		`SELECT id,conversation_id,role,content,created_at,COALESCE(reply_to_message_id,0),quote_role,quote_text FROM all_messages WHERE conversation_id=? AND id<? ORDER BY id DESC LIMIT ?`,
		conversationID, beforeID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	if err := attachMessages(msgs); err != nil {
		return nil, err
	}
	return msgs, rows.Err()
}

func ConversationAssistant(id int64) string {
	var a string
	if err := db.DB.QueryRow(`SELECT assistant FROM conversations WHERE id=?`, id).Scan(&a); err != nil || a == "" {
		return "rhys"
	}
	return a
}

func UpdateMessageContent(conversationID, messageID int64, role, content string) error {
	result, err := db.DB.Exec(
		`UPDATE messages SET content=? WHERE conversation_id=? AND id=? AND role=?`,
		content, conversationID, messageID, role,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		result, err = db.DB.Exec(
			`UPDATE messages_archive SET content=? WHERE conversation_id=? AND id=? AND role=?`,
			content, conversationID, messageID, role,
		)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return err
		}
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	_ = TouchConversation(conversationID)
	return nil
}

func GetHistory(conversationID int64, limit int) ([]Message, error) {
	rows, err := db.DB.Query(
		`SELECT id,conversation_id,role,content,created_at,COALESCE(reply_to_message_id,0),quote_role,quote_text FROM all_messages WHERE conversation_id=? ORDER BY id DESC LIMIT ?`,
		conversationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt, &m.ReplyToMessageID, &m.QuoteRole, &m.QuoteText); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	if err := attachMessages(msgs); err != nil {
		return nil, err
	}
	return msgs, rows.Err()
}

func GetHistoryAfter(conversationID, afterID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.DB.Query(
		`SELECT id,conversation_id,role,content,created_at,COALESCE(reply_to_message_id,0),quote_role,quote_text FROM all_messages WHERE conversation_id=? AND id>? ORDER BY id ASC LIMIT ?`,
		conversationID, afterID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if err := attachMessages(msgs); err != nil {
		return nil, err
	}
	return msgs, rows.Err()
}

func GetUnarchivedHistory(conversationID int64, limit int) ([]Message, error) {
	rows, err := db.DB.Query(
		`SELECT id, conversation_id, role, content, created_at, COALESCE(reply_to_message_id,0), quote_role, quote_text
		 FROM messages
		 WHERE conversation_id=? AND memory_archived_at IS NULL
		 ORDER BY id ASC LIMIT ?`,
		conversationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	if err := attachMessages(msgs); err != nil {
		return nil, err
	}
	return msgs, rows.Err()
}

func GetMessagesByIDs(conversationID int64, ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []interface{}{conversationID}
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := db.DB.Query(
		`SELECT id,conversation_id,role,content,created_at,COALESCE(reply_to_message_id,0),quote_role,quote_text FROM all_messages WHERE conversation_id=? AND id IN (`+strings.Join(placeholders, ",")+`) ORDER BY id ASC`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt, &m.ReplyToMessageID, &m.QuoteRole, &m.QuoteText); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func SearchMessages(conversationID int64, query string, topK int) ([]Message, error) {
	if topK <= 0 {
		topK = 6
	}
	if topK > 12 {
		topK = 12
	}
	words := tokenizeSearchQuery(query)
	if len(words) == 0 {
		return GetHistory(conversationID, topK)
	}

	candidates := map[int64]Message{}
	candidateLimit := max(topK*8, 24)
	for _, w := range words {
		like := "%" + w + "%"
		rows, err := db.DB.Query(
			`SELECT id, conversation_id, role, content, created_at FROM (
				SELECT id, conversation_id, role, content, created_at FROM messages WHERE conversation_id=? AND content LIKE ?
				UNION ALL
				SELECT id, conversation_id, role, content, created_at FROM messages_archive WHERE conversation_id=? AND content LIKE ?
			 ) ORDER BY id DESC LIMIT ?`,
			conversationID, like, conversationID, like, candidateLimit,
		)
		if err != nil {
			continue
		}
		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
				continue
			}
			candidates[m.ID] = m
		}
		rows.Close()
	}
	type scoredMessage struct {
		message Message
		score   int
	}
	scored := make([]scoredMessage, 0, len(candidates))
	for _, m := range candidates {
		content := strings.ToLower(m.Content)
		score := 0
		for _, word := range words {
			if strings.Contains(content, strings.ToLower(word)) {
				score += utf8.RuneCountInString(word)
			}
		}
		scored = append(scored, scoredMessage{message: m, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].message.ID > scored[j].message.ID
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	results := make([]Message, len(scored))
	for i := range scored {
		results[i] = scored[i].message
	}
	if err := attachMessages(results); err != nil {
		return nil, err
	}
	return results, nil
}

func attachMessages(msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	byID := map[int64]int{}
	var ids []string
	args := make([]interface{}, 0, len(msgs))
	for i := range msgs {
		byID[msgs[i].ID] = i
		ids = append(ids, "?")
		args = append(args, msgs[i].ID)
	}
	rows, err := db.DB.Query(
		`SELECT a.id, a.conversation_id, COALESCE(a.message_id,0), a.kind, COALESCE(a.original_name,''), a.file_path, a.url, a.mime_type, a.size_bytes, COALESCE(a.content_hash,''), a.created_at
		 FROM message_attachments a
		 JOIN all_messages m ON m.id=a.message_id AND m.conversation_id=a.conversation_id
		 WHERE m.id IN (`+strings.Join(ids, ",")+`)
		 ORDER BY a.id ASC`, args...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.ConversationID, &a.MessageID, &a.Kind, &a.OriginalName, &a.FilePath, &a.URL, &a.MimeType, &a.SizeBytes, &a.ContentHash, &a.CreatedAt); err != nil {
			return err
		}
		if idx, ok := byID[a.MessageID]; ok {
			msgs[idx].Attachments = append(msgs[idx].Attachments, a)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return attachVirtualTransfers(msgs)
}

func CountMessages(conversationID int64) (int, error) {
	var count int
	err := db.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM messages WHERE conversation_id=?) +
		(SELECT COUNT(*) FROM messages_archive WHERE conversation_id=?)`, conversationID, conversationID).Scan(&count)
	return count, err
}

func ConversationHasContent(conversationID int64) (bool, error) {
	var count int
	err := db.DB.QueryRow(
		`SELECT
			(SELECT COUNT(*) FROM messages WHERE conversation_id=?) +
			(SELECT COUNT(*) FROM messages_archive WHERE conversation_id=?) +
			(SELECT COUNT(*) FROM memory_chunks WHERE scope=? AND conversation_id=?)`,
		conversationID, conversationID, ScopeConversation, conversationID,
	).Scan(&count)
	return count > 0, err
}

func CountUnarchivedMessages(conversationID int64) (int, error) {
	var count int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=? AND memory_archived_at IS NULL`, conversationID).Scan(&count)
	return count, err
}

func MarkMessagesMemoryArchived(conversationID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, conversationID)
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := db.DB.Exec(
		`UPDATE messages
		 SET memory_archived_at=CURRENT_TIMESTAMP
		 WHERE conversation_id=? AND id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	return err
}

func DeleteOldestN(conversationID int64, n int) error {
	_, err := db.DB.Exec(
		`DELETE FROM messages WHERE id IN (
			SELECT id FROM messages WHERE conversation_id=? ORDER BY id ASC LIMIT ?
		)`,
		conversationID, n,
	)
	if err == nil {
		_ = SyncConversationJSONL(conversationID)
	}
	return err
}

func SaveChunk(conversationID int64, scope, sourceType string, sourceDocID sql.NullInt64, content, summary, keywords string) error {
	return SaveChunkWithMetadata(conversationID, scope, sourceType, sourceDocID, content, summary, keywords, ChunkMetadata{})
}

// FindDuplicateChunk returns an existing chunk with the same normalized content.
func FindDuplicateChunk(conversationID int64, scope, sourceType, content string) (Chunk, bool, error) {
	normalized := normalizeChunkContent(content)
	if normalized == "" {
		return Chunk{}, false, nil
	}
	chunks, err := ListChunksByScope(conversationID, scope, 1000)
	if err != nil {
		return Chunk{}, false, err
	}
	for _, chunk := range chunks {
		if chunk.SourceType == sourceType && normalizeChunkContent(chunk.Content) == normalized {
			return chunk, true, nil
		}
	}
	return Chunk{}, false, nil
}

func normalizeChunkContent(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(text))
}

func SaveChunkWithMetadata(conversationID int64, scope, sourceType string, sourceDocID sql.NullInt64, content, summary, keywords string, meta ChunkMetadata) error {
	_, err := SaveChunkWithMetadataReturningID(conversationID, scope, sourceType, sourceDocID, content, summary, keywords, meta)
	return err
}

func SaveChunkWithMetadataReturningID(conversationID int64, scope, sourceType string, sourceDocID sql.NullInt64, content, summary, keywords string, meta ChunkMetadata) (int64, error) {
 return saveVersionedChunk(conversationID,scope,sourceType,sourceDocID,content,summary,keywords,meta)
}

const chunkSelectColumns = `id, conversation_id, COALESCE(assistant,'rhys'), scope, source_doc_id, source_type, content,
	COALESCE(summary,''), COALESCE(keywords,''), COALESCE(time_start,''), COALESCE(time_end,''),
	COALESCE(emotion,''), COALESCE(correction,''), COALESCE(topic_label,''),
	COALESCE(is_correction,0), COALESCE(importance,'medium'), COALESCE(active,1),
	COALESCE(version,1), superseded_by, COALESCE(last_accessed_at,created_at), created_at, status, source_conversation_id, memory_type, confidence, updated_at`

func ListChunks(conversationID int64, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 80
	}
	rows, err := db.DB.Query(
		`SELECT `+chunkSelectColumns+`
		 FROM memory_chunks
			 WHERE active=1 AND assistant=? AND (scope=? OR conversation_id=?)
		 ORDER BY is_correction DESC, CASE WHEN scope=? THEN 0 ELSE 1 END, COALESCE(last_accessed_at,created_at) DESC, id DESC LIMIT ?`,
		ConversationAssistant(conversationID), ScopeGlobal, conversationID, ScopeGlobal, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func ListChunksByScope(conversationID int64, scope string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 80
	}
	var rows *sql.Rows
	var err error
	switch scope {
	case ScopeGlobal:
		rows, err = db.DB.Query(
			`SELECT `+chunkSelectColumns+`
			 FROM memory_chunks
			 WHERE active=1 AND assistant=? AND scope=?
			 ORDER BY is_correction DESC, COALESCE(last_accessed_at,created_at) DESC, id DESC LIMIT ?`,
			ConversationAssistant(conversationID), ScopeGlobal, limit,
		)
	case ScopeConversation:
		rows, err = db.DB.Query(
			`SELECT `+chunkSelectColumns+`
			 FROM memory_chunks
			 WHERE active=1 AND scope=? AND conversation_id=?
			 ORDER BY is_correction DESC, COALESCE(last_accessed_at,created_at) DESC, id DESC LIMIT ?`,
			ScopeConversation, conversationID, limit,
		)
	default:
		return ListChunks(conversationID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// ListCoreIndexChunks returns the small resident memory directory used by the
// reply model. Detailed recall remains available through search_memory.
func ListCoreIndexChunks(conversationID int64, globalLimit, conversationLimit int) ([]Chunk, error) {
	if globalLimit <= 0 {
		globalLimit = 8
	}
	if conversationLimit <= 0 {
		conversationLimit = 3
	}
	rows, err := db.DB.Query(
		`SELECT `+chunkSelectColumns+` FROM (
			SELECT *, 0 AS resident_group FROM memory_chunks
			WHERE active=1 AND assistant=? AND scope=? AND importance='high'
			ORDER BY is_correction DESC, COALESCE(last_accessed_at,created_at) DESC, id DESC LIMIT ?
		) UNION ALL SELECT `+chunkSelectColumns+` FROM (
			SELECT *, 1 AS resident_group FROM memory_chunks
			WHERE active=1 AND assistant=? AND scope=? AND conversation_id=?
			ORDER BY is_correction DESC, COALESCE(last_accessed_at,created_at) DESC, id DESC LIMIT ?
		)`,
		ConversationAssistant(conversationID), ScopeGlobal, globalLimit, ConversationAssistant(conversationID), ScopeConversation, conversationID, conversationLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		chunk, scanErr := scanChunk(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func GetChunk(id int64) (Chunk, error) {
	row := db.DB.QueryRow(
		`SELECT `+chunkSelectColumns+`
		 FROM memory_chunks WHERE id=?`, id,
	)
	return scanChunk(row)
}

func UpdateChunk(id int64, content, summary, keywords string) (Chunk, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Chunk{}, fmt.Errorf("记忆内容不能为空")
	}
	_, err := db.DB.Exec(
		`UPDATE memory_chunks SET content=?, summary=?, keywords=?, version=version+1 WHERE id=?`,
		content, strings.TrimSpace(summary), strings.TrimSpace(keywords), id,
	)
	if err != nil {
		return Chunk{}, err
	}
	return GetChunk(id)
}

func UpdateChunkForConversation(conversationID, id int64, content, summary, keywords string) (Chunk, error) {
	chunk, err := GetChunk(id)
	if err != nil {
		return Chunk{}, err
	}
	if chunk.Scope != ScopeGlobal && chunk.ConversationID != conversationID {
		return Chunk{}, sql.ErrNoRows
	}
	return UpdateChunk(id, content, summary, keywords)
}

func DeleteChunk(id int64) error {
	_, err := db.DB.Exec(`DELETE FROM memory_chunks WHERE id=?`, id)
	return err
}

func DeleteChunkForConversation(conversationID, id int64) error {
	chunk, err := GetChunk(id)
	if err != nil {
		return err
	}
	if chunk.Scope != ScopeGlobal && chunk.ConversationID != conversationID {
		return sql.ErrNoRows
	}
	return DeleteChunk(id)
}

type chunkScanner interface {
	Scan(dest ...interface{}) error
}

func scanChunk(row chunkScanner) (Chunk, error) {
	var c Chunk
	var correction, active int
	if err := row.Scan(&c.ID, &c.ConversationID, &c.Assistant, &c.Scope, &c.SourceDocID, &c.SourceType, &c.Content,
		&c.Summary, &c.Keywords, &c.TimeStart, &c.TimeEnd, &c.Emotion, &c.Correction,
		&c.TopicLabel, &correction, &c.Importance, &active, &c.Version, &c.SupersededBy,
		&c.LastAccessedAt, &c.CreatedAt,&c.Status,&c.SourceConversationID,&c.MemoryType,&c.Confidence,&c.UpdatedAt); err != nil {
		return Chunk{}, err
	}
	c.IsCorrection = correction != 0
	c.Active = active != 0
	return c, nil
}

func normalizeImportance(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

func SearchChunks(conversationID int64, query string, topK int) ([]Chunk, error) {
 return searchMemoryFTS(conversationID,query,nil,topK,true)
}

// SearchChunksByQueryOnly is used for explicit personal-fact questions where
// recent conversational context and unrelated corrections must not outrank a
// direct keyword match.
func SearchChunksByQueryOnly(conversationID int64, query string, topK int) ([]Chunk, error) {
 return searchMemoryFTS(conversationID,query,nil,topK,false)
}

func recentSearchContextWords(conversationID int64, exclude []string) []string {
	seen := map[string]bool{}
	for _, word := range exclude {
		seen[strings.ToLower(word)] = true
	}
	add := func(text string, out *[]string) {
		for _, word := range tokenizeSearchQuery(text) {
			key := strings.ToLower(word)
			if seen[key] {
				continue
			}
			seen[key] = true
			*out = append(*out, word)
			if len(*out) >= 24 {
				return
			}
		}
	}
	words := []string{}
	rows, err := db.DB.Query(`SELECT content FROM all_messages WHERE conversation_id=? ORDER BY id DESC LIMIT 10`, conversationID)
	if err == nil {
		for rows.Next() && len(words) < 16 {
			var content string
			if rows.Scan(&content) == nil {
				add(content, &words)
			}
		}
		rows.Close()
	}
	rows, err = db.DB.Query(`SELECT COALESCE(keywords,''),COALESCE(topic_label,'') FROM memory_chunks
		WHERE active=1 AND assistant=? AND (scope=? OR conversation_id=?) ORDER BY COALESCE(last_accessed_at,created_at) DESC,id DESC LIMIT 4`, ConversationAssistant(conversationID), ScopeGlobal, conversationID)
	if err == nil {
		for rows.Next() && len(words) < 24 {
			var keywords, topic string
			if rows.Scan(&keywords, &topic) == nil {
				add(keywords+" "+topic, &words)
			}
		}
		rows.Close()
	}
	return words
}

func SearchChunksBySourceTypes(conversationID int64, query string, sourceTypes []string, topK int) ([]Chunk, error) {
 return searchMemoryFTS(conversationID,query,sourceTypes,topK,true)
}

func listSearchableChunks(conversationID int64, sourceTypes []string) ([]Chunk, error) {
	rows, err := db.DB.Query(`SELECT `+chunkSelectColumns+` FROM memory_chunks WHERE active=1 AND assistant=? AND (scope=? OR conversation_id=?)`, ConversationAssistant(conversationID), ScopeGlobal, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func parseSQLiteTime(raw string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Now()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ClearHistory(conversationID int64) error {
	paths, _ := attachmentPathsForConversation(conversationID)
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM messages WHERE conversation_id=?`, conversationID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM messages_archive WHERE conversation_id=?`, conversationID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM message_attachments WHERE conversation_id=?`, conversationID); err != nil {
		return err
	}
	err = tx.Commit()
	if err == nil {
		removeUnreferencedFiles(paths)
		_ = SyncConversationJSONL(conversationID)
	}
	return err
}

func DeleteConversation(id int64) error {
	if id <= 0 {
		return fmt.Errorf("conversation_id无效")
	}
	paths, _ := attachmentPathsForConversation(id)
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM message_attachments WHERE conversation_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE conversation_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM messages_archive WHERE conversation_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM memory_chunks WHERE scope=? AND conversation_id=?`, ScopeConversation, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tool_activity WHERE conversation_id=?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM conversations WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	removeUnreferencedFiles(paths)
	_ = os.Remove(ConversationJSONLPath(id))
	return nil
}

// NormalizeCorrections keeps only the newest correction for each visible topic.
func NormalizeCorrections(conversationID int64) error {
	_, err := db.DB.Exec(`UPDATE memory_chunks SET is_correction=0
		WHERE is_correction=1 AND id NOT IN (
			SELECT MAX(id) FROM memory_chunks WHERE is_correction=1 AND topic_label<>''
			AND (scope=? OR conversation_id=?)
			GROUP BY scope, CASE WHEN scope=? THEN 0 ELSE conversation_id END, topic_label
		) AND (scope=? OR conversation_id=?)`,
		ScopeGlobal, conversationID, ScopeGlobal, ScopeGlobal, conversationID)
	return err
}

func FindMergeCandidates(conversationID int64) ([]MergeCandidate, error) {
	chunks, err := ListChunksByScope(conversationID, "all", 500)
	if err != nil {
		return nil, err
	}
	var out []MergeCandidate
	for i := 0; i < len(chunks); i++ {
		for j := i + 1; j < len(chunks); j++ {
			a, b := chunks[i], chunks[j]
			if a.IsCorrection || b.IsCorrection || a.TopicLabel == "" || a.TopicLabel != b.TopicLabel || !timeRangesOverlap(a, b) {
				continue
			}
			kw := setJaccard(tokenize(a.Keywords), tokenize(b.Keywords))
			sim := runeBigramJaccard(a.Summary, b.Summary)
			if kw > 0.5 && sim > 0.7 {
				out = append(out, MergeCandidate{A: a, B: b, KeywordSimilarity: kw, SummarySimilarity: sim})
			}
		}
	}
	return out, nil
}

func MergeChunks(conversationID, aID, bID int64) (Chunk, error) {
	a, err := GetChunk(aID)
	if err != nil {
		return Chunk{}, err
	}
	b, err := GetChunk(bID)
	if err != nil {
		return Chunk{}, err
	}
	if a.IsCorrection || b.IsCorrection || a.TopicLabel == "" || a.TopicLabel != b.TopicLabel {
		return Chunk{}, fmt.Errorf("记忆不满足合并条件")
	}
	if a.Scope != ScopeGlobal && a.ConversationID != conversationID {
		return Chunk{}, sql.ErrNoRows
	}
	if b.Scope != ScopeGlobal && b.ConversationID != conversationID {
		return Chunk{}, sql.ErrNoRows
	}
	keep, drop := a, b
	if len([]rune(b.Summary)) > len([]rune(a.Summary)) {
		keep, drop = b, a
	}
	summary := keep.Summary
	if !strings.Contains(normalizeText(keep.Summary), normalizeText(drop.Summary)) && strings.TrimSpace(drop.Summary) != "" {
		summary = strings.TrimSpace(keep.Summary) + "；" + strings.TrimSpace(drop.Summary)
	}
	content := keep.Content
	if strings.TrimSpace(drop.Content) != "" {
		content += "\n---\n" + drop.Content
	}
	keywords := strings.Join(unionTokens(keep.Keywords, drop.Keywords), ",")
	tx, err := db.DB.Begin()
	if err != nil {
		return Chunk{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE memory_chunks SET content=?, summary=?, keywords=?, time_start=MIN(COALESCE(NULLIF(time_start,''),?),?), time_end=MAX(COALESCE(NULLIF(time_end,''),?),?) WHERE id=?`, content, summary, keywords, drop.TimeStart, keep.TimeStart, drop.TimeEnd, keep.TimeEnd, keep.ID)
	if err != nil {
		return Chunk{}, err
	}
	if _, err = tx.Exec(`DELETE FROM memory_chunks WHERE id=?`, drop.ID); err != nil {
		return Chunk{}, err
	}
	if err = tx.Commit(); err != nil {
		return Chunk{}, err
	}
	return GetChunk(keep.ID)
}

func ArchiveInactiveConversations(activeConversationID int64, threshold int) error {
	if threshold <= 0 {
		threshold = 200
	}
	var total int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&total); err != nil || total <= threshold {
		return err
	}
	rows, err := db.DB.Query(`SELECT conversation_id, COUNT(*) FROM messages WHERE conversation_id<>? GROUP BY conversation_id`, activeConversationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	moving := 0
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		ids = append(ids, id)
		moving += n
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	logArchive("before", ids, moving, total, 0)
	marks := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := strings.Join(marks, ",")
	if _, err = tx.Exec(`INSERT OR IGNORE INTO messages_archive (id,conversation_id,role,content,created_at,memory_archived_at,reply_to_message_id,quote_role,quote_text) SELECT id,conversation_id,role,content,created_at,memory_archived_at,reply_to_message_id,quote_role,quote_text FROM messages WHERE conversation_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM messages WHERE conversation_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	var archived, remaining int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM messages_archive`).Scan(&archived)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&remaining)
	logArchive("after", ids, moving, remaining, archived)
	return nil
}

func logArchive(stage string, ids []int64, moving, total, archived int) {
	log.Printf("memory archive %s conversation_ids=%v moving=%d messages=%d archive=%d", stage, ids, moving, total, archived)
}
func timeRangesOverlap(a, b Chunk) bool {
	if a.TimeStart == "" || a.TimeEnd == "" || b.TimeStart == "" || b.TimeEnd == "" {
		return false
	}
	return a.TimeStart <= b.TimeEnd && b.TimeStart <= a.TimeEnd
}
func normalizeText(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), "") }
func setJaccard(a, b []string) float64 {
	as := map[string]bool{}
	bs := map[string]bool{}
	for _, x := range a {
		as[x] = true
	}
	for _, x := range b {
		bs[x] = true
	}
	u, i := 0, 0
	for x := range as {
		u++
		if bs[x] {
			i++
		}
	}
	for x := range bs {
		if !as[x] {
			u++
		}
	}
	if u == 0 {
		return 0
	}
	return float64(i) / float64(u)
}
func runeBigramJaccard(a, b string) float64 { return setJaccard(runeBigrams(a), runeBigrams(b)) }
func runeBigrams(s string) []string {
	r := []rune(normalizeText(s))
	if len(r) < 2 {
		return []string{string(r)}
	}
	o := make([]string, 0, len(r)-1)
	for i := 0; i < len(r)-1; i++ {
		o = append(o, string(r[i:i+2]))
	}
	return o
}
func unionTokens(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		for _, x := range tokenize(v) {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
}

func SetPref(key, value string) error {
	_, err := db.DB.Exec(
		`INSERT INTO preferences (key, value, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		key, value,
	)
	return err
}

func GetPref(key, defaultVal string) string {
	var val string
	err := db.DB.QueryRow(`SELECT value FROM preferences WHERE key=?`, key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

func GetAllPrefs() (map[string]string, error) {
	rows, err := db.DB.Query(`SELECT key, value FROM preferences`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prefs := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		prefs[k] = v
	}
	return prefs, rows.Err()
}

func CreateConversation(title string) (Conversation, error) {
	return CreateConversationForAssistant(title, "rhys")
}

func CreateConversationForAssistant(title, assistant string) (Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新对话"
	}
	assistant = strings.ToLower(strings.TrimSpace(assistant))
	if assistant != "grok" {
		assistant = "rhys"
	}
	res, err := db.DB.Exec(`INSERT INTO conversations (title, assistant) VALUES (?,?)`, title, assistant)
	if err != nil {
		return Conversation{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Conversation{}, err
	}
	c, err := GetConversation(id)
	if err == nil {
		_ = SyncConversationJSONL(id)
	}
	return c, err
}

func GetConversation(id int64) (Conversation, error) {
	var c Conversation
	err := db.DB.QueryRow(
		`SELECT id, title, assistant, created_at, updated_at FROM conversations WHERE id=?`, id,
	).Scan(&c.ID, &c.Title, &c.Assistant, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func EnsureConversation(id int64) (int64, error) {
	if id > 0 {
		var exists int
		err := db.DB.QueryRow(`SELECT 1 FROM conversations WHERE id=?`, id).Scan(&exists)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	c, err := CreateConversation("新对话")
	if err != nil {
		return 0, err
	}
	return c.ID, nil
}

func EnsureDefaultConversation() (int64, error) {
	conversations, err := ListConversations()
	if err != nil {
		return 0, err
	}
	if len(conversations) > 0 {
		return conversations[0].ID, nil
	}
	c, err := CreateConversation("默认对话")
	if err != nil {
		return 0, err
	}
	return c.ID, nil
}

func ListConversations() ([]Conversation, error) {
	return ListConversationsForAssistant("")
}

func ListConversationsForAssistant(assistant string) ([]Conversation, error) {
	query := `SELECT id, title, assistant, created_at, updated_at FROM conversations`
	var args []interface{}
	if assistant = strings.ToLower(strings.TrimSpace(assistant)); assistant != "" {
		query += ` WHERE assistant=?`
		args = append(args, assistant)
	}
	query += ` ORDER BY datetime(updated_at) DESC, id DESC`
	rows, err := db.DB.Query(
		query, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conversations []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Assistant, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func RenameConversation(id int64, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("title不能为空")
	}
	_, err := db.DB.Exec(`UPDATE conversations SET title=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, title, id)
	if err == nil {
		_ = SyncConversationJSONL(id)
	}
	return err
}

func TouchConversation(id int64) error {
	_, err := db.DB.Exec(`UPDATE conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	return err
}

func ConversationJSONLPath(conversationID int64) string {
	return filepath.Join(conversationJSONLDir, fmt.Sprintf("%d.jsonl", conversationID))
}

func SyncConversationJSONL(conversationID int64) error {
	if conversationID <= 0 {
		return fmt.Errorf("conversation_id无效")
	}
	conversation, err := GetConversation(conversationID)
	if err != nil {
		return err
	}
	messages, err := GetHistory(conversationID, 10000)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(conversationJSONLDir, 0755); err != nil {
		return err
	}
	path := ConversationJSONLPath(conversationID)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	writeErr := enc.Encode(map[string]interface{}{
		"type":         "conversation",
		"conversation": conversationWithBeijingTimes(conversation),
	})
	if writeErr == nil {
		for _, message := range messages {
			writeErr = enc.Encode(map[string]interface{}{
				"type":    "message",
				"message": messageWithBeijingTime(message),
			})
			if writeErr != nil {
				break
			}
		}
	}
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, path)
}

func conversationWithBeijingTimes(conversation Conversation) Conversation {
	conversation.CreatedAt = db.BeijingTimestamp(conversation.CreatedAt)
	conversation.UpdatedAt = db.BeijingTimestamp(conversation.UpdatedAt)
	return conversation
}

func messageWithBeijingTime(message Message) Message {
	message.CreatedAt = db.BeijingTimestamp(message.CreatedAt)
	for i := range message.Attachments {
		message.Attachments[i].CreatedAt = db.BeijingTimestamp(message.Attachments[i].CreatedAt)
	}
	return message
}

func MaybeTitleConversation(id int64, text string) error {
	var title string
	err := db.DB.QueryRow(`SELECT title FROM conversations WHERE id=?`, id).Scan(&title)
	if err != nil || title != "新对话" {
		return err
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}
	if len(runes) > 18 {
		runes = runes[:18]
	}
	return RenameConversation(id, string(runes))
}

func CreatePersona(title, content, sourcePath string, active bool) (PersonaProfile, error) {
	title = defaultTitle(title, "人物设定")
	content = strings.TrimSpace(content)
	if content == "" {
		return PersonaProfile{}, fmt.Errorf("人物设定不能为空")
	}
	if existing, ok, err := findPersonaByIdentity(title, content); err != nil {
		return PersonaProfile{}, err
	} else if ok {
		if active {
			if _, err := db.DB.Exec(`UPDATE persona_profiles SET active=0`); err != nil {
				return PersonaProfile{}, err
			}
		}
		_, err := db.DB.Exec(
			`UPDATE persona_profiles
			 SET content=?, source_path=?, active=CASE WHEN ?=1 THEN 1 ELSE active END, updated_at=CURRENT_TIMESTAMP
			 WHERE id=?`,
			content, sourcePath, boolInt(active), existing.ID,
		)
		if err != nil {
			return PersonaProfile{}, err
		}
		return GetPersona(existing.ID)
	}
	if active {
		if _, err := db.DB.Exec(`UPDATE persona_profiles SET active=0`); err != nil {
			return PersonaProfile{}, err
		}
	}
	res, err := db.DB.Exec(
		`INSERT INTO persona_profiles (title, content, source_path, active) VALUES (?, ?, ?, ?)`,
		title, content, sourcePath, boolInt(active),
	)
	if err != nil {
		return PersonaProfile{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return PersonaProfile{}, err
	}
	return GetPersona(id)
}

func ListPersonas() ([]PersonaProfile, error) {
	rows, err := db.DB.Query(
		`SELECT id, title, content, COALESCE(source_path,''), active, created_at, updated_at
		 FROM persona_profiles ORDER BY active DESC, id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var personas []PersonaProfile
	seen := map[string]bool{}
	for rows.Next() {
		p, err := scanPersona(rows)
		if err != nil {
			return nil, err
		}
		key := personaIdentityKey(p.Title, p.Content)
		if seen[key] {
			continue
		}
		seen[key] = true
		personas = append(personas, p)
	}
	return personas, rows.Err()
}

func findPersonaByIdentity(title, content string) (PersonaProfile, bool, error) {
	want := personaIdentityKey(title, content)
	rows, err := db.DB.Query(
		`SELECT id, title, content, COALESCE(source_path,''), active, created_at, updated_at
		 FROM persona_profiles
		 ORDER BY active DESC, id DESC`,
	)
	if err != nil {
		return PersonaProfile{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPersona(rows)
		if err != nil {
			return PersonaProfile{}, false, err
		}
		if personaIdentityKey(p.Title, p.Content) == want {
			return p, true, rows.Err()
		}
	}
	return PersonaProfile{}, false, rows.Err()
}

func personaIdentityKey(title, content string) string {
	return normalizePersonaIdentityText(title)
}

func normalizePersonaIdentityText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func GetActivePersona() (PersonaProfile, bool, error) {
	row := db.DB.QueryRow(
		`SELECT id, title, content, COALESCE(source_path,''), active, created_at, updated_at
		 FROM persona_profiles WHERE active=1 ORDER BY id DESC LIMIT 1`,
	)
	p, err := scanPersona(row)
	if err == sql.ErrNoRows {
		return PersonaProfile{}, false, nil
	}
	if err != nil {
		return PersonaProfile{}, false, err
	}
	return p, true, nil
}

func GetPersona(id int64) (PersonaProfile, error) {
	row := db.DB.QueryRow(
		`SELECT id, title, content, COALESCE(source_path,''), active, created_at, updated_at
		 FROM persona_profiles WHERE id=?`, id,
	)
	return scanPersona(row)
}

func ActivatePersona(id int64) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE persona_profiles SET active=0`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE persona_profiles SET active=1, updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func DeactivatePersona(id int64) error {
	res, err := db.DB.Exec(`UPDATE persona_profiles SET active=0, updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func DeletePersona(id int64) error {
	target, err := GetPersona(id)
	if err != nil {
		return err
	}
	want := personaIdentityKey(target.Title, target.Content)
	rows, err := db.DB.Query(
		`SELECT id, title, content, COALESCE(source_path,''), active, created_at, updated_at
		 FROM persona_profiles`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		p, err := scanPersona(rows)
		if err != nil {
			return err
		}
		if personaIdentityKey(p.Title, p.Content) == want {
			ids = append(ids, p.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return sql.ErrNoRows
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]interface{}, 0, len(ids))
	for _, deleteID := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, deleteID)
	}
	_, err = db.DB.Exec(`DELETE FROM persona_profiles WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanPersona(row rowScanner) (PersonaProfile, error) {
	var p PersonaProfile
	var active int
	err := row.Scan(&p.ID, &p.Title, &p.Content, &p.SourcePath, &active, &p.CreatedAt, &p.UpdatedAt)
	p.Active = active == 1
	return p, err
}

func CreateKnowledgeDoc(title, sourcePath, text, status, errText string) (KnowledgeDoc, error) {
	title = defaultTitle(title, "历史文档")
	if status == "" {
		status = "pending"
	}
	res, err := db.DB.Exec(
		`INSERT INTO knowledge_docs (title, source_path, content_text, status, error) VALUES (?, ?, ?, ?, ?)`,
		title, sourcePath, text, status, errText,
	)
	if err != nil {
		return KnowledgeDoc{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return KnowledgeDoc{}, err
	}
	return GetKnowledgeDoc(id)
}

func UpdateKnowledgeDocStatus(id int64, status, errText string) error {
	_, err := db.DB.Exec(
		`UPDATE knowledge_docs SET status=?, error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, errText, id,
	)
	return err
}

func GetKnowledgeDoc(id int64) (KnowledgeDoc, error) {
	row := db.DB.QueryRow(
		`SELECT id, title, COALESCE(source_path,''), COALESCE(content_text,''), status, COALESCE(error,''), created_at, updated_at
		 FROM knowledge_docs WHERE id=?`, id,
	)
	return scanKnowledgeDoc(row)
}

func ListKnowledgeDocs() ([]KnowledgeDoc, error) {
	rows, err := db.DB.Query(
		`SELECT id, title, COALESCE(source_path,''), COALESCE(content_text,''), status, COALESCE(error,''), created_at, updated_at
		 FROM knowledge_docs ORDER BY id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []KnowledgeDoc
	for rows.Next() {
		d, err := scanKnowledgeDoc(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func DeleteKnowledgeDoc(id int64) (KnowledgeDoc, error) {
	doc, err := GetKnowledgeDoc(id)
	if err != nil {
		return KnowledgeDoc{}, err
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return KnowledgeDoc{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM memory_chunks WHERE source_doc_id=?`, id); err != nil {
		return KnowledgeDoc{}, err
	}
	result, err := tx.Exec(`DELETE FROM knowledge_docs WHERE id=?`, id)
	if err != nil {
		return KnowledgeDoc{}, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return KnowledgeDoc{}, affectedErr
	} else if affected == 0 {
		return KnowledgeDoc{}, sql.ErrNoRows
	}
	if err = tx.Commit(); err != nil {
		return KnowledgeDoc{}, err
	}
	return doc, nil
}

func CountChunksBySourceDoc(sourceType string, sourceDocID int64) (int, error) {
	var count int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM memory_chunks WHERE active=1 AND source_type=? AND source_doc_id=?`,
		sourceType, sourceDocID,
	).Scan(&count)
	return count, err
}

func ListChunksBySourceDoc(sourceType string, sourceDocID int64, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := db.DB.Query(
		`SELECT `+chunkSelectColumns+`
		 FROM memory_chunks
		 WHERE active=1 AND source_type=? AND source_doc_id=?
		 ORDER BY id ASC LIMIT ?`,
		sourceType, sourceDocID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func scanKnowledgeDoc(row rowScanner) (KnowledgeDoc, error) {
	var d KnowledgeDoc
	err := row.Scan(&d.ID, &d.Title, &d.SourcePath, &d.ContentText, &d.Status, &d.Error, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func CreateAttachment(conversationID int64, originalName, filePath, url, mime string, size int64) (Attachment, error) {
	return CreateAttachmentWithHash(conversationID, originalName, filePath, url, mime, size, "")
}

func CreateAttachmentWithHash(conversationID int64, originalName, filePath, url, mime string, size int64, contentHash string) (Attachment, error) {
	return createAttachment(conversationID, "image", originalName, filePath, url, mime, size, contentHash)
}

func CreateFileAttachment(conversationID int64, originalName, filePath, url, mime string, size int64, contentHash string) (Attachment, error) {
	return createAttachment(conversationID, "file", originalName, filePath, url, mime, size, contentHash)
}

func createAttachment(conversationID int64, kind, originalName, filePath, url, mime string, size int64, contentHash string) (Attachment, error) {
	res, err := db.DB.Exec(
		`INSERT INTO message_attachments (conversation_id, kind, original_name, file_path, url, mime_type, size_bytes, content_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		conversationID, kind, originalName, filePath, url, mime, size, contentHash,
	)
	if err != nil {
		return Attachment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Attachment{}, err
	}
	return GetAttachment(id)
}

func GetAttachment(id int64) (Attachment, error) {
	var a Attachment
	err := db.DB.QueryRow(
		`SELECT id, conversation_id, COALESCE(message_id,0), kind, COALESCE(original_name,''), file_path, url, mime_type, size_bytes, COALESCE(content_hash,''), created_at
		 FROM message_attachments WHERE id=?`, id,
	).Scan(&a.ID, &a.ConversationID, &a.MessageID, &a.Kind, &a.OriginalName, &a.FilePath, &a.URL, &a.MimeType, &a.SizeBytes, &a.ContentHash, &a.CreatedAt)
	return a, err
}

func GetAttachments(ids []int64) ([]Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := db.DB.Query(
		`SELECT id, conversation_id, COALESCE(message_id,0), kind, COALESCE(original_name,''), file_path, url, mime_type, size_bytes, COALESCE(content_hash,''), created_at
		 FROM message_attachments WHERE id IN (`+strings.Join(placeholders, ",")+`) ORDER BY id ASC`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attachments []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.ConversationID, &a.MessageID, &a.Kind, &a.OriginalName, &a.FilePath, &a.URL, &a.MimeType, &a.SizeBytes, &a.ContentHash, &a.CreatedAt); err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

func LinkAttachments(messageID int64, attachmentIDs []int64, conversationID int64) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(attachmentIDs))
	args := []interface{}{messageID, conversationID}
	for _, id := range attachmentIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := db.DB.Exec(
		`UPDATE message_attachments SET message_id=? WHERE conversation_id=? AND id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	return err
}

func FindReusableAttachment(contentHash string) (Attachment, bool, error) {
	contentHash = strings.TrimSpace(contentHash)
	if contentHash == "" {
		return Attachment{}, false, nil
	}
	var a Attachment
	err := db.DB.QueryRow(`SELECT id, conversation_id, COALESCE(message_id,0), kind, COALESCE(original_name,''), file_path, url, mime_type, size_bytes, COALESCE(content_hash,''), created_at FROM message_attachments WHERE content_hash=? ORDER BY id DESC LIMIT 1`, contentHash).
		Scan(&a.ID, &a.ConversationID, &a.MessageID, &a.Kind, &a.OriginalName, &a.FilePath, &a.URL, &a.MimeType, &a.SizeBytes, &a.ContentHash, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, err
	}
	if _, err := os.Stat(a.FilePath); err != nil {
		return Attachment{}, false, nil
	}
	return a, true, nil
}

func CleanupOrphanAttachments(olderThan time.Duration) ([]string, error) {
	if olderThan <= 0 {
		olderThan = 24 * time.Hour
	}
	cutoff := time.Now().Add(-olderThan).UTC().Format("2006-01-02 15:04:05")
	rows, err := db.DB.Query(`SELECT id,file_path FROM message_attachments WHERE message_id IS NULL AND created_at<?`, cutoff)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		paths = append(paths, path)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	marks := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	if _, err := db.DB.Exec(`DELETE FROM message_attachments WHERE id IN (`+strings.Join(marks, ",")+")", args...); err != nil {
		return nil, err
	}
	removeUnreferencedFiles(paths)
	return paths, nil
}

func RemoveMissingAttachmentRecords() (int, error) {
	rows, err := db.DB.Query(`SELECT id,file_path FROM message_attachments`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var missing []int64
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return 0, err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return 0, rows.Err()
	}
	marks := make([]string, len(missing))
	args := make([]interface{}, len(missing))
	for i, id := range missing {
		marks[i] = "?"
		args[i] = id
	}
	_, err = db.DB.Exec(`DELETE FROM message_attachments WHERE id IN (`+strings.Join(marks, ",")+")", args...)
	return len(missing), err
}

func AttachmentPathReferenced(path string) (bool, error) {
	var n int
	err := db.DB.QueryRow(`SELECT COUNT(*) FROM message_attachments WHERE file_path=?`, path).Scan(&n)
	return n > 0, err
}

func attachmentPathsForConversation(conversationID int64) ([]string, error) {
	rows, err := db.DB.Query(`SELECT DISTINCT file_path FROM message_attachments WHERE conversation_id=?`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
func removeUnreferencedFiles(paths []string) {
	for _, p := range paths {
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM message_attachments WHERE file_path=?`, p).Scan(&n); err == nil && n == 0 {
			_ = os.Remove(p)
		}
	}
}

func CreateSticker(name, filePath, url, mime, tags, description, mood string, needsReview bool) (Sticker, error) {
	name = defaultTitle(name, "表情包")
	res, err := db.DB.Exec(
		`INSERT INTO stickers (name, file_path, url, mime_type, tags, description, mood, enabled, needs_review)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		name, filePath, url, mime, tags, description, mood, boolInt(needsReview),
	)
	if err != nil {
		return Sticker{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Sticker{}, err
	}
	return GetSticker(id)
}

func GetSticker(id int64) (Sticker, error) {
	row := db.DB.QueryRow(
		`SELECT id, name, file_path, url, mime_type, COALESCE(tags,''), COALESCE(description,''), COALESCE(mood,''), enabled, needs_review, created_at, updated_at
		 FROM stickers WHERE id=?`, id,
	)
	return scanSticker(row)
}

func UpdateStickerMetadata(id int64, name, tags, description, mood string) (Sticker, error) {
	name = defaultTitle(name, "表情包")
	_, err := db.DB.Exec(
		`UPDATE stickers
		 SET name=?, tags=?, description=?, mood=?, needs_review=0, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		name, strings.TrimSpace(tags), strings.TrimSpace(description), strings.TrimSpace(mood), id,
	)
	if err != nil {
		return Sticker{}, err
	}
	return GetSticker(id)
}

func DisableSticker(id int64) error {
	_, err := db.DB.Exec(
		`UPDATE stickers SET enabled=0, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		id,
	)
	return err
}

func SearchStickers(query, mood string, limit int) ([]Sticker, error) {
	if limit <= 0 {
		limit = 8
	}
	like := "%" + strings.TrimSpace(query) + "%"
	moodLike := "%" + strings.TrimSpace(mood) + "%"
	if strings.TrimSpace(query) == "" {
		like = "%"
	}
	if strings.TrimSpace(mood) == "" {
		moodLike = "%"
	}
	rows, err := db.DB.Query(
		`SELECT id, name, file_path, url, mime_type, COALESCE(tags,''), COALESCE(description,''), COALESCE(mood,''), enabled, needs_review, created_at, updated_at
		 FROM stickers
		 WHERE enabled=1 AND (name LIKE ? OR tags LIKE ? OR description LIKE ?) AND mood LIKE ?
		 ORDER BY needs_review ASC, id DESC LIMIT ?`,
		like, like, like, moodLike, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stickers []Sticker
	for rows.Next() {
		s, err := scanSticker(rows)
		if err != nil {
			return nil, err
		}
		stickers = append(stickers, s)
	}
	return stickers, rows.Err()
}

func ListStickers() ([]Sticker, error) {
	rows, err := db.DB.Query(
		`SELECT id, name, file_path, url, mime_type, COALESCE(tags,''), COALESCE(description,''), COALESCE(mood,''), enabled, needs_review, created_at, updated_at
		 FROM stickers
		 WHERE enabled=1
		 ORDER BY id DESC LIMIT 200`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stickers []Sticker
	for rows.Next() {
		s, err := scanSticker(rows)
		if err != nil {
			return nil, err
		}
		stickers = append(stickers, s)
	}
	return stickers, rows.Err()
}

func scanSticker(row rowScanner) (Sticker, error) {
	var s Sticker
	var enabled, needsReview int
	err := row.Scan(&s.ID, &s.Name, &s.FilePath, &s.URL, &s.MimeType, &s.Tags, &s.Description, &s.Mood, &enabled, &needsReview, &s.CreatedAt, &s.UpdatedAt)
	s.Enabled = enabled == 1
	s.NeedsReview = needsReview == 1
	return s, err
}

func defaultTitle(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func tokenize(s string) []string {
	replacer := strings.NewReplacer(
		"，", " ", "。", " ", "！", " ", "？", " ",
		"、", " ", "：", " ", "；", " ", "，", " ",
		"的", " ", "了", " ", "是", " ", "在", " ",
		"我", " ", "你", " ", "他", " ", "她", " ", "它", " ",
		"什么", " ", "之前", " ", "今天", " ", "吗", " ",
	)
	s = replacer.Replace(s)

	var words []string
	for _, p := range strings.Fields(s) {
		pr := []rune(p)
		if len(pr) < 2 {
			continue
		}
		words = append(words, p)
		for i := 0; i+2 <= len(pr); i++ {
			words = append(words, string(pr[i:i+2]))
		}
	}

	seen := map[string]bool{}
	var deduped []string
	for _, w := range words {
		if seen[w] {
			continue
		}
		seen[w] = true
		deduped = append(deduped, w)
	}
	if len(deduped) > 20 {
		return deduped[:20]
	}
	return deduped
}

func tokenizeSearchQuery(s string) []string {
	replacer := strings.NewReplacer(
		"，", " ", "。", " ", "！", " ", "？", " ", "、", " ", "：", " ", "；", " ",
		",", " ", ".", " ", "!", " ", "?", " ", ":", " ", ";", " ", "\n", " ", "\t", " ",
	)
	weak := map[string]bool{
		"昨天": true, "今天": true, "之前": true, "以前": true, "刚才": true, "当前": true,
		"这个": true, "那个": true, "相关": true, "记录": true, "一条": true, "内容": true,
		"什么": true, "怎么": true, "是否": true, "有没有": true, "情况": true, "怎么样": true,
		"还记": true, "记得": true, "得阿": true,
	}
	var terms []string
	seen := map[string]bool{}
	fields := strings.Fields(replacer.Replace(strings.ToLower(s)))
	for _, field := range fields {
		field = strings.Trim(field, "()（）[]【】\"'“”‘’~～")
		if utf8.RuneCountInString(field) < 2 || weak[field] || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	if len(fields) == 1 && utf8.RuneCountInString(fields[0]) > 4 {
		runes := []rune(fields[0])
		for i := 0; i+2 <= len(runes); i++ {
			term := string(runes[i : i+2])
			if !weak[term] && !seen[term] {
				seen[term] = true
				terms = append(terms, term)
			}
		}
	}
	if len(terms) == 0 {
		return tokenize(s)
	}
	if len(terms) > 20 {
		terms = terms[:20]
	}
	return terms
}

func vagueMemoryQuery(query string) bool {
	normalized := strings.ToLower(strings.TrimSpace(query))
	for _, marker := range []string{"那个", "这个", "那件事", "这件事", "前面说的", "之前说的", "刚才说的", "怎么样"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
