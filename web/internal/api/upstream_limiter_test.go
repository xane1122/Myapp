package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamLimiterUsesEightyPercentOfProviderRPM(t *testing.T) {
	t.Setenv("AI_PROVIDER_RPM", "10")
	limiter := newUpstreamRequestLimiter()
	for i := 0; i < 8; i++ {
		if err := limiter.Acquire("key", upstreamChat); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	limiter.mu.Lock()
	got := limiter.effectiveRPMLocked(limiter.states["key"])
	limiter.mu.Unlock()
	if got != 8 {
		t.Fatalf("effective RPM = %d, want 8", got)
	}
}

func TestUpstreamLimiterSkipsBackgroundBelowThirtyPercentRemaining(t *testing.T) {
	t.Setenv("AI_PROVIDER_RPM", "10")
	limiter := newUpstreamRequestLimiter()
	limiter.mu.Lock()
	state := limiter.stateLocked("key")
	now := limiter.now()
	for i := 0; i < 7; i++ {
		state.starts = append(state.starts, now)
	}
	limiter.mu.Unlock()
	if err := limiter.Acquire("key", upstreamBackground); err == nil || !strings.Contains(err.Error(), "余量低于 30%") {
		t.Fatalf("error = %v", err)
	}
}

func TestUpstreamLimiterCooldownAllowsOnlyChat(t *testing.T) {
	limiter := newUpstreamRequestLimiter()
	limiter.Record429("key")
	if err := limiter.Acquire("key", upstreamBackground); err == nil || !strings.Contains(err.Error(), "仅聊天") {
		t.Fatalf("background error = %v", err)
	}
	if err := limiter.Acquire("key", upstreamTool); err == nil || !strings.Contains(err.Error(), "仅聊天") {
		t.Fatalf("tool error = %v", err)
	}
	if err := limiter.Acquire("key", upstreamChat); err != nil {
		t.Fatalf("chat should pass cooldown: %v", err)
	}
}

func TestUpstreamLimiterDispatchesChatBeforeToolAndBackground(t *testing.T) {
	limiter := newUpstreamRequestLimiter()
	limiter.mu.Lock()
	state := limiter.stateLocked("key")
	background := make(chan error, 1)
	tool := make(chan error, 1)
	chat := make(chan error, 1)
	state.waiters[upstreamBackground] = append(state.waiters[upstreamBackground], background)
	state.waiters[upstreamTool] = append(state.waiters[upstreamTool], tool)
	state.waiters[upstreamChat] = append(state.waiters[upstreamChat], chat)
	state.providerRPM = 2 // Effective capacity is one request per minute.
	limiter.dispatchLocked(state, limiter.now())
	limiter.mu.Unlock()

	select {
	case err := <-chat:
		if err != nil {
			t.Fatalf("chat error = %v", err)
		}
	default:
		t.Fatal("chat request was not dispatched first")
	}
	select {
	case <-tool:
		t.Fatal("tool dispatched before chat budget was exhausted")
	default:
	}
	select {
	case <-background:
		t.Fatal("background dispatched before chat budget was exhausted")
	default:
	}
}

func TestProviderRPMFromHeaders(t *testing.T) {
	if got := providerRPMFromHeaders(http.Header{"X-Ratelimit-Limit-Requests": {"120;w=60"}}); got != 120 {
		t.Fatalf("RPM = %d, want 120", got)
	}
	if got := providerRPMFromHeaders(http.Header{}); got != 0 {
		t.Fatalf("RPM = %d, want 0", got)
	}
}

func TestCooldownDurationIsThirtySeconds(t *testing.T) {
	limiter := newUpstreamRequestLimiter()
	before := time.Now()
	limiter.Record429("key")
	limiter.mu.Lock()
	until := limiter.states["key"].cooldownUntil
	limiter.mu.Unlock()
	if delta := until.Sub(before); delta < 29*time.Second || delta > 31*time.Second {
		t.Fatalf("cooldown = %v", delta)
	}
}
