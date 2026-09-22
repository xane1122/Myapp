package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchReadableURLExtractsAndTruncates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>测试文章</title></head><body><article><h1>测试文章</h1><p>` + strings.Repeat("正文内容", 100) + `</p></article></body></html>`))
	}))
	defer server.Close()

	result, err := fetchReadableURL(server.URL, 200, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "测试文章" || len([]rune(result.Content)) != 200 || !result.Truncated || result.ContentLength <= 200 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFetchURLRejectsUnsafeTargets(t *testing.T) {
	for _, rawURL := range []string{
		"file:///etc/passwd",
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/",
		"http://localhost/",
		"http://user:pass@example.com/",
	} {
		if _, err := validateFetchURL(t.Context(), rawURL, false); err == nil {
			t.Fatalf("validateFetchURL(%q) succeeded", rawURL)
		}
	}
}

func TestFetchReadableURLStopsAfterThreeRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := strings.Count(strings.Trim(r.URL.Path, "/"), "next")
		if step < 4 {
			http.Redirect(w, r, "/"+strings.Repeat("next/", step+1), http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`<html><body><article><p>不应读取到这里</p></article></body></html>`))
	}))
	defer server.Close()
	if _, err := fetchReadableURL(server.URL, 1000, true); err == nil || !strings.Contains(err.Error(), "超过3次") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrivateIPClassification(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "::1", "fc00::1", "fe80::1"} {
		if !isPrivateIP(net.ParseIP(value)) {
			t.Fatalf("%s should be private", value)
		}
	}
	if isPrivateIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("8.8.8.8 should be public")
	}
}

func TestRunFetchURLToolValidationErrorIsJSON(t *testing.T) {
	output := runFetchURLTool(`{"url":"http://127.0.0.1/"}`)
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] == nil {
		t.Fatalf("output = %s", output)
	}
}
