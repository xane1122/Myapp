package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"myapp/internal/db"
)

func TestDaysMatterCRUDFavoriteSortingAndImageCleanup(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "days-matter.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	imageDir := filepath.Join(uploadRoot, "days_matter")
	imageName := "days-matter-test-cleanup.png"
	imagePath := filepath.Join(imageDir, imageName)
	t.Cleanup(func() { _ = os.Remove(imagePath) })
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, []byte("image"), 0644); err != nil {
		t.Fatal(err)
	}

	h := Handler()
	future := bellRequest(t, h, "POST", "/api/days-matter", []byte(`{"event_name":"旅行","event_date":"2099-08-10","direction":"count_down","image_url":"/uploads/days_matter/days-matter-test-cleanup.png","note":"出发","is_favorite":true}`), 200)
	futureID := int64(future["event"].(map[string]interface{})["id"].(float64))
	past := bellRequest(t, h, "POST", "/api/days-matter", []byte(`{"event_name":"纪念日","event_date":"2020-01-02","direction":"count_up","note":"第一次见面","is_favorite":true}`), 200)
	pastID := int64(past["event"].(map[string]interface{})["id"].(float64))

	var favoriteCount int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM days_matter_events WHERE is_favorite=1`).Scan(&favoriteCount); err != nil || favoriteCount != 1 {
		t.Fatalf("favorite count = %d, err = %v", favoriteCount, err)
	}
	list := bellRequest(t, h, "GET", "/api/days-matter", nil, 200)["events"].([]interface{})
	if got := int64(list[0].(map[string]interface{})["id"].(float64)); got != futureID {
		t.Fatalf("first event id = %d, want future id %d", got, futureID)
	}

	toggled := bellRequest(t, h, "POST", "/api/days-matter/"+jsonNumber(futureID)+"/favorite", nil, 200)
	if toggled["is_favorite"] != true {
		t.Fatalf("favorite response = %#v", toggled)
	}
	bellRequest(t, h, "DELETE", "/api/days-matter/"+jsonNumber(futureID), nil, 200)
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		t.Fatalf("image still exists after delete: %v", err)
	}
	bellRequest(t, h, "GET", "/api/days-matter/"+jsonNumber(pastID), nil, 200)
}

func TestDaysMatterRejectsUnsafeJSONAndImagePaths(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	valid := `{"event_name":"旅行","event_date":"2099-08-10","direction":"count_down"}`
	for _, body := range []string{
		valid + `{}`,
		`{"event_name":"旅行","event_date":"2099-08-10","direction":"count_down","admin":true}`,
		`{"event_name":"旅行","event_date":"2099-08-10","direction":"count_down","image_url":"/uploads/days_matter/%2e%2e/secret.jpg"}`,
		`{"event_name":"旅行","event_date":"2099-08-10","direction":"count_down","image_url":"/uploads/days_matter/a/b.jpg"}`,
	} {
		bellRequest(t, h, http.MethodPost, "/api/days-matter", []byte(body), http.StatusBadRequest)
	}
}

func TestDaysMatterSharedImageIsOnlyRemovedAfterLastReference(t *testing.T) {
	setupAPITestDB(t)
	dir := filepath.Join(uploadRoot, "days_matter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "days-matter-shared-test.jpg")
	t.Cleanup(func() { _ = os.Remove(path) })
	if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B"} {
		if _, err := db.DB.Exec(`INSERT INTO days_matter_events(event_name,event_date,direction,image_url) VALUES(?,?,?,?)`, name, "2099-01-01", "count_down", "/uploads/days_matter/days-matter-shared-test.jpg"); err != nil {
			t.Fatal(err)
		}
	}
	deleteDaysMatter(httptest.NewRecorder(), 1)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shared image removed too early: %v", err)
	}
	deleteDaysMatter(httptest.NewRecorder(), 2)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("image should be removed after last reference, err=%v", err)
	}
}

func TestDaysMatterValidation(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "days-matter-validation.sqlite"))
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	bellRequest(t, h, "POST", "/api/days-matter", []byte(`{"event_name":"","event_date":"bad","direction":"later"}`), 400)
}
