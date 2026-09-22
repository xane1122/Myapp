package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"myapp/internal/db"
)

func TestPushSubscriptionStoresUserAndKeys(t *testing.T) {
	setupAPITestDB(t)
	payload := map[string]interface{}{
		"user_id":  "device-user",
		"endpoint": "https://push.example/subscription",
		"keys":     map[string]string{"p256dh": "p256dh-value", "auth": "auth-value"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/push", bytes.NewReader(body))
	req.Header.Set("User-Agent", "push-test")
	rec := httptest.NewRecorder()
	handlePush(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var userID, endpoint, keysJSON, p256dh, auth, userAgent string
	if err := db.DB.QueryRow(`SELECT user_id,endpoint,keys_json,p256dh,auth,user_agent FROM push_subscriptions`).Scan(&userID, &endpoint, &keysJSON, &p256dh, &auth, &userAgent); err != nil {
		t.Fatal(err)
	}
	if userID != "device-user" || endpoint != "https://push.example/subscription" || p256dh != "p256dh-value" || auth != "auth-value" || userAgent != "push-test" {
		t.Fatalf("unexpected subscription: user=%q endpoint=%q p256dh=%q auth=%q ua=%q", userID, endpoint, p256dh, auth, userAgent)
	}
	var keys map[string]string
	if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
		t.Fatal(err)
	}
	if keys["p256dh"] != p256dh || keys["auth"] != auth {
		t.Fatalf("keys_json does not match key columns: %#v", keys)
	}
}

func TestPushSubscriptionUserIDFallsBackToEndpoint(t *testing.T) {
	first := pushSubscriptionUserID("", "https://push.example/subscription")
	second := pushSubscriptionUserID("", "https://push.example/subscription")
	if first == "" || first != second {
		t.Fatalf("fallback user id must be stable: first=%q second=%q", first, second)
	}
}

func TestPushTestRejectsUnsupportedMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	handlePushTest(rec, httptest.NewRequest(http.MethodGet, "/api/push/test", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
