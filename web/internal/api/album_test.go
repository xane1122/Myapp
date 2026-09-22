package api

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func TestDecodeAlbumImageRejectsInvalidAndOversizedDimensions(t *testing.T) {
	if _, err := decodeAlbumImage([]byte("not an image")); err == nil {
		t.Fatal("invalid image was accepted")
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, maxAlbumImageSide+1, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAlbumImage(encoded.Bytes()); err == nil || !strings.Contains(err.Error(), "超过限制") {
		t.Fatalf("oversized dimensions error = %v", err)
	}
}

func TestAlbumJSONEndpointsRejectUnknownAndTrailingFields(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	h := Handler()
	for _, body := range []string{`{"is_favorite":true,"admin":true}`, `{"is_favorite":true}{}`} {
		bellRequest(t, h, http.MethodPut, "/api/album/1/favorite", []byte(body), http.StatusBadRequest)
	}
}

func TestAlbumFavoriteMissingPhotoReturnsNotFound(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	bellRequest(t, Handler(), http.MethodPut, "/api/album/999/favorite", []byte(`{"is_favorite":true}`), http.StatusNotFound)
}

func TestAlbumDeleteRejectsPathsOutsideAlbumStorage(t *testing.T) {
	setupAPITestDB(t)
	result, err := db.DB.Exec(`INSERT INTO album_photos(uploader,file_path,thumb_path,file_size,width,height,mime_type) VALUES('Xane',?, ?,1,1,1,'image/jpeg')`, filepath.Join(t.TempDir(), "outside.jpg"), filepath.Join(t.TempDir(), "outside-thumb.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	photo, err := memory.GetAlbumPhoto(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageAlbumDelete(photo); err == nil {
		t.Fatal("path outside album storage was accepted")
	}
}

func TestAlbumDeleteAllowsAIPhotoWithMissingFiles(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	full := filepath.Join(uploadRoot, "album", "full", "missing.jpg")
	thumb := filepath.Join(uploadRoot, "album", "thumb", "missing.jpg")
	result, err := db.DB.Exec(`INSERT INTO album_photos(uploader,file_path,thumb_path,file_size,width,height,mime_type) VALUES('Rhys',?,?,0,1,1,'image/jpeg')`, full, thumb)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	bellRequest(t, Handler(), http.MethodDelete, fmt.Sprintf("/api/album/%d", id), nil, http.StatusOK)
	if _, err = memory.GetAlbumPhoto(id); err != sql.ErrNoRows {
		t.Fatalf("album row still exists: %v", err)
	}
}

func TestRandomAlbumNameUsesUnpredictableHex(t *testing.T) {
	first, err := randomAlbumName()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomAlbumName()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("unexpected generated names %q %q", first, second)
	}
}
