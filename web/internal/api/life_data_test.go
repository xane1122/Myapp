package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func initLifeDataTestDB(t *testing.T) {
	t.Helper()
	db.Init(filepath.Join(t.TempDir(), "life-data.sqlite"))
}

func TestLifeDataUpdatesDaysMatterAndManagesBells(t *testing.T) {
	initLifeDataTestDB(t)
	favorite := true
	created, err := executeLifeData(lifeDataInput{Module: "days_matter", Action: "create", Title: "旧名称", Date: "2026-07-24"}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	id := created.(map[string]interface{})["id"].(int64)
	if _, err = executeLifeData(lifeDataInput{Module: "days_matter", Action: "update", ID: id, Title: "新名称", Date: "2026-08-01", Direction: "count_down", Favorite: &favorite}, "ai"); err != nil {
		t.Fatal(err)
	}
	var name, direction string
	if err = db.DB.QueryRow(`SELECT event_name,direction FROM days_matter_events WHERE id=?`, id).Scan(&name, &direction); err != nil || name != "新名称" || direction != "count_down" {
		t.Fatalf("name=%q direction=%q err=%v", name, direction, err)
	}

	enabled := true
	created, err = executeLifeData(lifeDataInput{Module: "bell", Action: "create", Title: "喝水", Frequency: "daily", ReminderTime: "09:30", PushTarget: "me", Enabled: &enabled}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	bellID := created.(map[string]interface{})["id"].(int64)
	if _, err = executeLifeData(lifeDataInput{Module: "bell", Action: "update", ID: bellID, Title: "吃药", Frequency: "daily", ReminderTime: "10:00", PushTarget: "both", Enabled: &enabled}, "ai"); err != nil {
		t.Fatal(err)
	}
	result, err := executeLifeData(lifeDataInput{Module: "bell", Action: "delete", Query: "吃药"}, "ai")
	if err != nil || result.(map[string]interface{})["affected"].(int64) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestLifeDataAlbumDeleteRemovesFilesAndRow(t *testing.T) {
	initLifeDataTestDB(t)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	fullDir, thumbDir := filepath.Join(uploadRoot, "album", "full"), filepath.Join(uploadRoot, "album", "thumb")
	if err := os.MkdirAll(fullDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		t.Fatal(err)
	}
	full, thumb := filepath.Join(fullDir, "photo.jpg"), filepath.Join(thumbDir, "photo.jpg")
	if err := os.WriteFile(full, []byte("full"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumb, []byte("thumb"), 0644); err != nil {
		t.Fatal(err)
	}
	photo, err := memory.CreateAlbumPhoto(memory.AlbumPhoto{Uploader: "Xane", FilePath: full, ThumbPath: thumb, MimeType: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeLifeData(lifeDataInput{Module: "album", Action: "delete", ID: photo.ID}, "ai"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("full image still exists: %v", err)
	}
	if _, err = memory.GetAlbumPhoto(photo.ID); err == nil {
		t.Fatal("album database row still exists")
	}
}

func TestLifeDataCreatesAndListsNewModules(t *testing.T) {
	initLifeDataTestDB(t)
	tests := []lifeDataInput{
		{Module: "ledger", Action: "create", Date: "2026-07-24", Kind: "expense", Amount: 12.34, Category: "餐饮", Note: "午饭"},
		{Module: "daily_log", Action: "create", Date: "2026-07-24", Content: "今天完成了接入。", Mood: "平静"},
		{Module: "notebook", Action: "create", Title: "接入记录", Content: "统一业务层", Tags: []string{"开发", "AI"}},
	}
	for _, input := range tests {
		if _, err := executeLifeData(input, "ai"); err != nil {
			t.Fatalf("create %s: %v", input.Module, err)
		}
		result, err := executeLifeData(lifeDataInput{Module: input.Module, Action: "list"}, "ai")
		if err != nil {
			t.Fatalf("list %s: %v", input.Module, err)
		}
		if result.(map[string]interface{})["count"].(int) != 1 {
			t.Fatalf("list %s result=%#v", input.Module, result)
		}
	}
}

func TestLifeDataSupportsConditionalBatchDelete(t *testing.T) {
	initLifeDataTestDB(t)
	for _, note := range []string{"午饭", "晚饭", "车费"} {
		category := "交通"
		if strings.Contains(note, "饭") {
			category = "餐饮"
		}
		_, err := executeLifeData(lifeDataInput{Module: "ledger", Action: "create", Date: "2026-07-24", Kind: "expense", Amount: 10, Category: category, Note: note}, "ai")
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := executeLifeData(lifeDataInput{Module: "ledger", Action: "delete", Query: "餐饮"}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]interface{})["affected"].(int64) != 2 {
		t.Fatalf("delete result=%#v", result)
	}
}

func TestLifeDataTrainingStatusPreservesRewardsAndPunishments(t *testing.T) {
	initLifeDataTestDB(t)
	created, err := executeLifeData(lifeDataInput{Module: "training", Action: "create", Title: "测试任务", Kind: "daily", Category: "obedience", Difficulty: 2, RewardPoints: 30, FailurePenalty: "补做一次", Date: "2026-07-24"}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	id := created.(map[string]interface{})["id"].(int64)
	if _, err = executeLifeData(lifeDataInput{Module: "training", Action: "update", ID: id, Status: "completed"}, "ai"); err != nil {
		t.Fatal(err)
	}
	var points int
	if err = db.DB.QueryRow(`SELECT total_points FROM training_profile WHERE id=1`).Scan(&points); err != nil || points != 30 {
		t.Fatalf("points=%d err=%v", points, err)
	}
	if _, err = executeLifeData(lifeDataInput{Module: "training", Action: "update", ID: id, Status: "failed"}, "ai"); err != nil {
		t.Fatal(err)
	}
	var punishments int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM training_punishments WHERE task_id=?`, id).Scan(&punishments); err != nil || punishments != 1 {
		t.Fatalf("punishments=%d err=%v", punishments, err)
	}
}

func TestLifeDataToolDefinitionAndRunner(t *testing.T) {
	initLifeDataTestDB(t)
	tool := lifeDataToolDefinition()
	if tool.Function.Name != "manage_life_data" {
		t.Fatalf("tool=%#v", tool)
	}
	raw := runLifeDataTool(`{"module":"notes","action":"create","content":"AI 纸条","tags":["AI"]}`, 1)
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result["ok"] != true {
		t.Fatalf("result=%s err=%v", raw, err)
	}
}

func TestExplicitLifeDataIntent(t *testing.T) {
	for _, message := range []string{"看看今天的账本", "在日迹记录今天", "列出调教室任务", "搜索本子里的计划"} {
		if !explicitLifeDataRequested(message) {
			t.Fatalf("intent not detected: %s", message)
		}
	}
}
