package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type providerModelsRoundTripper func(*http.Request) (*http.Response, error)

func (fn providerModelsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestProviderModelsEndpoint(t *testing.T) {
	got, err := providerModelsEndpoint("https://api.meeyo.org/v1")
	if err != nil || got != "https://api.meeyo.org/v1/models" {
		t.Fatalf("endpoint=%q err=%v", got, err)
	}
	got, err = providerModelsEndpoint("https://api.meeyo.org/v1/chat/completions")
	if err != nil || got != "https://api.meeyo.org/v1/models" {
		t.Fatalf("full endpoint=%q err=%v", got, err)
	}
	if _, err := providerModelsEndpoint("http://localhost:8080/v1"); err == nil {
		t.Fatal("expected unsafe endpoint to be rejected")
	}
}

func TestHandleProviderModels(t *testing.T) {
	oldClient := providerModelsHTTPClient
	providerModelsHTTPClient = &http.Client{Transport: providerModelsRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.meeyo.org/v1/models" || req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected provider request: %s auth=%q", req.URL, req.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`)), Header: make(http.Header)}, nil
	})}
	t.Cleanup(func() { providerModelsHTTPClient = oldClient })

	req := httptest.NewRequest(http.MethodPost, "/api/provider-models", strings.NewReader(`{"api_key":"test-key","api_base_url":"https://api.meeyo.org/v1"}`))
	rec := httptest.NewRecorder()
	handleProviderModels(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"model-a"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
