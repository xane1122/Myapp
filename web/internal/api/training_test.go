package api

import (
	"net/http"
	"testing"

	"myapp/internal/db"
)

func TestTrainingTasksHandleNullableDatesAndValidateDate(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	if _, err := db.DB.Exec(`INSERT INTO training_tasks(title,assigned_date) VALUES('整理桌面','2026-07-22')`); err != nil {
		t.Fatal(err)
	}
	h := Handler()
	result := bellRequest(t, h, http.MethodGet, "/api/training/tasks?date=2026-07-22", nil, http.StatusOK)
	tasks := result["tasks"].([]interface{})
	if len(tasks) != 1 {
		t.Fatalf("tasks=%#v", tasks)
	}
	task := tasks[0].(map[string]interface{})
	if task["deadline_at"] != "" || task["completed_at"] != "" {
		t.Fatalf("nullable dates=%#v", task)
	}
	bellRequest(t, h, http.MethodGet, "/api/training/tasks?date=2026-99-99", nil, http.StatusBadRequest)
}

func TestTrainingProfileCreatesDefaultRow(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	result := bellRequest(t, Handler(), http.MethodGet, "/api/training/profile", nil, http.StatusOK)
	if result["role_name"] != "小宝" || result["level"].(float64) != 1 || result["xp_to_next"].(float64) != 500 {
		t.Fatalf("profile=%#v", result)
	}
}

func TestTrainingTaskCompletionAndFailureUpdateRelatedState(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	if _, err := db.DB.Exec(`INSERT INTO training_tasks(title,difficulty,reward_points,failure_penalty,assigned_date) VALUES('训练',2,30,'加练','2026-07-22')`); err != nil {
		t.Fatal(err)
	}
	h := Handler()
	bellRequest(t, h, http.MethodPatch, "/api/training/tasks/1", []byte(`{"status":"completed"}`), http.StatusOK)
	var points, xp int
	if err := db.DB.QueryRow(`SELECT total_points,xp FROM training_profile WHERE id=1`).Scan(&points, &xp); err != nil || points != 30 || xp != 40 {
		t.Fatalf("points=%d xp=%d err=%v", points, xp, err)
	}
	bellRequest(t, h, http.MethodPatch, "/api/training/tasks/1", []byte(`{"status":"failed"}`), http.StatusOK)
	var punishment string
	if err := db.DB.QueryRow(`SELECT content FROM training_punishments WHERE task_id=1`).Scan(&punishment); err != nil || punishment != "加练" {
		t.Fatalf("punishment=%q err=%v", punishment, err)
	}
}

func TestTrainingTaskPutUpdatesInPlace(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	if _, err := db.DB.Exec(`INSERT INTO training_tasks(title,difficulty,reward_points,assigned_date) VALUES('旧任务',1,10,'2026-07-22')`); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"title":"新任务","description":"保持原任务编号","type":"challenge","category":"endurance","difficulty":3,"time_limit":45,"reward_points":60,"failure_penalty":"补做一次","assigned_date":"2026-07-22","deadline_at":"","review":""}`)
	result := bellRequest(t, Handler(), http.MethodPut, "/api/training/tasks/1", body, http.StatusOK)
	if result["id"].(float64) != 1 || result["title"] != "新任务" || result["status"] != "pending" {
		t.Fatalf("task=%#v", result)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM training_tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
