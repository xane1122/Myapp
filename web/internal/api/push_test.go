package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"myapp/internal/db"
)

func TestUsesMyAppBetaNotifications(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		want      bool
	}{
		{name: "current beta shell", userAgent: "Mozilla/5.0 Mobile/15E148 Safari/604.1 MyAppBeta/0.1", want: true},
		{name: "future beta version", userAgent: "MyAppBeta/1.0", want: true},
		{name: "ordinary safari", userAgent: "Mozilla/5.0 Mobile/15E148 Safari/604.1", want: false},
		{name: "similar token", userAgent: "MyAppBetaPreview/0.1", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
			req.Header.Set("User-Agent", tt.userAgent)
			if got := usesMyAppBetaNotifications(req); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	if usesMyAppBetaNotifications(nil) {
		t.Fatal("nil request must not suppress Bark")
	}
}

func TestShouldDeletePushSubscription(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusNotFound, true},
		{http.StatusGone, true},
		{http.StatusUnauthorized, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusCreated, false},
	}
	for _, tt := range tests {
		if got := shouldDeletePushSubscription(tt.status); got != tt.want {
			t.Fatalf("status %d: got %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestAppIsForegroundUsesExplicitVisibilityAndFreshness(t *testing.T) {
	setupAPITestDB(t)
	now := time.Now().UTC()
	if err := recordAppPresence(1, "pwa"); err != nil {
		t.Fatal(err)
	}
	if !appIsForeground(now.Add(time.Second)) {
		t.Fatal("fresh visible activity should mark app foreground")
	}
	if err := recordAppPresence(1, "pwa_hidden"); err != nil {
		t.Fatal(err)
	}
	if appIsForeground(now.Add(time.Second)) {
		t.Fatal("explicit hidden activity must mark app away immediately")
	}
	if _, err := db.DB.Exec(`UPDATE wake_activity SET source='pwa',last_seen_at=datetime('now','-2 minutes') WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if appIsForeground(now) {
		t.Fatal("stale visible heartbeat must not keep app foreground")
	}
}
