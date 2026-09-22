package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"myapp/internal/db"
)

type daysMatterEvent struct {
	ID         int64  `json:"id"`
	EventName  string `json:"event_name"`
	EventDate  string `json:"event_date"`
	Direction  string `json:"direction"`
	ImageURL   string `json:"image_url"`
	Note       string `json:"note"`
	IsFavorite bool   `json:"is_favorite"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

const daysMatterSelect = `SELECT id,event_name,event_date,direction,image_url,note,is_favorite,created_at,updated_at FROM days_matter_events`

func handleDaysMatter(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listDaysMatter(w)
	case http.MethodPost:
		createDaysMatter(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleDaysMatterRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/days-matter/"), "/")
	if path == "upload" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		uploadDaysMatterImage(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "id无效"})
		return
	}
	if len(parts) == 2 && parts[1] == "favorite" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		favoriteDaysMatter(w, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getDaysMatter(w, id)
	case http.MethodPut:
		updateDaysMatter(w, r, id)
	case http.MethodDelete:
		deleteDaysMatter(w, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func scanDaysMatter(scanner interface{ Scan(...interface{}) error }) (daysMatterEvent, error) {
	var event daysMatterEvent
	var favorite int
	err := scanner.Scan(&event.ID, &event.EventName, &event.EventDate, &event.Direction, &event.ImageURL, &event.Note, &favorite, &event.CreatedAt, &event.UpdatedAt)
	event.IsFavorite = favorite != 0
	return event, err
}

func listDaysMatter(w http.ResponseWriter) {
	rows, err := db.DB.Query(daysMatterSelect + ` ORDER BY CASE WHEN event_date>=date('now','+8 hours') THEN 0 ELSE 1 END, CASE WHEN event_date>=date('now','+8 hours') THEN event_date END ASC, CASE WHEN event_date<date('now','+8 hours') THEN event_date END DESC, id DESC`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	events := []daysMatterEvent{}
	for rows.Next() {
		event, scanErr := scanDaysMatter(rows)
		if scanErr != nil {
			jsonResp(w, 500, map[string]string{"error": scanErr.Error()})
			return
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"events": events})
}

func decodeDaysMatter(w http.ResponseWriter, r *http.Request) (daysMatterEvent, error) {
	var event daysMatterEvent
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, errors.New("请求格式错误")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return event, errors.New("请求格式错误")
	}
	event.EventName = strings.TrimSpace(event.EventName)
	event.EventDate = strings.TrimSpace(event.EventDate)
	event.ImageURL = strings.TrimSpace(event.ImageURL)
	event.Note = strings.TrimSpace(event.Note)
	if event.EventName == "" {
		return event, errors.New("事件名不能为空")
	}
	if len([]rune(event.EventName)) > 80 {
		return event, errors.New("事件名不能超过80字")
	}
	if _, err := time.Parse("2006-01-02", event.EventDate); err != nil {
		return event, errors.New("日期无效")
	}
	if event.Direction != "count_up" && event.Direction != "count_down" {
		return event, errors.New("计时方向无效")
	}
	if event.ImageURL != "" {
		if _, ok := daysMatterImageName(event.ImageURL); !ok {
			return event, errors.New("配图地址无效")
		}
	}
	if len([]rune(event.Note)) > 500 {
		return event, errors.New("备注不能超过500字")
	}
	return event, nil
}

func createDaysMatter(w http.ResponseWriter, r *http.Request) {
	event, err := decodeDaysMatter(w, r)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if event.IsFavorite {
		if _, err = tx.Exec(`UPDATE days_matter_events SET is_favorite=0,updated_at=CURRENT_TIMESTAMP WHERE is_favorite=1`); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	res, err := tx.Exec(`INSERT INTO days_matter_events(event_name,event_date,direction,image_url,note,is_favorite) VALUES(?,?,?,?,?,?)`, event.EventName, event.EventDate, event.Direction, event.ImageURL, event.Note, boolInt(event.IsFavorite))
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	if err = tx.Commit(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	getDaysMatter(w, id)
}

func getDaysMatter(w http.ResponseWriter, id int64) {
	event, err := scanDaysMatter(db.DB.QueryRow(daysMatterSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "事件不存在"})
		return
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"event": event})
}

func updateDaysMatter(w http.ResponseWriter, r *http.Request, id int64) {
	event, err := decodeDaysMatter(w, r)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var oldImage string
	if err = db.DB.QueryRow(`SELECT image_url FROM days_matter_events WHERE id=?`, id).Scan(&oldImage); errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "事件不存在"})
		return
	} else if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if event.IsFavorite {
		if _, err = tx.Exec(`UPDATE days_matter_events SET is_favorite=0,updated_at=CURRENT_TIMESTAMP WHERE is_favorite=1 AND id<>?`, id); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	_, err = tx.Exec(`UPDATE days_matter_events SET event_name=?,event_date=?,direction=?,image_url=?,note=?,is_favorite=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, event.EventName, event.EventDate, event.Direction, event.ImageURL, event.Note, boolInt(event.IsFavorite), id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if err = tx.Commit(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if oldImage != "" && oldImage != event.ImageURL {
		removeDaysMatterImage(oldImage)
	}
	getDaysMatter(w, id)
}

func favoriteDaysMatter(w http.ResponseWriter, id int64) {
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	var current int
	if err = tx.QueryRow(`SELECT is_favorite FROM days_matter_events WHERE id=?`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "事件不存在"})
		return
	} else if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	next := current == 0
	if next {
		if _, err = tx.Exec(`UPDATE days_matter_events SET is_favorite=0,updated_at=CURRENT_TIMESTAMP WHERE is_favorite=1`); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	if _, err = tx.Exec(`UPDATE days_matter_events SET is_favorite=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, boolInt(next), id); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if err = tx.Commit(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"id": id, "is_favorite": next})
}

func deleteDaysMatter(w http.ResponseWriter, id int64) {
	var imageURL string
	if err := db.DB.QueryRow(`SELECT image_url FROM days_matter_events WHERE id=?`, id).Scan(&imageURL); errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "事件不存在"})
		return
	} else if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if _, err := db.DB.Exec(`DELETE FROM days_matter_events WHERE id=?`, id); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	removeDaysMatterImage(imageURL)
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func uploadDaysMatterImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
	_, imageURL, mimeType, size, _, err := saveMultipartFile(r, "file", "days_matter", true)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"image": map[string]interface{}{"url": imageURL, "mime_type": mimeType, "size_bytes": size}})
}

func removeDaysMatterImage(imageURL string) {
	name, ok := daysMatterImageName(imageURL)
	if !ok {
		return
	}
	var references int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM days_matter_events WHERE image_url=?`, imageURL).Scan(&references); err != nil || references != 0 {
		return
	}
	_ = os.Remove(filepath.Join(uploadRoot, "days_matter", name))
}

func daysMatterImageName(imageURL string) (string, bool) {
	parsed, err := url.PathUnescape(strings.TrimSpace(imageURL))
	const prefix = "/uploads/days_matter/"
	if err != nil || !strings.HasPrefix(parsed, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(parsed, prefix)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", false
	}
	return name, true
}
