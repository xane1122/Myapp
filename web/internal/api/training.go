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

type trainingTask struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Type           string `json:"type"`
	Category       string `json:"category"`
	Difficulty     int    `json:"difficulty"`
	TimeLimit      int    `json:"time_limit"`
	RewardPoints   int    `json:"reward_points"`
	FailurePenalty string `json:"failure_penalty"`
	AssignedDate   string `json:"assigned_date"`
	DeadlineAt     string `json:"deadline_at"`
	Status         string `json:"status"`
	CompletedAt    string `json:"completed_at"`
	Review         string `json:"review"`
	Score          *int   `json:"score,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

const trainingTaskSelect = `SELECT id,title,description,type,category,difficulty,time_limit,reward_points,failure_penalty,assigned_date,deadline_at,status,completed_at,review,score,created_at,updated_at FROM training_tasks`

func handleTraining(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/training"), "/")
	parts := strings.Split(path, "/")
	switch parts[0] {
	case "profile":
		handleTrainingProfile(w, r)
	case "tasks":
		if len(parts) == 1 {
			handleTrainingTasks(w, r)
			return
		}
		handleTrainingTask(w, r, trainingID(w, parts[1]))
	case "punishments":
		if len(parts) == 1 {
			handleTrainingPunishments(w, r)
			return
		}
		handleTrainingPunishment(w, r, trainingID(w, parts[1]))
	case "reviews":
		handleTrainingReviews(w, r)
	case "badges":
		if len(parts) == 1 {
			handleTrainingBadges(w, r)
			return
		}
		handleTrainingBadge(w, r, trainingID(w, parts[1]))
	case "progress":
		handleTrainingProgress(w, r)
	default:
		http.NotFound(w, r)
	}
}

func trainingID(w http.ResponseWriter, raw string) int64 {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		jsonResp(w, 400, map[string]string{"error": "id无效"})
		return 0
	}
	return id
}

func decodeTraining(w http.ResponseWriter, r *http.Request, target interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("请求格式错误")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求格式错误")
	}
	return nil
}

func validTrainingDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func trainingToday() string { return time.Now().In(wakeLocation).Format("2006-01-02") }

func handleTrainingProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var in struct {
			RoleName    string `json:"role_name"`
			TodayStatus string `json:"today_status"`
		}
		if err := decodeTraining(w, r, &in); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		in.RoleName, in.TodayStatus = strings.TrimSpace(in.RoleName), strings.TrimSpace(in.TodayStatus)
		if in.RoleName == "" || len([]rune(in.RoleName)) > 30 {
			jsonResp(w, 400, map[string]string{"error": "学员名须为1至30字"})
			return
		}
		if in.TodayStatus != "待开始" && in.TodayStatus != "进行中" && in.TodayStatus != "已完成" {
			jsonResp(w, 400, map[string]string{"error": "今日状态无效"})
			return
		}
		if _, err := db.DB.Exec(`UPDATE training_profile SET role_name=?,today_status=? WHERE id=1`, in.RoleName, in.TodayStatus); err != nil {
			jsonResp(w, 500, map[string]string{"error": "更新资料失败"})
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	var name, status string
	var last sql.NullString
	var level, xp, next, streak, points, earned, spent int
	err := db.DB.QueryRow(`SELECT role_name,level,xp,xp_to_next,streak_days,last_active,total_points,daily_earned,daily_spent,today_status FROM training_profile WHERE id=1`).Scan(&name, &level, &xp, &next, &streak, &last, &points, &earned, &spent, &status)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取角色资料失败"})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"role_name": name, "level": level, "xp": xp, "xp_to_next": next, "streak_days": streak, "last_active": last.String, "total_points": points, "daily_earned": earned, "daily_spent": spent, "today_status": status})
}

func cleanTrainingTask(task *trainingTask) error {
	task.Title, task.Description = strings.TrimSpace(task.Title), strings.TrimSpace(task.Description)
	task.FailurePenalty, task.Review = strings.TrimSpace(task.FailurePenalty), strings.TrimSpace(task.Review)
	if task.Title == "" || len([]rune(task.Title)) > 80 {
		return errors.New("任务名须为1至80字")
	}
	if len([]rune(task.Description)) > 500 || len([]rune(task.FailurePenalty)) > 300 {
		return errors.New("任务内容过长")
	}
	if task.Type == "" {
		task.Type = "daily"
	}
	if task.Category == "" {
		task.Category = "obedience"
	}
	if task.Status == "" {
		task.Status = "pending"
	}
	if task.AssignedDate == "" {
		task.AssignedDate = trainingToday()
	}
	if !map[string]bool{"daily": true, "challenge": true, "punishment": true, "hidden": true}[task.Type] {
		return errors.New("任务类型无效")
	}
	if !map[string]bool{"obedience": true, "endurance": true, "service": true, "mindset": true, "punishment": true}[task.Category] {
		return errors.New("任务分类无效")
	}
	if !map[string]bool{"pending": true, "completed": true, "failed": true, "skipped": true, "locked": true}[task.Status] {
		return errors.New("任务状态无效")
	}
	if task.Difficulty < 1 || task.Difficulty > 5 {
		return errors.New("难度须为1至5")
	}
	if task.TimeLimit < 0 || task.TimeLimit > 10080 || task.RewardPoints < 0 || task.RewardPoints > 100000 {
		return errors.New("时限或奖励超出范围")
	}
	if !validTrainingDate(task.AssignedDate) {
		return errors.New("日期格式无效")
	}
	if task.DeadlineAt != "" {
		if _, err := time.Parse(time.RFC3339, task.DeadlineAt); err != nil {
			return errors.New("截止时间格式无效")
		}
	}
	if task.Score != nil && (*task.Score < 1 || *task.Score > 10) {
		return errors.New("评分须为1至10")
	}
	return nil
}

func scanTrainingTask(scanner interface{ Scan(...interface{}) error }) (trainingTask, error) {
	var task trainingTask
	var deadline, completed sql.NullString
	var score sql.NullInt64
	err := scanner.Scan(&task.ID, &task.Title, &task.Description, &task.Type, &task.Category, &task.Difficulty, &task.TimeLimit, &task.RewardPoints, &task.FailurePenalty, &task.AssignedDate, &deadline, &task.Status, &completed, &task.Review, &score, &task.CreatedAt, &task.UpdatedAt)
	task.DeadlineAt, task.CompletedAt = deadline.String, completed.String
	if score.Valid {
		value := int(score.Int64)
		task.Score = &value
	}
	return task, err
}

func handleTrainingTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var task trainingTask
		if err := decodeTraining(w, r, &task); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := cleanTrainingTask(&task); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		result, err := db.DB.Exec(`INSERT INTO training_tasks(title,description,type,category,difficulty,time_limit,reward_points,failure_penalty,assigned_date,deadline_at,status,review,score) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`, task.Title, task.Description, task.Type, task.Category, task.Difficulty, task.TimeLimit, task.RewardPoints, task.FailurePenalty, task.AssignedDate, task.DeadlineAt, task.Status, task.Review, task.Score)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "创建任务失败"})
			return
		}
		id, _ := result.LastInsertId()
		task, _ = scanTrainingTask(db.DB.QueryRow(trainingTaskSelect+` WHERE id=?`, id))
		jsonResp(w, 201, task)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = trainingToday()
	}
	if !validTrainingDate(date) {
		jsonResp(w, 400, map[string]string{"error": "日期格式无效"})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query, args := trainingTaskSelect+` WHERE assigned_date=?`, []interface{}{date}
	if status != "" {
		if !map[string]bool{"pending": true, "completed": true, "failed": true, "skipped": true, "locked": true}[status] {
			jsonResp(w, 400, map[string]string{"error": "状态无效"})
			return
		}
		query += ` AND status=?`
		args = append(args, status)
	}
	rows, err := db.DB.Query(query+` ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'locked' THEN 1 ELSE 2 END,id DESC`, args...)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取任务失败"})
		return
	}
	defer rows.Close()
	tasks := []trainingTask{}
	for rows.Next() {
		task, err := scanTrainingTask(rows)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取任务失败"})
			return
		}
		tasks = append(tasks, task)
	}
	jsonResp(w, 200, map[string]interface{}{"tasks": tasks})
}

func handleTrainingTask(w http.ResponseWriter, r *http.Request, id int64) {
	if id == 0 {
		return
	}
	if r.Method == http.MethodGet {
		task, err := scanTrainingTask(db.DB.QueryRow(trainingTaskSelect+` WHERE id=?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			jsonResp(w, 404, map[string]string{"error": "任务不存在"})
			return
		}
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取任务失败"})
			return
		}
		jsonResp(w, 200, task)
		return
	}
	if r.Method == http.MethodDelete {
		result, err := db.DB.Exec(`DELETE FROM training_tasks WHERE id=?`, id)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "删除任务失败"})
			return
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			jsonResp(w, 404, map[string]string{"error": "任务不存在"})
			return
		}
		jsonResp(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method == http.MethodPut {
		var task trainingTask
		if err := decodeTraining(w, r, &task); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		var currentStatus string
		if err := db.DB.QueryRow(`SELECT status FROM training_tasks WHERE id=?`, id).Scan(&currentStatus); errors.Is(err, sql.ErrNoRows) {
			jsonResp(w, 404, map[string]string{"error": "任务不存在"})
			return
		} else if err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取任务失败"})
			return
		}
		task.Status = currentStatus
		if err := cleanTrainingTask(&task); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		_, err := db.DB.Exec(`UPDATE training_tasks SET title=?,description=?,type=?,category=?,difficulty=?,time_limit=?,reward_points=?,failure_penalty=?,assigned_date=?,deadline_at=NULLIF(?,''),review=?,score=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, task.Title, task.Description, task.Type, task.Category, task.Difficulty, task.TimeLimit, task.RewardPoints, task.FailurePenalty, task.AssignedDate, task.DeadlineAt, task.Review, task.Score, id)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "更新任务失败"})
			return
		}
		task, _ = scanTrainingTask(db.DB.QueryRow(trainingTaskSelect+` WHERE id=?`, id))
		jsonResp(w, 200, task)
		return
	}
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Status string  `json:"status"`
		Review *string `json:"review"`
		Score  *int    `json:"score"`
	}
	if err := decodeTraining(w, r, &in); err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if !map[string]bool{"pending": true, "completed": true, "failed": true, "skipped": true, "locked": true}[in.Status] {
		jsonResp(w, 400, map[string]string{"error": "状态无效"})
		return
	}
	if in.Score != nil && (*in.Score < 1 || *in.Score > 10) {
		jsonResp(w, 400, map[string]string{"error": "评分须为1至10"})
		return
	}
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "更新任务失败"})
		return
	}
	defer tx.Rollback()
	var oldStatus, title, penalty string
	var reward, difficulty int
	if err = tx.QueryRow(`SELECT status,title,failure_penalty,reward_points,difficulty FROM training_tasks WHERE id=?`, id).Scan(&oldStatus, &title, &penalty, &reward, &difficulty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonResp(w, 404, map[string]string{"error": "任务不存在"})
		} else {
			jsonResp(w, 500, map[string]string{"error": "更新任务失败"})
		}
		return
	}
	review := ""
	if in.Review != nil {
		review = strings.TrimSpace(*in.Review)
	}
	_, err = tx.Exec(`UPDATE training_tasks SET status=?,review=CASE WHEN ?='' THEN review ELSE ? END,score=COALESCE(?,score),completed_at=CASE WHEN ?='completed' THEN COALESCE(completed_at,CURRENT_TIMESTAMP) ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, in.Status, review, review, in.Score, in.Status, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "更新任务失败"})
		return
	}
	if oldStatus != "completed" && in.Status == "completed" {
		xp := difficulty * 20
		_, err = tx.Exec(`UPDATE training_profile SET total_points=total_points+?,daily_earned=daily_earned+?,xp=xp+?,today_status='进行中',last_active=? WHERE id=1`, reward, reward, xp, trainingToday())
	}
	if oldStatus == "completed" && in.Status != "completed" {
		xp := difficulty * 20
		_, err = tx.Exec(`UPDATE training_profile SET total_points=MAX(0,total_points-?),daily_earned=MAX(0,daily_earned-?),xp=MAX(0,xp-?) WHERE id=1`, reward, reward, xp)
	}
	if oldStatus != "failed" && in.Status == "failed" {
		content := penalty
		if content == "" {
			content = "复盘并补做：" + title
		}
		_, err = tx.Exec(`INSERT INTO training_punishments(task_id,reason,content,severity) VALUES(?,?,?,?)`, id, "任务失败", content, difficulty)
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "更新关联进度失败"})
		return
	}
	if err = tx.Commit(); err != nil {
		jsonResp(w, 500, map[string]string{"error": "更新任务失败"})
		return
	}
	refreshTrainingStatus()
	task, _ := scanTrainingTask(db.DB.QueryRow(trainingTaskSelect+` WHERE id=?`, id))
	jsonResp(w, 200, task)
}

func handleTrainingPunishments(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in struct {
			TaskID      *int64 `json:"task_id"`
			Description string `json:"description"`
			Severity    int    `json:"severity"`
		}
		if err := decodeTraining(w, r, &in); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		in.Description = strings.TrimSpace(in.Description)
		if in.Description == "" || in.Severity < 1 || in.Severity > 5 {
			jsonResp(w, 400, map[string]string{"error": "惩罚内容或等级无效"})
			return
		}
		result, err := db.DB.Exec(`INSERT INTO training_punishments(task_id,content,severity) VALUES(?,?,?)`, in.TaskID, in.Description, in.Severity)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "创建惩罚失败"})
			return
		}
		id, _ := result.LastInsertId()
		jsonResp(w, 201, map[string]interface{}{"id": id})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query := `SELECT id,task_id,reason,content,severity,status,created_at,executed_at FROM training_punishments`
	args := []interface{}{}
	if status != "" {
		if status != "pending" && status != "executed" {
			jsonResp(w, 400, map[string]string{"error": "状态无效"})
			return
		}
		query += ` WHERE status=?`
		args = append(args, status)
	}
	rows, err := db.DB.Query(query+` ORDER BY status='executed',id DESC`, args...)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取惩罚失败"})
		return
	}
	defer rows.Close()
	items := []map[string]interface{}{}
	for rows.Next() {
		var id, severity int
		var taskID sql.NullInt64
		var reason, content, status, created string
		var executed sql.NullString
		if rows.Scan(&id, &taskID, &reason, &content, &severity, &status, &created, &executed) != nil {
			continue
		}
		items = append(items, map[string]interface{}{"id": id, "task_id": taskID.Int64, "reason": reason, "description": content, "severity": severity, "status": status, "created_at": created, "executed_at": executed.String})
	}
	jsonResp(w, 200, map[string]interface{}{"punishments": items})
}

func handleTrainingPunishment(w http.ResponseWriter, r *http.Request, id int64) {
	if id == 0 {
		return
	}
	if r.Method == http.MethodDelete {
		result, err := db.DB.Exec(`DELETE FROM training_punishments WHERE id=?`, id)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "删除失败"})
			return
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			jsonResp(w, 404, map[string]string{"error": "惩罚不存在"})
			return
		}
		jsonResp(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if decodeTraining(w, r, &in) != nil || in.Status != "executed" {
		jsonResp(w, 400, map[string]string{"error": "状态无效"})
		return
	}
	result, err := db.DB.Exec(`UPDATE training_punishments SET status='executed',executed_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "更新失败"})
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		jsonResp(w, 404, map[string]string{"error": "惩罚不存在"})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func handleTrainingReviews(w http.ResponseWriter, r *http.Request) {
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = trainingToday()
	}
	if !validTrainingDate(date) {
		jsonResp(w, 400, map[string]string{"error": "日期格式无效"})
		return
	}
	if r.Method == http.MethodPost {
		var in struct {
			Date    string `json:"date"`
			Comment string `json:"comment"`
			Score   int    `json:"score"`
			Tier    string `json:"tier"`
		}
		if err := decodeTraining(w, r, &in); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if in.Date != "" {
			date = in.Date
		}
		in.Comment = strings.TrimSpace(in.Comment)
		in.Tier = strings.ToUpper(strings.TrimSpace(in.Tier))
		if !validTrainingDate(date) || in.Comment == "" || in.Score < 1 || in.Score > 10 || !map[string]bool{"S": true, "A": true, "B": true, "C": true, "D": true}[in.Tier] {
			jsonResp(w, 400, map[string]string{"error": "评语内容无效"})
			return
		}
		progress := trainingSummary(date)
		_, err := db.DB.Exec(`INSERT INTO training_reviews(review_date,completion_rate,comment,score,tier) VALUES(?,?,?,?,?) ON CONFLICT(review_date) DO UPDATE SET completion_rate=excluded.completion_rate,comment=excluded.comment,score=excluded.score,tier=excluded.tier,updated_at=CURRENT_TIMESTAMP`, date, progress["completion_rate"], in.Comment, in.Score, in.Tier)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "保存评语失败"})
			return
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var comment, tier, created string
	var score, rate int
	err := db.DB.QueryRow(`SELECT comment,score,tier,completion_rate,created_at FROM training_reviews WHERE review_date=?`, date).Scan(&comment, &score, &tier, &rate, &created)
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 200, map[string]interface{}{"review": nil})
		return
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取评语失败"})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"review": map[string]interface{}{"date": date, "comment": comment, "score": score, "tier": tier, "completion_rate": rate, "created_at": created}})
}

func handleTrainingBadges(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in struct {
			Name            string `json:"name"`
			Icon            string `json:"icon"`
			UnlockCondition string `json:"unlock_condition"`
		}
		if err := decodeTraining(w, r, &in); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			jsonResp(w, 400, map[string]string{"error": "徽章名称不能为空"})
			return
		}
		if in.Icon == "" {
			in.Icon = "★"
		}
		result, err := db.DB.Exec(`INSERT INTO training_badges(name,description,icon_char) VALUES(?,?,?)`, in.Name, strings.TrimSpace(in.UnlockCondition), in.Icon)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": "创建徽章失败"})
			return
		}
		id, _ := result.LastInsertId()
		jsonResp(w, 201, map[string]interface{}{"id": id})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	rows, err := db.DB.Query(`SELECT id,name,description,icon_char,unlocked,unlocked_at FROM training_badges ORDER BY unlocked DESC,id`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取徽章失败"})
		return
	}
	defer rows.Close()
	items := []map[string]interface{}{}
	for rows.Next() {
		var id, unlocked int
		var name, desc, icon string
		var at sql.NullString
		if rows.Scan(&id, &name, &desc, &icon, &unlocked, &at) != nil {
			continue
		}
		items = append(items, map[string]interface{}{"id": id, "name": name, "unlock_condition": desc, "icon": icon, "is_unlocked": unlocked != 0, "unlocked_at": at.String})
	}
	jsonResp(w, 200, map[string]interface{}{"badges": items})
}

func handleTrainingBadge(w http.ResponseWriter, r *http.Request, id int64) {
	if id == 0 {
		return
	}
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", 405)
		return
	}
	result, err := db.DB.Exec(`UPDATE training_badges SET unlocked=1,unlocked_at=COALESCE(unlocked_at,CURRENT_TIMESTAMP) WHERE id=?`, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "解锁失败"})
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		jsonResp(w, 404, map[string]string{"error": "徽章不存在"})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func trainingSummary(date string) map[string]interface{} {
	var total, completed, failed, points int
	_ = db.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(status='completed'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(CASE WHEN status='completed' THEN reward_points ELSE 0 END),0) FROM training_tasks WHERE assigned_date=?`, date).Scan(&total, &completed, &failed, &points)
	rate := 0
	if total > 0 {
		rate = completed * 100 / total
	}
	return map[string]interface{}{"date": date, "total_tasks": total, "completed_tasks": completed, "failed_tasks": failed, "completion_rate": rate, "earned_points": points}
}

func trainingStreak() int {
	rows, err := db.DB.Query(`SELECT assigned_date FROM training_tasks GROUP BY assigned_date HAVING COUNT(*)>0 AND SUM(status='completed')=COUNT(*) ORDER BY assigned_date DESC`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	dates := map[string]bool{}
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		dates[d] = true
	}
	day := time.Now().In(wakeLocation)
	if !dates[day.Format("2006-01-02")] {
		day = day.AddDate(0, 0, -1)
	}
	streak := 0
	for dates[day.Format("2006-01-02")] {
		streak++
		day = day.AddDate(0, 0, -1)
	}
	return streak
}

func refreshTrainingStatus() {
	summary := trainingSummary(trainingToday())
	total := summary["total_tasks"].(int)
	done := summary["completed_tasks"].(int)
	status := "待开始"
	if done > 0 {
		status = "进行中"
	}
	if total > 0 && done == total {
		status = "已完成"
	}
	streak := trainingStreak()
	_, _ = db.DB.Exec(`UPDATE training_profile SET today_status=?,streak_days=? WHERE id=1`, status, streak)
}

func handleTrainingProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	refreshTrainingStatus()
	today := trainingSummary(trainingToday())
	streak := trainingStreak()
	today["streak_days"] = streak
	rows, err := db.DB.Query(`SELECT assigned_date,COUNT(*),SUM(status='completed') FROM training_tasks GROUP BY assigned_date ORDER BY assigned_date DESC LIMIT 7`)
	recent := []map[string]interface{}{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var date string
			var total, done int
			_ = rows.Scan(&date, &total, &done)
			rate := 0
			if total > 0 {
				rate = done * 100 / total
			}
			recent = append(recent, map[string]interface{}{"date": date, "completion_rate": rate, "completed_tasks": done, "total_tasks": total})
		}
	}
	jsonResp(w, 200, map[string]interface{}{"today": today, "recent": recent, "streak_days": streak})
}
