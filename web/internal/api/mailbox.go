package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"myapp/internal/db"
)

type mailboxMessage struct {
	ID        int64  `json:"id"`
	Sender    string `json:"sender"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

const mailboxBrowserSender = "Xane"

type createMailboxRequest struct {
	Content string `json:"content"`
}

func mailboxMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	jsonResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "请求方法不支持"})
}

func handleMailbox(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listMailboxMessages(w, r)
	case http.MethodPost:
		createMailboxMessage(w, r)
	default:
		mailboxMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func handleMailboxRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/mailbox/"), "/")
	if path == "dates" {
		if r.Method != http.MethodGet {
			mailboxMethodNotAllowed(w, http.MethodGet)
			return
		}
		mailboxDates(w)
		return
	}
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil || id <= 0 {
		jsonResp(w, 400, map[string]string{"error": "id无效"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		getMailboxMessage(w, id)
	case http.MethodDelete:
		deleteMailboxMessage(w, r, id)
	default:
		mailboxMethodNotAllowed(w, http.MethodGet+", "+http.MethodDelete)
	}
}

func getMailboxMessage(w http.ResponseWriter, id int64) {
	var m mailboxMessage
	err := db.DB.QueryRow(`SELECT id,sender,content,created_at FROM mailbox_messages WHERE id=?`, id).Scan(&m.ID, &m.Sender, &m.Content, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "留言不存在"})
		return
	}
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"message": m})
}

func listMailboxMessages(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if len([]rune(query)) > 80 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "搜索词不能超过80字"})
		return
	}
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "日期格式无效"})
			return
		}
	}
	rows, err := db.DB.Query(`SELECT id,sender,content,created_at FROM mailbox_messages
		WHERE (?='' OR content LIKE '%'||?||'%') AND (?='' OR date(created_at,'+8 hours')=?)
		ORDER BY created_at DESC,id DESC LIMIT 500`, query, query, date, date)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []mailboxMessage{}
	for rows.Next() {
		var m mailboxMessage
		if err := rows.Scan(&m.ID, &m.Sender, &m.Content, &m.CreatedAt); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"messages": items, "query": query, "date": date})
}

func createMailboxMessage(w http.ResponseWriter, r *http.Request) {
	var in createMailboxRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		jsonResp(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		jsonResp(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	if n := len([]rune(in.Content)); n == 0 || n > 200 {
		jsonResp(w, 400, map[string]string{"error": "留言须为 1 至 200 字"})
		return
	}
	res, err := db.DB.Exec(`INSERT INTO mailbox_messages(sender,content) VALUES(?,?)`, mailboxBrowserSender, in.Content)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	var m mailboxMessage
	err = db.DB.QueryRow(`SELECT id,sender,content,created_at FROM mailbox_messages WHERE id=?`, id).Scan(&m.ID, &m.Sender, &m.Content, &m.CreatedAt)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"message": m})
}

func deleteMailboxMessage(w http.ResponseWriter, _ *http.Request, id int64) {
	res, err := db.DB.Exec(`DELETE FROM mailbox_messages WHERE id=? AND sender=?`, id, mailboxBrowserSender)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	deleted, _ := res.RowsAffected()
	if deleted == 0 {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "留言不存在或不可删除"})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func mailboxDates(w http.ResponseWriter) {
	rows, err := db.DB.Query(`SELECT date(created_at,'+8 hours'),COUNT(*) FROM mailbox_messages GROUP BY date(created_at,'+8 hours') ORDER BY 1 DESC`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := map[string]int{}
	for rows.Next() {
		var date string
		var count int
		if err := rows.Scan(&date, &count); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		items[date] = count
	}
	jsonResp(w, 200, map[string]interface{}{"dates": items})
}
