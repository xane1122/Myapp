package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/db"
)

func TestDaysMatterCreateToolIsForcedAndCreatesEvent(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "days-matter-tool.sqlite"))
	if !explicitDaysMatterCreateRequested("帮我在 DaysMatter 新建纪念日") {
		t.Fatal("DaysMatter request was not detected")
	}
	names := explicitToolNames("帮我在 DaysMatter 新建纪念日")
	if len(names) != 1 || names[0] != "days_matter_create" {
		t.Fatalf("tool names=%v", names)
	}
	result := runDaysMatterCreateTool(`{"event_name":"第一次见面","event_date":"2025-06-01","note":"纪念日","is_favorite":true}`)
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("result=%s", result)
	}
	var name, date, direction, note string
	var favorite int
	if err := db.DB.QueryRow(`SELECT event_name,event_date,direction,note,is_favorite FROM days_matter_events`).Scan(&name, &date, &direction, &note, &favorite); err != nil {
		t.Fatal(err)
	}
	if name != "第一次见面" || date != "2025-06-01" || direction != "count_up" || note != "纪念日" || favorite != 1 {
		t.Fatalf("name=%q date=%q direction=%q note=%q favorite=%d", name, date, direction, note, favorite)
	}
}

func TestDaysMatterCreateToolMaintainsSingleFavorite(t *testing.T) {
	if db.DB != nil {
		_ = db.DB.Close()
	}
	db.Init(filepath.Join(t.TempDir(), "days-matter-favorite-tool.sqlite"))
	runDaysMatterCreateTool(`{"event_name":"旧事件","event_date":"2025-01-01","is_favorite":true}`)
	runDaysMatterCreateTool(`{"event_name":"新事件","event_date":"2026-01-01","direction":"count_down","is_favorite":true}`)
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM days_matter_events WHERE is_favorite=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("favorite count=%d err=%v", count, err)
	}
}

func TestDaysMatterCreateToolRejectsInvalidArguments(t *testing.T) {
	for _, arguments := range []string{
		`{"event_name":"","event_date":"2025-01-01"}`,
		`{"event_name":"旅行","event_date":"bad"}`,
		`{"event_name":"旅行","event_date":"2025-01-01","direction":"later"}`,
		`{"event_name":"旅行","event_date":"2025-01-01","note":"` + strings.Repeat("字", 501) + `"}`,
	} {
		if result := runDaysMatterCreateTool(arguments); !strings.Contains(result, `"error"`) {
			t.Fatalf("arguments=%s result=%s", arguments, result)
		}
	}
}
