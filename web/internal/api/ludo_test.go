package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"myapp/internal/db"
)

type ludoRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ludoRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestLudoActionSupportsPasswordlessBrowserAccess(t *testing.T) {
	t.Setenv("APP_BEARER_TOKEN", "")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	handler := Handler()
	body := `{"api_key":"provider-key","kind":"turn","context":{"position":1}}`

	originalClient := httpClient
	httpClient = &http.Client{Transport: ludoRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != defaultOpenRouterURL {
			t.Fatalf("provider URL=%q", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"dice\":4,\"message\":\"轮到我了。\"}"}}]}`)),
			Request:    req,
		}, nil
	})}
	t.Cleanup(func() { httpClient = originalClient })

	sameOriginReq := httptest.NewRequest(http.MethodPost, "/api/games/ludo/action", strings.NewReader(body))
	sameOriginReq.Header.Set("Origin", "https://xanelove.com")
	sameOrigin := httptest.NewRecorder()
	handler.ServeHTTP(sameOrigin, sameOriginReq)
	if sameOrigin.Code != http.StatusOK {
		t.Fatalf("same-origin status=%d body=%s", sameOrigin.Code, sameOrigin.Body.String())
	}

}

func TestLudoRewardAddsPointsAndCabinetItem(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "ludo.sqlite"))
	if _, err := db.DB.Exec(`INSERT INTO ludo_games(game_id,player_pos) VALUES('test-game-1',25)`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/games/ludo/reward", strings.NewReader(`{"game_id":"test-game-1","tile_id":25}`))
	recorder := httptest.NewRecorder()
	handleLudoReward(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var points, items int
	if err := db.DB.QueryRow(`SELECT total_points FROM training_profile WHERE id=1`).Scan(&points); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM cabinet_items WHERE name='红丝带' AND source='飞行棋奖励'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if points != 25 || items != 1 {
		t.Fatalf("points=%d items=%d", points, items)
	}
}

func TestLudoRewardCannotBeClaimedTwice(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "ludo-replay.sqlite"))
	if _, err := db.DB.Exec(`INSERT INTO ludo_games(game_id,player_pos) VALUES('test-game-2',50)`); err != nil {
		t.Fatal(err)
	}
	body := `{"game_id":"test-game-2","tile_id":50}`
	for attempt, want := range []int{http.StatusOK, http.StatusConflict} {
		recorder := httptest.NewRecorder()
		handleLudoReward(recorder, httptest.NewRequest(http.MethodPost, "/api/games/ludo/reward", strings.NewReader(body)))
		if recorder.Code != want {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
		}
	}
}

func TestLudoRewardRejectsUnreachedTile(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "ludo-unreached.sqlite"))
	if _, err := db.DB.Exec(`INSERT INTO ludo_games(game_id,player_pos) VALUES('test-game-3',1)`); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handleLudoReward(recorder, httptest.NewRequest(http.MethodPost, "/api/games/ludo/reward", strings.NewReader(`{"game_id":"test-game-3","tile_id":25}`)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLudoDestinationResolvesChainedMovement(t *testing.T) {
	if got := ludoDestination(49); got != 51 {
		t.Fatalf("destination=%d want=51", got)
	}
	if got := ludoDestination(10); got != 9 {
		t.Fatalf("destination=%d want=9", got)
	}
}

func TestLudoActionRejectsInvalidKindBeforeProviderCall(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/games/ludo/action", strings.NewReader(`{"kind":"invalid","context":{}}`))
	recorder := httptest.NewRecorder()
	handleLudoAction(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
