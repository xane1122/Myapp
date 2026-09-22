package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"myapp/internal/db"
)

// StartBellMonitor delivers reminders independently from chat persistence.
func StartBellMonitor() {
	go func() {
		processDueBells(time.Now().In(wakeLocation), sendBellPush)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			processDueBells(time.Now().In(wakeLocation), sendBellPush)
		}
	}()
}

type dueBell struct {
	ID            int64
	Title         string
	Note          string
	PushTarget    string
	AssistantName string
}

func activeDeadlinesSystemPrompt(now time.Time) string {
	now = now.In(wakeLocation)
	rows, err := db.DB.Query(`SELECT title,note,deadline FROM bells WHERE enabled=1 AND deadline<>'' AND deadline>=? ORDER BY deadline,id`, now.Format("2006-01-02"))
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var title, note, deadline string
		if rows.Scan(&title, &note, &deadline) != nil {
			continue
		}
		line := "- " + deadline + "：" + strings.TrimSpace(title)
		if note = strings.TrimSpace(note); note != "" {
			line += "（" + note + "）"
		}
		if due, parseErr := time.ParseInLocation("2006-01-02", deadline, wakeLocation); parseErr == nil {
			days := int(due.Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, wakeLocation)).Hours() / 24)
			if days == 0 {
				line += "，今天截止"
			} else {
				line += fmt.Sprintf("，还有%d天", days)
			}
		}
		lines = append(lines, line)
	}
	if rows.Err() != nil {
		return ""
	}
	if len(lines) == 0 {
		return ""
	}
	return "【当前活跃截止日期（DDL）】\n" + strings.Join(lines, "\n") + "\n可在相关日常聊天中自然提醒，不要每轮机械重复。"
}

func processDueBells(now time.Time, deliver func(dueBell)) {
	now = now.In(wakeLocation).Truncate(time.Minute)
	_, _ = db.DB.Exec(`UPDATE bells SET enabled=0,updated_at=CURRENT_TIMESTAMP WHERE enabled=1 AND deadline<>'' AND deadline < ?`, now.Format("2006-01-02"))
	rows, err := db.DB.Query(`SELECT b.id,b.title,b.note,b.frequency,b.reminder_time,b.deadline,b.reminder_interval,b.push_target,b.created_at,COALESCE(b.assistant_name,'')
		FROM bells b WHERE b.enabled=1 AND (b.deadline='' OR b.deadline>=?)
		AND NOT EXISTS(SELECT 1 FROM bell_checkins c WHERE c.bell_id=b.id)`, now.Format("2006-01-02"))
	if err != nil {
		return
	}
	defer rows.Close()
	type candidate struct {
		dueBell
		frequency, reminderTime, deadline, interval, createdAt string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.ID, &c.Title, &c.Note, &c.frequency, &c.reminderTime, &c.deadline, &c.interval, &c.PushTarget, &c.createdAt, &c.AssistantName) == nil {
			candidates = append(candidates, c)
		}
	}
	for _, c := range candidates {
		scheduled, ok := bellScheduledAt(c.frequency, c.reminderTime, c.interval, c.createdAt, now)
		if !ok {
			continue
		}
		res, err := db.DB.Exec(`INSERT OR IGNORE INTO bell_deliveries(bell_id,scheduled_at) VALUES(?,?)`, c.ID, scheduled.Format("2006-01-02 15:04"))
		if err != nil {
			continue
		}
		inserted, _ := res.RowsAffected()
		if inserted == 1 {
			deliver(c.dueBell)
		}
	}
}

func bellScheduledAt(frequency, reminderTime, interval, createdAt string, now time.Time) (time.Time, bool) {
	clock, err := time.ParseInLocation("15:04", reminderTime, wakeLocation)
	if err != nil || !bellOccursOn(frequency, createdAt, now) {
		return time.Time{}, false
	}
	first := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, wakeLocation)
	if now.Before(first) {
		return time.Time{}, false
	}
	if interval == "" {
		return first, now.Equal(first)
	}
	d, ok := parseBellInterval(interval)
	if !ok {
		return time.Time{}, false
	}
	elapsed := now.Sub(first)
	return first.Add((elapsed / d) * d), elapsed%d == 0
}

func bellOccursOn(frequency, createdAt string, now time.Time) bool {
	created, err := time.Parse("2006-01-02 15:04:05", createdAt)
	if err != nil {
		created = now
	} else {
		created = created.In(wakeLocation)
	}
	switch frequency {
	case "daily":
		return true
	case "weekdays":
		return now.Weekday() >= time.Monday && now.Weekday() <= time.Friday
	case "weekly":
		return now.Weekday() == created.Weekday()
	case "monthly":
		return now.Day() == bellMinInt(created.Day(), daysInMonth(now.Year(), now.Month()))
	default:
		return false
	}
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, wakeLocation).Day()
}

func bellMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseBellInterval(value string) (time.Duration, bool) {
	if len(value) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	var unit time.Duration
	switch value[len(value)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	default:
		return 0, false
	}
	d := time.Duration(n) * unit
	return d, d >= time.Minute && d <= 24*time.Hour
}

type bell struct {
	ID               int64  `json:"id"`
	Title            string `json:"title"`
	Note             string `json:"note"`
	Kind             string `json:"kind"`
	Frequency        string `json:"frequency"`
	ReminderTime     string `json:"reminder_time"`
	Deadline         string `json:"deadline"`
	ReminderInterval string `json:"reminder_interval"`
	PushTarget       string `json:"push_target"`
	Enabled          bool   `json:"enabled"`
	Completed        bool   `json:"completed"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func handleBells(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listBells(w)
	case http.MethodPost:
		createBell(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleBellRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/bells/"), "/")
	if path == "stats" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		bellStats(w)
		return
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "id无效"})
		return
	}
	if len(parts) == 2 && parts[1] == "checkins" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		toggleBellCheckin(w, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		getBell(w, id)
	case http.MethodPut:
		updateBell(w, r, id)
	case http.MethodDelete:
		deleteBell(w, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func scanBell(scanner interface{ Scan(...interface{}) error }) (bell, error) {
	var b bell
	var enabled, completed int
	err := scanner.Scan(&b.ID, &b.Title, &b.Note, &b.Kind, &b.Frequency, &b.ReminderTime, &b.PushTarget, &b.Deadline, &b.ReminderInterval, &enabled, &completed, &b.CreatedAt, &b.UpdatedAt)
	b.Enabled, b.Completed = enabled != 0, completed != 0
	return b, err
}

const bellSelect = `SELECT b.id,b.title,b.note,b.kind,b.frequency,b.reminder_time,b.push_target,b.deadline,b.reminder_interval,b.enabled,
	EXISTS(SELECT 1 FROM bell_checkins c WHERE c.bell_id=b.id) AS completed,
b.created_at,b.updated_at FROM bells b`

func listBells(w http.ResponseWriter) {
	rows, err := db.DB.Query(bellSelect + ` ORDER BY completed ASC, b.updated_at DESC`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []bell{}
	for rows.Next() {
		b, e := scanBell(rows)
		if e != nil {
			jsonResp(w, 500, map[string]string{"error": e.Error()})
			return
		}
		items = append(items, b)
	}
	if err := rows.Err(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"bells": items})
}

func decodeBell(w http.ResponseWriter, r *http.Request) (bell, error) {
	var b bell
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return b, errors.New("请求格式错误")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return b, errors.New("请求格式错误")
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Note = strings.TrimSpace(b.Note)
	b.Frequency = strings.TrimSpace(b.Frequency)
	b.ReminderTime = strings.TrimSpace(b.ReminderTime)
	b.Deadline = strings.TrimSpace(b.Deadline)
	b.ReminderInterval = strings.TrimSpace(b.ReminderInterval)
	if b.Title == "" {
		return b, errors.New("标题不能为空")
	}
	if len([]rune(b.Title)) > 80 {
		return b, errors.New("标题不能超过80字")
	}
	if len([]rune(b.Note)) > 500 {
		return b, errors.New("备注不能超过500字")
	}
	if b.Kind != "mine" && b.Kind != "ai" {
		return b, errors.New("类型无效")
	}
	if b.PushTarget != "me" && b.PushTarget != "ai" && b.PushTarget != "both" {
		return b, errors.New("推送目标无效")
	}
	switch b.Frequency {
	case "daily", "weekdays", "weekly", "monthly":
	default:
		return b, errors.New("频率无效")
	}
	if _, err := time.Parse("15:04", b.ReminderTime); err != nil {
		return b, errors.New("提醒时间无效")
	}
	if b.Deadline != "" {
		if _, err := time.Parse("2006-01-02", b.Deadline); err != nil {
			return b, errors.New("截止日期无效")
		}
	}
	if b.ReminderInterval != "" {
		if _, ok := parseBellInterval(b.ReminderInterval); !ok {
			return b, errors.New("提醒间隔无效")
		}
	}
	return b, nil
}

func bellNotificationText(b dueBell) string {
	assistantName := strings.TrimSpace(b.AssistantName)
	if assistantName == "" {
		assistantName = "Rhys"
	}
	body := strings.TrimSpace(b.Note)
	if body == "" {
		body = "该完成「" + b.Title + "」了"
	}
	if b.PushTarget == "ai" {
		return assistantName + " 的铃铛：" + body
	}
	if b.PushTarget == "both" {
		return "Xane 和 " + assistantName + " 的铃铛：" + body
	}
	return fmt.Sprintf("铃铛「%s」：%s", b.Title, body)
}

func generateBellNotificationText(b dueBell) string {
	fallback := bellNotificationText(b)
	apiKey := resolveAPIKey("")
	if apiKey == "" && !hasCustomAPIBaseURL("") {
		return fallback
	}
	assistantName := firstNonEmpty(strings.TrimSpace(b.AssistantName), "Rhys")
	prompt := fmt.Sprintf(`请用%s的口吻写一条给 Xane 的铃铛提醒。只输出提醒正文，不要 JSON、标题、引号或动作描写；自然、亲近、简短，不超过 80 个中文字符。
提醒标题：%s
备注：%s`, assistantName, b.Title, b.Note)
	text, err := Call(apiKey, strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")), resolveModel(""), []ChatMessage{{Role: "user", Content: prompt}}, 120)
	if err != nil {
		return fallback
	}
	text = strings.Trim(strings.TrimSpace(text), "\"'“”")
	if text == "" {
		return fallback
	}
	return truncateRunes(text, 120)
}

func createBell(w http.ResponseWriter, r *http.Request) {
	b, err := decodeBell(w, r)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	res, err := db.DB.Exec(`INSERT INTO bells(title,note,kind,frequency,reminder_time,deadline,reminder_interval,push_target,enabled) VALUES(?,?,?,?,?,?,?,?,?)`, b.Title, b.Note, b.Kind, b.Frequency, b.ReminderTime, b.Deadline, b.ReminderInterval, b.PushTarget, boolInt(b.Enabled))
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	getBell(w, id)
}

func getBell(w http.ResponseWriter, id int64) {
	b, err := scanBell(db.DB.QueryRow(bellSelect+` WHERE b.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "铃铛不存在"})
		return
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"bell": b})
}

func updateBell(w http.ResponseWriter, r *http.Request, id int64) {
	b, err := decodeBell(w, r)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	res, err := db.DB.Exec(`UPDATE bells SET title=?,note=?,kind=?,frequency=?,reminder_time=?,deadline=?,reminder_interval=?,push_target=?,enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, b.Title, b.Note, b.Kind, b.Frequency, b.ReminderTime, b.Deadline, b.ReminderInterval, b.PushTarget, boolInt(b.Enabled), id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		jsonResp(w, 404, map[string]string{"error": "铃铛不存在"})
		return
	}
	getBell(w, id)
}

func deleteBell(w http.ResponseWriter, id int64) {
	res, err := db.DB.Exec(`DELETE FROM bells WHERE id=?`, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		jsonResp(w, 404, map[string]string{"error": "铃铛不存在"})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func toggleBellCheckin(w http.ResponseWriter, id int64) {
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM bells WHERE id=?`, id).Scan(&exists); err != nil || exists == 0 {
		jsonResp(w, 404, map[string]string{"error": "铃铛不存在"})
		return
	}
	today := time.Now().In(wakeLocation).Format("2006-01-02")
	res, err := tx.Exec(`DELETE FROM bell_checkins WHERE bell_id=?`, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	completed := false
	if n == 0 {
		_, err = tx.Exec(`INSERT INTO bell_checkins(bell_id,checkin_date) VALUES(?,?)`, id, today)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		completed = true
	}
	if err := tx.Commit(); err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"completed": completed, "date": time.Now().In(wakeLocation).Format("2006-01-02")})
}

func bellStats(w http.ResponseWriter) {
	todayDate := time.Now().In(wakeLocation).Format("2006-01-02")
	var total, today int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM bells WHERE enabled=1`).Scan(&total)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM bell_checkins c JOIN bells b ON b.id=c.bell_id WHERE b.enabled=1 AND c.checkin_date=?`, todayDate).Scan(&today)
	rate := func(mod string, days int) map[string]interface{} {
		var done int
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM bell_checkins c JOIN bells b ON b.id=c.bell_id WHERE b.enabled=1 AND c.checkin_date>=date('now','+8 hours',?)`, mod).Scan(&done)
		expected := total * days
		pct := 0
		if expected > 0 {
			pct = done * 100 / expected
		}
		return map[string]interface{}{"completed": done, "expected": expected, "rate": pct}
	}
	jsonResp(w, 200, map[string]interface{}{"total": total, "today_completed": today, "week": rate("-6 days", 7), "month": rate("start of month", time.Now().In(wakeLocation).Day())})
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
