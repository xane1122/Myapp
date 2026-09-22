package api

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// upstreamRequestLimiter enforces a per-credential rolling request budget.
// It deliberately counts request starts: provider RPM limits normally do too.
type upstreamRequestLimiter struct {
	mu     sync.Mutex
	states map[string]*upstreamLimitState
	now    func() time.Time
}

type upstreamLimitState struct {
	starts        []time.Time
	providerRPM   int
	cooldownUntil time.Time
	waiters       [3][]chan error
	timer         *time.Timer
}

func newUpstreamRequestLimiter() *upstreamRequestLimiter {
	limiter := &upstreamRequestLimiter{states: make(map[string]*upstreamLimitState), now: time.Now}
	rpm := configuredProviderRPM()
	log.Printf("upstream rate limiter configured: configured_rpm=%d effective_rpm=%d", rpm, max(1, int(math.Floor(float64(rpm)*.80))))
	return limiter
}

func (l *upstreamRequestLimiter) Acquire(key string, priority upstreamPriority) error {
	ready := make(chan error, 1)
	l.mu.Lock()
	state := l.stateLocked(key)
	now := l.now()
	l.pruneLocked(state, now)
	if priority != upstreamChat && now.Before(state.cooldownUntil) {
		l.mu.Unlock()
		return fmt.Errorf("上游限流冷却中，仅聊天请求可继续（%d 秒后恢复）", int(math.Ceil(time.Until(state.cooldownUntil).Seconds())))
	}
	if priority == upstreamBackground && l.remainingLocked(state) < l.backgroundReserveLocked(state) {
		l.mu.Unlock()
		return fmt.Errorf("后台 AI 任务已跳过：上游请求余量低于 30%%")
	}
	state.waiters[priority] = append(state.waiters[priority], ready)
	l.dispatchLocked(state, now)
	l.mu.Unlock()
	return <-ready
}

func (l *upstreamRequestLimiter) Record429(key string) {
	l.mu.Lock()
	state := l.stateLocked(key)
	state.cooldownUntil = l.now().Add(providerCooldown)
	l.mu.Unlock()
}

func (l *upstreamRequestLimiter) ObserveLimit(key string, rpm int) {
	if rpm <= 0 {
		return
	}
	l.mu.Lock()
	state := l.stateLocked(key)
	if state.providerRPM == 0 || rpm < state.providerRPM {
		state.providerRPM = rpm
	}
	l.mu.Unlock()
}

func (l *upstreamRequestLimiter) stateLocked(key string) *upstreamLimitState {
	state := l.states[key]
	if state == nil {
		state = &upstreamLimitState{providerRPM: configuredProviderRPM()}
		l.states[key] = state
	}
	return state
}

func (l *upstreamRequestLimiter) dispatchLocked(state *upstreamLimitState, now time.Time) {
	l.pruneLocked(state, now)
	for l.remainingLocked(state) > 0 {
		priority := l.nextPriorityLocked(state)
		if priority < 0 {
			break
		}
		waiter := state.waiters[priority][0]
		state.waiters[priority] = state.waiters[priority][1:]
		if priority == int(upstreamBackground) && l.remainingLocked(state) < l.backgroundReserveLocked(state) {
			waiter <- fmt.Errorf("后台 AI 任务已跳过：上游请求余量低于 30%%")
			continue
		}
		state.starts = append(state.starts, now)
		waiter <- nil
	}
	if l.nextPriorityLocked(state) >= 0 && len(state.starts) > 0 {
		delay := state.starts[0].Add(time.Minute).Sub(now)
		if delay < 0 {
			delay = 0
		}
		if state.timer == nil {
			state.timer = time.AfterFunc(delay, func() {
				l.mu.Lock()
				state.timer = nil
				l.dispatchLocked(state, l.now())
				l.mu.Unlock()
			})
		}
	}
}

func (l *upstreamRequestLimiter) pruneLocked(state *upstreamLimitState, now time.Time) {
	cutoff := now.Add(-time.Minute)
	first := 0
	for first < len(state.starts) && !state.starts[first].After(cutoff) {
		first++
	}
	state.starts = state.starts[first:]
}

func (l *upstreamRequestLimiter) remainingLocked(state *upstreamLimitState) int {
	return l.effectiveRPMLocked(state) - len(state.starts)
}

func (l *upstreamRequestLimiter) backgroundReserveLocked(state *upstreamLimitState) int {
	return int(math.Ceil(float64(l.effectiveRPMLocked(state)) * .30))
}

func (l *upstreamRequestLimiter) effectiveRPMLocked(state *upstreamLimitState) int {
	return max(1, int(math.Floor(float64(max(1, state.providerRPM))*.80)))
}

func (l *upstreamRequestLimiter) nextPriorityLocked(state *upstreamLimitState) int {
	for priority := int(upstreamChat); priority >= int(upstreamBackground); priority-- {
		if len(state.waiters[priority]) > 0 {
			return priority
		}
	}
	return -1
}

func configuredProviderRPM() int {
	rpm, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AI_PROVIDER_RPM")))
	if err == nil && rpm > 0 {
		return rpm
	}
	return defaultProviderRPM
}

func providerRPMFromHeaders(header http.Header) int {
	for _, name := range []string{"X-RateLimit-Limit-Requests", "X-RateLimit-Limit", "RateLimit-Limit"} {
		for _, raw := range header.Values(name) {
			value := strings.TrimSpace(strings.Split(raw, ";")[0])
			if rpm, err := strconv.Atoi(value); err == nil && rpm > 0 {
				return rpm
			}
		}
	}
	return 0
}
