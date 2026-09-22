package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"myapp/internal/memory"
	"myapp/internal/tts"
)

func TestMessageAudioGeneratesMissingAssistantAudio(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("APP_BEARER_TOKEN", "test-token")
	t.Setenv("APP_ALLOWED_ORIGIN", "https://xanelove.com")
	t.Setenv("EDGE_TTS_AUDIO_DIR", t.TempDir())
	oldService, oldSynthesize := edgeTTS, synthesizeReplyAudio
	edgeTTS = tts.NewServiceFromEnv()
	synthesizeReplyAudio = func(_ context.Context, text string, messageID int64) (string, error) {
		if text != "你好" || messageID <= 0 {
			t.Fatalf("text=%q messageID=%d", text, messageID)
		}
		return "/audio/generated.mp3", nil
	}
	t.Cleanup(func() {
		edgeTTS = oldService
		synthesizeReplyAudio = oldSynthesize
	})

	messageID, err := memory.SaveMessage(1, "assistant", `{"reply":"你好"}`)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"conversation_id":1,"message_id":` + strconv.FormatInt(messageID, 10) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/api/message-audio", body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "/audio/generated.mp3") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
