package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"myapp/internal/db"
)

type note struct {
	ID             int64    `json:"id"`
	Content        string   `json:"content"`
	Tags           []string `json:"tags"`
	CreatedAt      string   `json:"created_at"`
	Status         string   `json:"status"`
	ConversationID *int64   `json:"conversation_id,omitempty"`
	Author         string   `json:"author"`
	Pinned         bool     `json:"pinned"`
	Deleted        bool     `json:"deleted"`
}

func handleNotes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listNotes(w, r)
	case http.MethodPost:
		createNote(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleNoteRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/notes/"), "/")
	if path == "tags" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		listNoteTags(w)
		return
	}
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil || id <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "id无效"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		getNote(w, id)
	case http.MethodPut:
		updateNote(w, r, id)
	case http.MethodDelete:
		softDeleteNote(w, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func validNoteStatus(v string) bool { return v == "pending" || v == "done" || v == "archived" }
func validNoteAuthor(v string) bool { return v == "user" || v == "ai" }

func cleanNote(in note, creating bool) (note, error) {
	in.Content = strings.TrimSpace(in.Content)
	if n := len([]rune(in.Content)); n == 0 || n > 500 {
		return in, errors.New("纸条内容须为 1 至 500 字")
	}
	if creating && in.Status == "" {
		in.Status = "pending"
	}
	if !validNoteStatus(in.Status) {
		return in, errors.New("纸条状态无效")
	}
	if creating && in.Author == "" {
		in.Author = "user"
	}
	if !creating && in.Author == "" {
		in.Author = "user"
	}
	if !validNoteAuthor(in.Author) {
		return in, errors.New("纸条作者无效")
	}
	seen := map[string]bool{}
	tags := make([]string, 0, len(in.Tags))
	for _, raw := range in.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || seen[tag] {
			continue
		}
		if len([]rune(tag)) > 30 {
			return in, errors.New("单个标签不能超过 30 字")
		}
		seen[tag] = true
		tags = append(tags, tag)
		if len(tags) > 20 {
			return in, errors.New("标签不能超过 20 个")
		}
	}
	in.Tags = tags
	if in.ConversationID != nil {
		var exists int
		if *in.ConversationID <= 0 || db.DB.QueryRow(`SELECT COUNT(*) FROM conversations WHERE id=?`, *in.ConversationID).Scan(&exists) != nil || exists == 0 {
			return in, errors.New("关联对话不存在")
		}
	}
	return in, nil
}

func decodeNote(w http.ResponseWriter, r *http.Request, creating bool) (note, error) {
	var in note
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return in, errors.New("请求格式错误")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return in, errors.New("请求格式错误")
	}
	return cleanNote(in, creating)
}

func scanNote(scanner interface{ Scan(...interface{}) error }) (note, error) {
	var n note
	var tags string
	var conversationID sql.NullInt64
	var pinned, deleted int
	err := scanner.Scan(&n.ID, &n.Content, &tags, &n.CreatedAt, &n.Status, &conversationID, &n.Author, &pinned, &deleted)
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal([]byte(tags), &n.Tags); err != nil {
		n.Tags = []string{}
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	if conversationID.Valid {
		n.ConversationID = &conversationID.Int64
	}
	n.Pinned, n.Deleted = pinned != 0, deleted != 0
	return n, nil
}

const noteSelect = `SELECT id,content,tags,created_at,status,conversation_id,author,pinned,deleted FROM notes`

func listNotes(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	author := strings.TrimSpace(r.URL.Query().Get("author"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	archived := r.URL.Query().Get("archived") == "1"
	if len([]rune(q)) > 500 {
		jsonResp(w, 400, map[string]string{"error": "搜索词不能超过 500 字"})
		return
	}
	if author != "" && !validNoteAuthor(author) {
		jsonResp(w, 400, map[string]string{"error": "作者筛选无效"})
		return
	}
	if status != "" && !validNoteStatus(status) {
		jsonResp(w, 400, map[string]string{"error": "状态筛选无效"})
		return
	}
	where := []string{"deleted=0", "(?='' OR content LIKE '%'||?||'%')", "(?='' OR author=?)"}
	args := []interface{}{q, q, author, author}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	} else if archived {
		where = append(where, "status='archived'")
	} else {
		where = append(where, "status<>'archived'")
	}
	filterTags := r.URL.Query()["tag"]
	if len(filterTags) > 10 {
		jsonResp(w, 400, map[string]string{"error": "标签筛选不能超过 10 个"})
		return
	}
	for _, tag := range filterTags {
		tag = strings.TrimSpace(tag)
		if len([]rune(tag)) > 30 {
			jsonResp(w, 400, map[string]string{"error": "标签筛选不能超过 30 字"})
			return
		}
		if tag != "" {
			where = append(where, "EXISTS (SELECT 1 FROM json_each(notes.tags) WHERE json_each.value=?)")
			args = append(args, tag)
		}
	}
	rows, err := db.DB.Query(noteSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY pinned DESC,created_at DESC,id DESC LIMIT 500`, args...)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"notes": items})
}

func createNote(w http.ResponseWriter, r *http.Request) {
	n, err := decodeNote(w, r, true)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	tags, _ := json.Marshal(n.Tags)
	res, err := db.DB.Exec(`INSERT INTO notes(content,tags,status,conversation_id,author,pinned) VALUES(?,?,?,?,?,?)`, n.Content, string(tags), n.Status, n.ConversationID, n.Author, boolInt(n.Pinned))
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	getNote(w, id)
}

func getNote(w http.ResponseWriter, id int64) {
	n, err := scanNote(db.DB.QueryRow(noteSelect+` WHERE id=? AND deleted=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "纸条不存在"})
		return
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"note": n})
}

func updateNote(w http.ResponseWriter, r *http.Request, id int64) {
	n, err := decodeNote(w, r, false)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var author string
	if err := db.DB.QueryRow(`SELECT author FROM notes WHERE id=? AND deleted=0`, id).Scan(&author); errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "纸条不存在"})
		return
	} else if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	n.Author = author
	tags, _ := json.Marshal(n.Tags)
	res, err := db.DB.Exec(`UPDATE notes SET content=?,tags=?,status=?,conversation_id=?,author=?,pinned=? WHERE id=? AND deleted=0`, n.Content, string(tags), n.Status, n.ConversationID, n.Author, boolInt(n.Pinned), id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		jsonResp(w, 404, map[string]string{"error": "纸条不存在"})
		return
	}
	getNote(w, id)
}

func softDeleteNote(w http.ResponseWriter, id int64) {
	res, err := db.DB.Exec(`UPDATE notes SET deleted=1 WHERE id=? AND deleted=0`, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		jsonResp(w, 404, map[string]string{"error": "纸条不存在"})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func listNoteTags(w http.ResponseWriter) {
	rows, err := db.DB.Query(`SELECT DISTINCT value FROM notes,json_each(notes.tags) WHERE deleted=0 AND trim(value)<>'' ORDER BY value LIMIT 500`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"tags": tags})
}
