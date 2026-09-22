package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	t.Run("allows passwordless access when token is empty", func(t *testing.T) {
		t.Setenv("APP_BEARER_TOKEN", "")
		t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
		recorder := httptest.NewRecorder()
		withAPIAuth(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
		}
	})

	t.Run("passwordless mode still rejects foreign origins", func(t *testing.T) {
		t.Setenv("APP_BEARER_TOKEN", "")
		t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Origin", "https://attacker.example")
		recorder := httptest.NewRecorder()
		withAPIAuth(next).ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	handler := withAPIAuth(next)

	t.Run("rejects missing token", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
	})

	t.Run("rejects a foreign origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Origin", "https://attacker.example")
		req.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("allows configured origin and valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Origin", "https://xanelove.com")
		req.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://xanelove.com" {
			t.Fatalf("allow origin = %q", got)
		}
	})

	t.Run("allows configured origin without login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
		req.Header.Set("Origin", "https://xanelove.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
		}
	})

	t.Run("allows the actual same origin behind a reverse proxy", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/diaries", nil)
		req.Host = "www.xanelove.com"
		req.Header.Set("Origin", "https://www.xanelove.com")
		req.Header.Set("X-Forwarded-Host", "www.xanelove.com")
		req.Header.Set("X-Forwarded-Proto", "https")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://www.xanelove.com" {
			t.Fatalf("allow origin = %q", got)
		}
	})

	t.Run("rejects a forged origin with the same hostname text", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/diaries", nil)
		req.Host = "www.xanelove.com"
		req.Header.Set("Origin", "https://www.xanelove.com.attacker.example")
		req.Header.Set("X-Forwarded-Host", "www.xanelove.com")
		req.Header.Set("X-Forwarded-Proto", "https")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("allows same-origin browser fetch without login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
		}
	})

	t.Run("allows preflight without bearer token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/api/health", nil)
		req.Header.Set("Origin", "https://xanelove.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization" {
			t.Fatalf("allow headers = %q", got)
		}
	})

	t.Run("delegates screen upload to its dedicated token handler", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/screen/upload?token=screen-token", nil))
		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
		}
	})
}

func TestAuthLoginCreatesHTTPOnlySession(t *testing.T) {
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"token":"test-token"}`))
	recorder := httptest.NewRecorder()
	handleAuthLogin(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Value == "" {
		t.Fatalf("expected a non-empty HttpOnly session cookie, got %#v", cookies)
	}
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	authenticatedReq := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	authenticatedReq.AddCookie(cookies[0])
	authenticated := httptest.NewRecorder()
	withAPIAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })).ServeHTTP(authenticated, authenticatedReq)
	if authenticated.Code != http.StatusTeapot {
		t.Fatalf("session-authenticated status = %d", authenticated.Code)
	}
}

func TestAuthConfigDoesNotExposeBearerToken(t *testing.T) {
	t.Setenv("APP_BEARER_TOKEN", "must-not-leak")
	recorder := httptest.NewRecorder()
	handleAuthConfig(recorder, httptest.NewRequest(http.MethodGet, "/auth-config.js", nil))
	if bytes.Contains(recorder.Body.Bytes(), []byte("must-not-leak")) {
		t.Fatalf("auth config exposed the bearer token: %s", recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("APP_AUTH_ENABLED=true")) {
		t.Fatalf("auth config did not report enabled state: %s", recorder.Body.String())
	}
}
