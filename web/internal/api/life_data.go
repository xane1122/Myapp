package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"myapp/internal/db"
	"myapp/internal/memory"
)

type lifeDataInput struct {
	Module         string   `json:"module"`
	Action         string   `json:"action"`
	Section        string   `json:"section,omitempty"`
	ID             int64    `json:"id,omitempty"`
	Query          string   `json:"query,omitempty"`
	Date           string   `json:"date,omitempty"`
	Title          string   `json:"title,omitempty"`
	Content        string   `json:"content,omitempty"`
	Note           string   `json:"note,omitempty"`
	Category       string   `json:"category,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	Amount         float64  `json:"amount,omitempty"`
	Mood           string   `json:"mood,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Status         string   `json:"status,omitempty"`
	Pinned         *bool    `json:"pinned,omitempty"`
	Favorite       *bool    `json:"favorite,omitempty"`
	Direction      string   `json:"direction,omitempty"`
	Difficulty     int      `json:"difficulty,omitempty"`
	Score          int      `json:"score,omitempty"`
	Tier           string   `json:"tier,omitempty"`
	Icon           string   `json:"icon,omitempty"`
	TimeLimit      int      `json:"time_limit,omitempty"`
	RewardPoints   int      `json:"reward_points,omitempty"`
	FailurePenalty string   `json:"failure_penalty,omitempty"`
	Frequency      string   `json:"frequency,omitempty"`
	ReminderTime   string   `json:"reminder_time,omitempty"`
	Deadline       string   `json:"deadline,omitempty"`
	Interval       string   `json:"reminder_interval,omitempty"`
	PushTarget     string   `json:"push_target,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
}

func handleLifeData(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/life/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || !map[string]bool{"ledger": true, "daily-log": true, "notebook": true}[parts[0]] {
		http.NotFound(w, r)
		return
	}
	in := lifeDataInput{Module: parts[0]}
	if len(parts) > 1 {
		in.ID, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	switch r.Method {
	case http.MethodGet:
		in.Action = "list"
		in.Query = r.URL.Query().Get("q")
		in.Date = r.URL.Query().Get("date")
	case http.MethodPost:
		in.Action = "create"
		if err := decodeTraining(w, r, &in); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		in.Module = parts[0]
	case http.MethodPut:
		in.Action = "update"
		if in.ID <= 0 {
			jsonResp(w, 400, map[string]string{"error": "id无效"})
			return
		}
		if err := decodeTraining(w, r, &in); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		in.Module, in.ID = parts[0], in.ID
	case http.MethodDelete:
		in.Action = "delete"
		if in.ID <= 0 {
			jsonResp(w, 400, map[string]string{"error": "id无效"})
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := executeLifeData(in, "user")
	if err != nil {
		jsonResp(w, lifeDataStatus(err), map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, result)
}

func lifeDataStatus(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func executeLifeData(in lifeDataInput, author string) (interface{}, error) {
	return executeLifeDataAs(in, author, assistantNameForConversation(0))
}

func executeLifeDataAs(in lifeDataInput, author, assistantName string) (interface{}, error) {
	in.Module, in.Action = strings.TrimSpace(in.Module), strings.TrimSpace(in.Action)
	if author != "ai" {
		author = "user"
	}
	switch in.Module {
	case "ledger":
		return executeLedger(in, author)
	case "daily_log", "daily-log":
		return executeDailyLog(in, author)
	case "notebook":
		return executeNotebook(in, author)
	case "mailbox":
		return executeMailboxLife(in, assistantName)
	case "days_matter", "days-matter":
		return executeDaysMatterLife(in)
	case "notes":
		return executeNotesLife(in, author)
	case "album":
		return executeAlbumLife(in)
	case "bell":
		return executeBellLife(in)
	case "training":
		return executeTrainingLife(in)
	default:
		return nil, errors.New("不支持的生活模块")
	}
}

func executeLedger(in lifeDataInput, author string) (interface{}, error) {
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,entry_date,kind,amount_cents,category,note,author,created_at,updated_at FROM ledger_entries WHERE (?='' OR entry_date=?) AND (?='' OR category LIKE '%'||?||'%' OR note LIKE '%'||?||'%') ORDER BY entry_date DESC,id DESC LIMIT 100`, in.Date, in.Date, in.Query, in.Query, in.Query)
	case "create":
		date := firstNonEmpty(strings.TrimSpace(in.Date), trainingToday())
		if !validTrainingDate(date) || (in.Kind != "income" && in.Kind != "expense") || in.Amount < 0 || in.Amount > 100000000 {
			return nil, errors.New("账目日期、类型或金额无效")
		}
		if len([]rune(in.Category)) > 40 || len([]rune(in.Note)) > 300 {
			return nil, errors.New("账目内容过长")
		}
		res, err := db.DB.Exec(`INSERT INTO ledger_entries(entry_date,kind,amount_cents,category,note,author) VALUES(?,?,?,?,?,?)`, date, in.Kind, int64(in.Amount*100+0.5), strings.TrimSpace(in.Category), strings.TrimSpace(in.Note), author)
		return insertedLifeID(res, err)
	case "update":
		if in.ID <= 0 || !validTrainingDate(in.Date) || (in.Kind != "income" && in.Kind != "expense") || in.Amount < 0 {
			return nil, errors.New("账目参数无效")
		}
		res, err := db.DB.Exec(`UPDATE ledger_entries SET entry_date=?,kind=?,amount_cents=?,category=?,note=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, in.Date, in.Kind, int64(in.Amount*100+0.5), strings.TrimSpace(in.Category), strings.TrimSpace(in.Note), in.ID)
		return changedLifeRow(res, err)
	case "delete":
		return deleteLifeRows("ledger_entries", in)
	}
	return nil, errors.New("账本操作无效")
}

func executeDailyLog(in lifeDataInput, author string) (interface{}, error) {
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,log_date,content,mood,author,created_at,updated_at FROM daily_logs WHERE (?='' OR log_date=?) AND (?='' OR content LIKE '%'||?||'%' OR mood LIKE '%'||?||'%') ORDER BY log_date DESC LIMIT 100`, in.Date, in.Date, in.Query, in.Query, in.Query)
	case "create":
		date := firstNonEmpty(strings.TrimSpace(in.Date), trainingToday())
		if !validTrainingDate(date) || strings.TrimSpace(in.Content) == "" || len([]rune(in.Content)) > 5000 || len([]rune(in.Mood)) > 30 {
			return nil, errors.New("日迹日期或内容无效")
		}
		res, err := db.DB.Exec(`INSERT INTO daily_logs(log_date,content,mood,author) VALUES(?,?,?,?) ON CONFLICT(log_date) DO UPDATE SET content=excluded.content,mood=excluded.mood,author=excluded.author,updated_at=CURRENT_TIMESTAMP`, date, strings.TrimSpace(in.Content), strings.TrimSpace(in.Mood), author)
		return insertedLifeID(res, err)
	case "update":
		if in.ID <= 0 || strings.TrimSpace(in.Content) == "" || len([]rune(in.Content)) > 5000 {
			return nil, errors.New("日迹参数无效")
		}
		res, err := db.DB.Exec(`UPDATE daily_logs SET content=?,mood=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, strings.TrimSpace(in.Content), strings.TrimSpace(in.Mood), in.ID)
		return changedLifeRow(res, err)
	case "delete":
		return deleteLifeRows("daily_logs", in)
	}
	return nil, errors.New("日迹操作无效")
}

func executeNotebook(in lifeDataInput, author string) (interface{}, error) {
	tags, _ := json.Marshal(cleanLifeTags(in.Tags))
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,title,content,tags,author,pinned,created_at,updated_at FROM notebook_entries WHERE (?='' OR title LIKE '%'||?||'%' OR content LIKE '%'||?||'%') ORDER BY pinned DESC,updated_at DESC,id DESC LIMIT 100`, in.Query, in.Query, in.Query)
	case "create":
		if strings.TrimSpace(in.Title) == "" || len([]rune(in.Title)) > 100 || len([]rune(in.Content)) > 20000 {
			return nil, errors.New("本子标题或内容无效")
		}
		res, err := db.DB.Exec(`INSERT INTO notebook_entries(title,content,tags,author,pinned) VALUES(?,?,?,?,?)`, strings.TrimSpace(in.Title), strings.TrimSpace(in.Content), string(tags), author, boolInt(in.Pinned != nil && *in.Pinned))
		return insertedLifeID(res, err)
	case "update":
		if in.ID <= 0 || strings.TrimSpace(in.Title) == "" || len([]rune(in.Content)) > 20000 {
			return nil, errors.New("本子参数无效")
		}
		res, err := db.DB.Exec(`UPDATE notebook_entries SET title=?,content=?,tags=?,pinned=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, strings.TrimSpace(in.Title), strings.TrimSpace(in.Content), string(tags), boolInt(in.Pinned != nil && *in.Pinned), in.ID)
		return changedLifeRow(res, err)
	case "delete":
		return deleteLifeRows("notebook_entries", in)
	}
	return nil, errors.New("本子操作无效")
}

func executeMailboxLife(in lifeDataInput, assistantName string) (interface{}, error) {
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,sender,content,created_at FROM mailbox_messages WHERE (?='' OR content LIKE '%'||?||'%') ORDER BY id DESC LIMIT 100`, in.Query, in.Query)
	case "create":
		content := strings.TrimSpace(in.Content)
		if content == "" || len([]rune(content)) > 200 {
			return nil, errors.New("留言须为1至200字")
		}
		res, err := db.DB.Exec(`INSERT INTO mailbox_messages(sender,content) VALUES(?,?)`, firstNonEmpty(strings.TrimSpace(assistantName), "Rhys"), content)
		return insertedLifeID(res, err)
	case "delete":
		return deleteLifeRows("mailbox_messages", in)
	}
	return nil, errors.New("留言箱操作无效")
}

func executeDaysMatterLife(in lifeDataInput) (interface{}, error) {
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,event_name,event_date,direction,note,is_favorite,created_at,updated_at FROM days_matter_events WHERE (?='' OR event_name LIKE '%'||?||'%' OR note LIKE '%'||?||'%') ORDER BY is_favorite DESC,event_date,id DESC LIMIT 100`, in.Query, in.Query, in.Query)
	case "create":
		name := strings.TrimSpace(firstNonEmpty(in.Title, in.Content))
		if name == "" || len([]rune(name)) > 80 || !validTrainingDate(in.Date) {
			return nil, errors.New("Days Matter 名称或日期无效")
		}
		direction := firstNonEmpty(in.Direction, "count_up")
		if direction != "count_up" && direction != "count_down" {
			return nil, errors.New("Days Matter 方向无效")
		}
		tx, err := db.DB.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		favorite := in.Favorite != nil && *in.Favorite
		if favorite {
			_, _ = tx.Exec(`UPDATE days_matter_events SET is_favorite=0,updated_at=CURRENT_TIMESTAMP WHERE is_favorite=1`)
		}
		res, err := tx.Exec(`INSERT INTO days_matter_events(event_name,event_date,direction,note,is_favorite) VALUES(?,?,?,?,?)`, name, in.Date, direction, strings.TrimSpace(in.Note), boolInt(favorite))
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return insertedLifeID(res, nil)
	case "update":
		name := strings.TrimSpace(firstNonEmpty(in.Title, in.Content))
		direction := firstNonEmpty(in.Direction, "count_up")
		if in.ID <= 0 || name == "" || len([]rune(name)) > 80 || !validTrainingDate(in.Date) || (direction != "count_up" && direction != "count_down") {
			return nil, errors.New("Days Matter 参数无效")
		}
		tx, err := db.DB.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		favorite := in.Favorite != nil && *in.Favorite
		if favorite {
			_, _ = tx.Exec(`UPDATE days_matter_events SET is_favorite=0,updated_at=CURRENT_TIMESTAMP WHERE id<>? AND is_favorite=1`, in.ID)
		}
		res, err := tx.Exec(`UPDATE days_matter_events SET event_name=?,event_date=?,direction=?,note=?,is_favorite=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, name, in.Date, direction, strings.TrimSpace(in.Note), boolInt(favorite), in.ID)
		if err != nil {
			return nil, err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, sql.ErrNoRows
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]interface{}{"ok": true, "affected": count}, nil
	case "delete":
		return deleteLifeRows("days_matter_events", in)
	}
	return nil, errors.New("Days Matter 操作无效")
}

func executeNotesLife(in lifeDataInput, author string) (interface{}, error) {
	tags, _ := json.Marshal(cleanLifeTags(in.Tags))
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,content,tags,status,author,pinned,conversation_id,created_at FROM notes WHERE deleted=0 AND (?='' OR content LIKE '%'||?||'%') ORDER BY pinned DESC,id DESC LIMIT 100`, in.Query, in.Query)
	case "create":
		content := strings.TrimSpace(in.Content)
		status := firstNonEmpty(in.Status, "pending")
		if content == "" || len([]rune(content)) > 500 || !map[string]bool{"pending": true, "done": true, "archived": true}[status] {
			return nil, errors.New("纸条内容或状态无效")
		}
		res, err := db.DB.Exec(`INSERT INTO notes(content,tags,status,author,pinned) VALUES(?,?,?,?,?)`, content, string(tags), status, author, boolInt(in.Pinned != nil && *in.Pinned))
		return insertedLifeID(res, err)
	case "update":
		if in.ID <= 0 || strings.TrimSpace(in.Content) == "" || !map[string]bool{"pending": true, "done": true, "archived": true}[in.Status] {
			return nil, errors.New("纸条参数无效")
		}
		res, err := db.DB.Exec(`UPDATE notes SET content=?,tags=?,status=?,pinned=? WHERE id=? AND deleted=0`, strings.TrimSpace(in.Content), string(tags), in.Status, boolInt(in.Pinned != nil && *in.Pinned), in.ID)
		return changedLifeRow(res, err)
	case "delete":
		query, args, err := lifeDeleteCondition("notes", in)
		if err != nil {
			return nil, err
		}
		res, err := db.DB.Exec(`UPDATE notes SET deleted=1 WHERE deleted=0 AND `+query, args...)
		return changedLifeRow(res, err)
	}
	return nil, errors.New("小纸条操作无效")
}

func executeAlbumLife(in lifeDataInput) (interface{}, error) {
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,uploader,caption,is_favorite,source_type,source_id,created_at FROM album_photos WHERE (?='' OR caption LIKE '%'||?||'%') ORDER BY is_favorite DESC,id DESC LIMIT 100`, in.Query, in.Query)
	case "update":
		if in.ID <= 0 || len([]rune(in.Content)) > 200 {
			return nil, errors.New("相册参数无效")
		}
		favorite := in.Favorite != nil && *in.Favorite
		res, err := db.DB.Exec(`UPDATE album_photos SET caption=?,is_favorite=? WHERE id=?`, strings.TrimSpace(in.Content), boolInt(favorite), in.ID)
		return changedLifeRow(res, err)
	case "delete":
		if in.ID <= 0 {
			return nil, errors.New("相册删除需要图片 ID")
		}
		albumSaveMu.Lock()
		defer albumSaveMu.Unlock()
		photo, err := memory.GetAlbumPhoto(in.ID)
		if err != nil {
			return nil, err
		}
		fullTrash, thumbTrash, err := stageAlbumDelete(photo)
		if err != nil {
			return nil, err
		}
		if _, err = memory.DeleteAlbumPhoto(in.ID, photo.Uploader); err != nil {
			restoreAlbumDelete(fullTrash, photo.FilePath)
			restoreAlbumDelete(thumbTrash, photo.ThumbPath)
			return nil, err
		}
		if err = os.Remove(fullTrash); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err = os.Remove(thumbTrash); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return map[string]interface{}{"ok": true, "affected": int64(1)}, nil
	}
	return nil, errors.New("贴贴操作无效；保存聊天图片请使用 album_save")
}

func executeBellLife(in lifeDataInput) (interface{}, error) {
	switch in.Action {
	case "list":
		return queryLifeRows(`SELECT id,title,note,kind,frequency,reminder_time,deadline,reminder_interval,push_target,enabled,created_at,updated_at FROM bells WHERE (?='' OR title LIKE '%'||?||'%' OR note LIKE '%'||?||'%') ORDER BY enabled DESC,updated_at DESC,id DESC LIMIT 100`, in.Query, in.Query, in.Query)
	case "create", "update":
		title := strings.TrimSpace(in.Title)
		frequency := firstNonEmpty(strings.TrimSpace(in.Frequency), "daily")
		reminderTime := firstNonEmpty(strings.TrimSpace(in.ReminderTime), "09:00")
		pushTarget := firstNonEmpty(strings.TrimSpace(in.PushTarget), "me")
		if title == "" || len([]rune(title)) > 80 || len([]rune(in.Note)) > 500 || !map[string]bool{"daily": true, "weekdays": true, "weekly": true, "monthly": true}[frequency] || !validBellTime(reminderTime) || !validBellDeadline(in.Deadline) || (in.Interval != "" && !validBellInterval(in.Interval)) || !map[string]bool{"me": true, "ai": true, "both": true}[pushTarget] {
			return nil, errors.New("铃铛参数无效")
		}
		enabled := in.Enabled == nil || *in.Enabled
		if in.Action == "create" {
			res, err := db.DB.Exec(`INSERT INTO bells(title,note,kind,frequency,reminder_time,deadline,reminder_interval,push_target,enabled) VALUES(?,?,'ai',?,?,?,?,?,?)`, title, strings.TrimSpace(in.Note), frequency, reminderTime, strings.TrimSpace(in.Deadline), strings.TrimSpace(in.Interval), pushTarget, boolInt(enabled))
			return insertedLifeID(res, err)
		}
		if in.ID <= 0 {
			return nil, errors.New("铃铛更新需要 ID")
		}
		res, err := db.DB.Exec(`UPDATE bells SET title=?,note=?,frequency=?,reminder_time=?,deadline=?,reminder_interval=?,push_target=?,enabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, title, strings.TrimSpace(in.Note), frequency, reminderTime, strings.TrimSpace(in.Deadline), strings.TrimSpace(in.Interval), pushTarget, boolInt(enabled), in.ID)
		return changedLifeRow(res, err)
	case "delete":
		return deleteLifeRows("bells", in)
	}
	return nil, errors.New("铃铛操作无效")
}

func validBellTime(value string) bool     { _, err := time.Parse("15:04", value); return err == nil }
func validBellDeadline(value string) bool { return value == "" || validTrainingDate(value) }
func validBellInterval(value string) bool { _, ok := parseBellInterval(value); return ok }

func executeTrainingLife(in lifeDataInput) (interface{}, error) {
	section := firstNonEmpty(strings.TrimSpace(in.Section), "tasks")
	if section == "profile" {
		if in.Action == "list" || in.Action == "summary" {
			return queryLifeRows(`SELECT role_name,level,xp,xp_to_next,streak_days,last_active,total_points,daily_earned,daily_spent,today_status FROM training_profile WHERE id=1`)
		}
		if in.Action == "update" {
			if strings.TrimSpace(in.Title) == "" || !map[string]bool{"待开始": true, "进行中": true, "已完成": true}[in.Status] {
				return nil, errors.New("学员名或今日状态无效")
			}
			res, err := db.DB.Exec(`UPDATE training_profile SET role_name=?,today_status=? WHERE id=1`, strings.TrimSpace(in.Title), in.Status)
			return changedLifeRow(res, err)
		}
	}
	if section == "punishments" {
		switch in.Action {
		case "list", "summary":
			return queryLifeRows(`SELECT id,task_id,reason,content,severity,status,created_at,updated_at,executed_at FROM training_punishments WHERE (?='' OR status=?) ORDER BY id DESC LIMIT 100`, in.Status, in.Status)
		case "create":
			if strings.TrimSpace(in.Content) == "" || in.Difficulty < 1 || in.Difficulty > 5 {
				return nil, errors.New("惩罚内容或严重度无效")
			}
			res, err := db.DB.Exec(`INSERT INTO training_punishments(reason,content,severity,status) VALUES(?,?,?,'pending')`, strings.TrimSpace(in.Note), strings.TrimSpace(in.Content), in.Difficulty)
			return insertedLifeID(res, err)
		case "update":
			if in.ID <= 0 || !map[string]bool{"pending": true, "executed": true, "cancelled": true}[in.Status] {
				return nil, errors.New("惩罚 ID 或状态无效")
			}
			res, err := db.DB.Exec(`UPDATE training_punishments SET status=?,executed_at=CASE WHEN ?='executed' THEN CURRENT_TIMESTAMP ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, in.Status, in.Status, in.ID)
			return changedLifeRow(res, err)
		case "delete":
			return deleteTrainingRows("training_punishments", in)
		}
	}
	if section == "reviews" {
		switch in.Action {
		case "list", "summary":
			return queryLifeRows(`SELECT id,review_date,completion_rate,comment,score,tier,created_at,updated_at FROM training_reviews WHERE (?='' OR review_date=?) ORDER BY review_date DESC LIMIT 100`, in.Date, in.Date)
		case "create", "update":
			date := firstNonEmpty(in.Date, trainingToday())
			if !validTrainingDate(date) || strings.TrimSpace(in.Content) == "" || in.Score < 1 || in.Score > 10 || !map[string]bool{"S": true, "A": true, "B": true, "C": true, "D": true}[in.Tier] {
				return nil, errors.New("评语日期、内容、评分或等级无效")
			}
			res, err := db.DB.Exec(`INSERT INTO training_reviews(review_date,comment,score,tier) VALUES(?,?,?,?) ON CONFLICT(review_date) DO UPDATE SET comment=excluded.comment,score=excluded.score,tier=excluded.tier,updated_at=CURRENT_TIMESTAMP`, date, strings.TrimSpace(in.Content), in.Score, in.Tier)
			return insertedLifeID(res, err)
		case "delete":
			return deleteTrainingRows("training_reviews", in)
		}
	}
	if section == "badges" {
		switch in.Action {
		case "list", "summary":
			return queryLifeRows(`SELECT id,name,description,icon_char,unlocked,unlocked_at,created_at FROM training_badges ORDER BY unlocked DESC,id DESC LIMIT 100`)
		case "create":
			if strings.TrimSpace(in.Title) == "" || len([]rune(in.Title)) > 40 || len([]rune(in.Icon)) > 4 {
				return nil, errors.New("徽章名称或图标无效")
			}
			res, err := db.DB.Exec(`INSERT INTO training_badges(name,description,icon_char) VALUES(?,?,?)`, strings.TrimSpace(in.Title), strings.TrimSpace(in.Content), firstNonEmpty(strings.TrimSpace(in.Icon), "★"))
			return insertedLifeID(res, err)
		case "update":
			if in.ID <= 0 || !map[string]bool{"unlocked": true, "locked": true}[in.Status] {
				return nil, errors.New("徽章 ID 或状态无效")
			}
			unlocked := in.Status == "unlocked"
			res, err := db.DB.Exec(`UPDATE training_badges SET unlocked=?,unlocked_at=CASE WHEN ? THEN COALESCE(unlocked_at,CURRENT_TIMESTAMP) ELSE NULL END WHERE id=?`, boolInt(unlocked), unlocked, in.ID)
			return changedLifeRow(res, err)
		case "delete":
			return deleteTrainingRows("training_badges", in)
		}
	}
	if section == "progress" {
		return queryLifeRows(`SELECT date,total_tasks,completed_tasks,failed_tasks,streak_days,total_points FROM training_progress WHERE (?='' OR date=?) ORDER BY date DESC LIMIT 100`, in.Date, in.Date)
	}
	if section != "tasks" {
		return nil, errors.New("调教室分区无效")
	}
	switch in.Action {
	case "list", "summary":
		date := firstNonEmpty(in.Date, trainingToday())
		return queryLifeRows(trainingTaskSelect+` WHERE assigned_date=? ORDER BY id DESC LIMIT 100`, date)
	case "create":
		task := trainingTask{Title: in.Title, Description: in.Content, Type: firstNonEmpty(in.Kind, "daily"), Category: firstNonEmpty(in.Category, "obedience"), Difficulty: in.Difficulty, TimeLimit: in.TimeLimit, RewardPoints: in.RewardPoints, FailurePenalty: in.FailurePenalty, AssignedDate: firstNonEmpty(in.Date, trainingToday()), Status: "pending"}
		if task.Difficulty == 0 {
			task.Difficulty = 1
		}
		if err := cleanTrainingTask(&task); err != nil {
			return nil, err
		}
		res, err := db.DB.Exec(`INSERT INTO training_tasks(title,description,type,category,difficulty,time_limit,reward_points,failure_penalty,assigned_date,status) VALUES(?,?,?,?,?,?,?,?,?,'pending')`, task.Title, task.Description, task.Type, task.Category, task.Difficulty, task.TimeLimit, task.RewardPoints, task.FailurePenalty, task.AssignedDate)
		return insertedLifeID(res, err)
	case "update":
		if in.ID <= 0 || !map[string]bool{"pending": true, "completed": true, "failed": true, "skipped": true, "locked": true}[in.Status] {
			return nil, errors.New("调教任务 ID 或状态无效")
		}
		return applyTrainingTaskStatus(in.ID, in.Status, strings.TrimSpace(in.Note))
	case "delete":
		return deleteTrainingRows("training_tasks", in)
	}
	return nil, errors.New("调教室操作无效")
}

func deleteTrainingRows(table string, in lifeDataInput) (interface{}, error) {
	allowed := map[string]bool{"training_tasks": true, "training_punishments": true, "training_reviews": true, "training_badges": true}
	if !allowed[table] {
		return nil, errors.New("调教室删除目标无效")
	}
	if in.ID > 0 {
		result, err := db.DB.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id=?`, table), in.ID)
		return changedLifeRow(result, err)
	}
	if strings.TrimSpace(in.Date) != "" && (table == "training_tasks" || table == "training_reviews") {
		column := "assigned_date"
		if table == "training_reviews" {
			column = "review_date"
		}
		result, err := db.DB.Exec(fmt.Sprintf(`DELETE FROM %s WHERE %s=?`, table, column), in.Date)
		return changedLifeRow(result, err)
	}
	if strings.TrimSpace(in.Status) != "" && (table == "training_tasks" || table == "training_punishments") {
		result, err := db.DB.Exec(fmt.Sprintf(`DELETE FROM %s WHERE status=?`, table), in.Status)
		return changedLifeRow(result, err)
	}
	return nil, errors.New("调教室批量删除需要日期或状态条件")
}

func applyTrainingTaskStatus(id int64, status, review string) (interface{}, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var oldStatus, title, penalty string
	var reward, difficulty int
	if err = tx.QueryRow(`SELECT status,title,failure_penalty,reward_points,difficulty FROM training_tasks WHERE id=?`, id).Scan(&oldStatus, &title, &penalty, &reward, &difficulty); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE training_tasks SET status=?,review=CASE WHEN ?='' THEN review ELSE ? END,completed_at=CASE WHEN ?='completed' THEN COALESCE(completed_at,CURRENT_TIMESTAMP) ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, review, review, status, id); err != nil {
		return nil, err
	}
	if oldStatus != "completed" && status == "completed" {
		xp := difficulty * 20
		_, err = tx.Exec(`UPDATE training_profile SET total_points=total_points+?,daily_earned=daily_earned+?,xp=xp+?,today_status='进行中',last_active=? WHERE id=1`, reward, reward, xp, trainingToday())
	} else if oldStatus == "completed" && status != "completed" {
		xp := difficulty * 20
		_, err = tx.Exec(`UPDATE training_profile SET total_points=MAX(0,total_points-?),daily_earned=MAX(0,daily_earned-?),xp=MAX(0,xp-?) WHERE id=1`, reward, reward, xp)
	}
	if err != nil {
		return nil, err
	}
	if oldStatus != "failed" && status == "failed" {
		content := penalty
		if content == "" {
			content = "复盘并补做：" + title
		}
		if _, err = tx.Exec(`INSERT INTO training_punishments(task_id,reason,content,severity) VALUES(?,?,?,?)`, id, "任务失败", content, difficulty); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	refreshTrainingStatus()
	return map[string]interface{}{"ok": true, "id": id, "status": status}, nil
}

func queryLifeRows(query string, args ...interface{}) (interface{}, error) {
	rows, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	items := make([]map[string]interface{}, 0)
	for rows.Next() {
		values := make([]interface{}, len(columns))
		dest := make([]interface{}, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		item := make(map[string]interface{}, len(columns))
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				value = string(raw)
			}
			item[columns[i]] = value
		}
		items = append(items, item)
	}
	return map[string]interface{}{"items": items, "count": len(items)}, rows.Err()
}

func insertedLifeID(result sql.Result, err error) (interface{}, error) {
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true, "id": id}, nil
}

func changedLifeRow(result sql.Result, err error) (interface{}, error) {
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, sql.ErrNoRows
	}
	return map[string]interface{}{"ok": true, "affected": count}, nil
}

func deleteLifeRows(table string, in lifeDataInput) (interface{}, error) {
	allowed := map[string]bool{"ledger_entries": true, "daily_logs": true, "notebook_entries": true, "mailbox_messages": true, "days_matter_events": true, "bells": true}
	if !allowed[table] {
		return nil, errors.New("不允许删除该模块")
	}
	condition, args, err := lifeDeleteCondition(table, in)
	if err != nil {
		return nil, err
	}
	result, err := db.DB.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", table, condition), args...)
	return changedLifeRow(result, err)
}

func lifeDeleteCondition(module string, in lifeDataInput) (string, []interface{}, error) {
	if in.ID > 0 {
		return "id=?", []interface{}{in.ID}, nil
	}
	query, date, status := strings.TrimSpace(in.Query), strings.TrimSpace(in.Date), strings.TrimSpace(in.Status)
	switch module {
	case "ledger_entries":
		if date != "" {
			return "entry_date=?", []interface{}{date}, nil
		}
		if query != "" {
			return "category LIKE '%'||?||'%' OR note LIKE '%'||?||'%'", []interface{}{query, query}, nil
		}
	case "daily_logs":
		if date != "" {
			return "log_date=?", []interface{}{date}, nil
		}
		if query != "" {
			return "content LIKE '%'||?||'%' OR mood LIKE '%'||?||'%'", []interface{}{query, query}, nil
		}
	case "notebook_entries":
		if query != "" {
			return "title LIKE '%'||?||'%' OR content LIKE '%'||?||'%'", []interface{}{query, query}, nil
		}
	case "mailbox_messages":
		if date != "" {
			return "date(created_at)=?", []interface{}{date}, nil
		}
		if query != "" {
			return "content LIKE '%'||?||'%'", []interface{}{query}, nil
		}
	case "days_matter_events":
		if date != "" {
			return "event_date=?", []interface{}{date}, nil
		}
		if query != "" {
			return "event_name LIKE '%'||?||'%' OR note LIKE '%'||?||'%'", []interface{}{query, query}, nil
		}
	case "bells":
		if status == "enabled" {
			return "enabled=1", nil, nil
		}
		if status == "disabled" {
			return "enabled=0", nil, nil
		}
		if query != "" {
			return "title LIKE '%'||?||'%' OR note LIKE '%'||?||'%'", []interface{}{query, query}, nil
		}
	case "notes":
		if status != "" {
			return "status=?", []interface{}{status}, nil
		}
		if query != "" {
			return "content LIKE '%'||?||'%'", []interface{}{query}, nil
		}
	}
	return "", nil, errors.New("批量删除需要提供 ID、日期、状态或关键词条件")
}

func cleanLifeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || len([]rune(tag)) > 30 || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
		if len(out) == 12 {
			break
		}
	}
	return out
}

func runLifeDataTool(arguments string, conversationID int64) string {
	var in lifeDataInput
	if err := json.Unmarshal([]byte(arguments), &in); err != nil {
		return `{"error":"参数格式错误"}`
	}
	result, err := executeLifeDataAs(in, "ai", assistantNameForConversation(conversationID))
	if err != nil {
		encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(encoded)
	}
	encoded, _ := json.Marshal(result)
	return string(encoded)
}

func lifeDataToolDefinition() Tool {
	return Tool{Type: "function", Function: ToolFunction{
		Name:        "manage_life_data",
		Description: "查询或管理生活模块数据：调教室、小纸条、留言箱、Days Matter、贴贴相册、铃铛、账本、日迹和本子。支持按 ID 或日期、状态、关键词等条件进行单条或批量操作。",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"module":  map[string]interface{}{"type": "string", "enum": []string{"training", "notes", "mailbox", "days_matter", "album", "bell", "ledger", "daily_log", "notebook"}},
				"action":  map[string]interface{}{"type": "string", "enum": []string{"list", "summary", "create", "update", "delete"}},
				"section": map[string]interface{}{"type": "string", "enum": []string{"profile", "tasks", "punishments", "reviews", "badges", "progress"}},
				"id":      map[string]interface{}{"type": "integer", "minimum": 1}, "query": map[string]interface{}{"type": "string"}, "date": map[string]interface{}{"type": "string"},
				"title": map[string]interface{}{"type": "string"}, "content": map[string]interface{}{"type": "string"}, "note": map[string]interface{}{"type": "string"},
				"category": map[string]interface{}{"type": "string"}, "kind": map[string]interface{}{"type": "string"}, "amount": map[string]interface{}{"type": "number", "minimum": 0},
				"mood": map[string]interface{}{"type": "string"}, "tags": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}}, "status": map[string]interface{}{"type": "string"},
				"pinned": map[string]interface{}{"type": "boolean"}, "favorite": map[string]interface{}{"type": "boolean"}, "direction": map[string]interface{}{"type": "string", "enum": []string{"count_up", "count_down"}},
				"difficulty": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 5}, "time_limit": map[string]interface{}{"type": "integer", "minimum": 0},
				"score": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 10}, "tier": map[string]interface{}{"type": "string", "enum": []string{"S", "A", "B", "C", "D"}}, "icon": map[string]interface{}{"type": "string"},
				"reward_points": map[string]interface{}{"type": "integer", "minimum": 0}, "failure_penalty": map[string]interface{}{"type": "string"},
				"frequency": map[string]interface{}{"type": "string", "enum": []string{"daily", "weekdays", "weekly", "monthly"}}, "reminder_time": map[string]interface{}{"type": "string"}, "deadline": map[string]interface{}{"type": "string"}, "reminder_interval": map[string]interface{}{"type": "string"}, "push_target": map[string]interface{}{"type": "string", "enum": []string{"me", "ai", "both"}}, "enabled": map[string]interface{}{"type": "boolean"},
			},
			"required": []string{"module", "action"}, "additionalProperties": false,
		},
	}}
}
