package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func withAppearanceTestDir(t *testing.T) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func appearanceRequest(t *testing.T, handler http.Handler, method, body, match string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/appearance", strings.NewReader(body))
	req.Header.Set("Origin", "https://xanelove.com")
	if match != "" {
		req.Header.Set("If-Match", match)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestAppearanceRequiresRevisionAndPersistsSeparately(t *testing.T) {
	withAppearanceTestDir(t)
	t.Setenv("APP_BEARER_TOKEN", "")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	handler := Handler()

	get := appearanceRequest(t, handler, http.MethodGet, "", "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"appearance-0"` {
		t.Fatalf("initial GET status=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
	missing := appearanceRequest(t, handler, http.MethodPut, `{"target":"all","appearance":{"x":50,"y":50,"zoom":1}}`, "")
	if missing.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing precondition status=%d body=%s", missing.Code, missing.Body.String())
	}
	put := appearanceRequest(t, handler, http.MethodPut, `{"target":"all","appearance":{"x":50,"y":45,"zoom":1.2,"iconColor":"#cfaeb5"}}`, `"appearance-0"`)
	if put.Code != http.StatusOK || put.Header().Get("ETag") != `"appearance-1"` {
		t.Fatalf("PUT status=%d etag=%q body=%s", put.Code, put.Header().Get("ETag"), put.Body.String())
	}
	if _, err := os.Stat(appearancePath); err != nil {
		t.Fatalf("appearance file missing: %v", err)
	}
	if _, err := os.Stat("data/memories.db"); !os.IsNotExist(err) {
		t.Fatalf("appearance endpoint unexpectedly created chat database: %v", err)
	}

	conflict := appearanceRequest(t, handler, http.MethodPut, `{"target":"home","appearance":{"x":50,"y":50,"zoom":1}}`, `"appearance-0"`)
	if conflict.Code != http.StatusConflict || conflict.Header().Get("ETag") != `"appearance-1"` {
		t.Fatalf("conflict status=%d etag=%q body=%s", conflict.Code, conflict.Header().Get("ETag"), conflict.Body.String())
	}
	var state appearanceState
	if err := json.NewDecoder(bytes.NewReader(conflict.Body.Bytes())).Decode(&state); err != nil || state.Revision != 1 {
		t.Fatalf("conflict did not return current state: revision=%d err=%v", state.Revision, err)
	}
}

func TestAppearanceRejectsUnknownFieldsTargetsAndImages(t *testing.T) {
	withAppearanceTestDir(t)
	t.Setenv("APP_BEARER_TOKEN", "")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	handler := Handler()
	tests := []string{
		`{"target":"unknown","appearance":{"x":50,"y":50,"zoom":1}}`,
		`{"target":"all","appearance":{"x":50,"y":50,"zoom":1,"admin":true}}`,
		`{"target":"all","appearance":{"image":"data:image/svg+xml;base64,PHN2Zz4=","x":50,"y":50,"zoom":1}}`,
		`{"target":"all","appearance":{"x":50,"y":50,"zoom":3}}`,
	}
	for _, body := range tests {
		response := appearanceRequest(t, handler, http.MethodPut, body, `"appearance-0"`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestAppearanceResetUsesTombstone(t *testing.T) {
	withAppearanceTestDir(t)
	t.Setenv("APP_BEARER_TOKEN", "")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	handler := Handler()
	response := appearanceRequest(t, handler, http.MethodPut, `{"target":"chat","appearance":{"deleted":true}}`, `"appearance-0"`)
	if response.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", response.Code, response.Body.String())
	}
	var state appearanceState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil || !state.Targets["chat"].Deleted {
		t.Fatalf("reset tombstone missing: %#v err=%v", state.Targets["chat"], err)
	}
}
