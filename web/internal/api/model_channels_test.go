package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"myapp/internal/db"
)

const memoryChannel = "memory"

func TestModelChannelsPersistIndependentlyAndReturnKeysToSettings(t *testing.T) {
	setupAPITestDB(t)
	body := `{"reply":{"base_url":"https://reply.example/v1","model":"reply-model","api_key":"reply-secret"},"grok":{"base_url":"https://grok.example/v1","model":"grok-model","api_key":"grok-secret"},"memory_rhys":{"base_url":"https://rhys-b.example/v1","model":"rhys-b-model","api_key":"rhys-b-secret"},"memory_grok":{"base_url":"https://tail-b.example/v1","model":"tail-b-model","api_key":"tail-b-secret"}}`
	post := httptest.NewRecorder()
	handleModelChannels(post, httptest.NewRequest(http.MethodPost, "/api/model-channels", strings.NewReader(body)))
	if post.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", post.Code, post.Body.String())
	}
	get := httptest.NewRecorder()
	handleModelChannels(get, httptest.NewRequest(http.MethodGet, "/api/model-channels", nil))
	var result struct {
		Channels map[string]modelChannelView `json:"channels"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Channels) != 5 || result.Channels[replyChannel].Model != "reply-model" || result.Channels[grokChannel].Model != "grok-model" || result.Channels[rhysMemoryChannel].Model != "rhys-b-model" || result.Channels[grokMemoryChannel].Model != "tail-b-model" {
		t.Fatalf("unexpected channels: %#v", result.Channels)
	}
	if _, exposed := result.Channels[memoryChannel]; exposed {
		t.Fatal("legacy shared memory channel must not be exposed")
	}
	if result.Channels[replyChannel].APIKey != "reply-secret" || result.Channels[grokChannel].APIKey != "grok-secret" || result.Channels[rhysMemoryChannel].APIKey != "rhys-b-secret" || result.Channels[grokMemoryChannel].APIKey != "tail-b-secret" {
		t.Fatalf("settings did not receive saved keys: %#v", result.Channels)
	}
	if got := loadModelChannel(grokChannel); got.APIKey != "grok-secret" || got.BaseURL != "https://grok.example/v1" {
		t.Fatalf("grok config=%#v", got)
	}
	if got := loadModelChannel(rhysMemoryChannel); got.APIKey != "rhys-b-secret" || got.BaseURL != "https://rhys-b.example/v1" {
		t.Fatalf("Rhys B config=%#v", got)
	}
}

func TestModelChannelPatchKeepsUnchangedFields(t *testing.T) {
	setupAPITestDB(t)
	body := `{"reply":{"base_url":"https://reply.example/v1","model":"model-1","api_key":"secret","assistant_name":"Rhys"}}`
	first := httptest.NewRecorder()
	handleModelChannels(first, httptest.NewRequest(http.MethodPost, "/api/model-channels", strings.NewReader(body)))
	patch := httptest.NewRecorder()
	handleModelChannels(patch, httptest.NewRequest(http.MethodPost, "/api/model-channels", strings.NewReader(`{"reply":{"model":"model-2"}}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	got := loadModelChannel(replyChannel)
	if got.Model != "model-2" || got.BaseURL != "https://reply.example/v1" || got.APIKey != "secret" || got.AssistantName != "Rhys" {
		t.Fatalf("patch overwrote unchanged fields: %#v", got)
	}
}

func TestAssistantMemoryChannelsAreIndependentAndMayBeBlank(t *testing.T) {
	setupAPITestDB(t)
	if memoryChannelForAssistant("rhys") != rhysMemoryChannel || memoryChannelForAssistant("grok") != grokMemoryChannel {
		t.Fatal("assistant memory channel routing is not independent")
	}
	if err := saveModelChannel(rhysMemoryChannel, modelChannelInput{BaseURL: modelChannelString("https://rhys-b.example/v1"), Model: modelChannelString("rhys-b"), APIKey: modelChannelString("rhys-key")}); err != nil {
		t.Fatal(err)
	}
	if err := saveModelChannel(grokMemoryChannel, modelChannelInput{BaseURL: modelChannelString(""), Model: modelChannelString(""), APIKey: modelChannelString("")}); err != nil {
		t.Fatal(err)
	}
	if !modelChannelComplete(loadModelChannel(rhysMemoryChannel)) {
		t.Fatal("Rhys B should be complete")
	}
	if modelChannelComplete(loadModelChannel(grokMemoryChannel)) {
		t.Fatal("blank Tail B should fall back to Tail A")
	}
	if got := loadModelChannel(rhysMemoryChannel).Model; got != "rhys-b" {
		t.Fatalf("Rhys B model=%q", got)
	}
}

func TestModelChannelBlankKeyKeepsExistingSecret(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(memoryChannel, modelChannelInput{Model: modelChannelString("m1"), APIKey: modelChannelString("keep-me")}); err != nil {
		t.Fatal(err)
	}
	if err := saveModelChannel(memoryChannel, modelChannelInput{Model: modelChannelString("m2")}); err != nil {
		t.Fatal(err)
	}
	if got := loadModelChannel(memoryChannel); got.APIKey != "keep-me" || got.Model != "m2" {
		t.Fatalf("config=%#v", got)
	}
}

func TestLegacyGrokDisplayNameNormalizesToTail(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(grokChannel, modelChannelInput{Model: modelChannelString("grok-model"), AssistantName: modelChannelString("Grok")}); err != nil {
		t.Fatal(err)
	}
	if got := loadModelChannel(grokChannel).AssistantName; got != "Tail" {
		t.Fatalf("assistant name=%q, want Tail", got)
	}
}

func TestBackgroundChannelNeverSelectsLegacySharedMemory(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(memoryChannel, modelChannelInput{BaseURL: modelChannelString("https://memory.example/v1"), Model: modelChannelString("memory-model"), APIKey: modelChannelString("memory-key")}); err != nil {
		t.Fatal(err)
	}
	if err := saveModelChannel(replyChannel, modelChannelInput{BaseURL: modelChannelString("https://rhys-a.example/v1"), Model: modelChannelString("rhys-a"), APIKey: modelChannelString("rhys-a-key")}); err != nil {
		t.Fatal(err)
	}
	got := backgroundModelChannelForConversation(1)
	if got.APIKey != "rhys-a-key" || got.BaseURL != "https://rhys-a.example/v1" || got.Model != "rhys-a" {
		t.Fatalf("background config selected legacy shared memory: %#v", got)
	}
}

func TestBackgroundChannelsRouteByAssistantAndFallbackToA(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO conversations(id,title,assistant) VALUES(2,'Tail','grok')`); err != nil {
		t.Fatal(err)
	}
	configs := map[string]modelChannelConfig{
		replyChannel:      {APIKey: "rhys-a-key", BaseURL: "https://rhys-a.example/v1", Model: "rhys-a"},
		grokChannel:       {APIKey: "tail-a-key", BaseURL: "https://tail-a.example/v1", Model: "tail-a"},
		rhysMemoryChannel: {APIKey: "rhys-b-key", BaseURL: "https://rhys-b.example/v1", Model: "rhys-b"},
		grokMemoryChannel: {APIKey: "tail-b-key", BaseURL: "https://tail-b.example/v1", Model: "tail-b"},
	}
	for channel, cfg := range configs {
		if err := saveModelChannel(channel, modelChannelInput{APIKey: modelChannelString(cfg.APIKey), BaseURL: modelChannelString(cfg.BaseURL), Model: modelChannelString(cfg.Model)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := backgroundModelChannelForConversation(1); got.Model != "rhys-b" || got.APIKey != "rhys-b-key" {
		t.Fatalf("Rhys background config=%#v", got)
	}
	if got := backgroundModelChannelForConversation(2); got.Model != "tail-b" || got.APIKey != "tail-b-key" {
		t.Fatalf("Tail background config=%#v", got)
	}
	if err := saveModelChannel(grokMemoryChannel, modelChannelInput{APIKey: modelChannelString(""), BaseURL: modelChannelString(""), Model: modelChannelString("")}); err != nil {
		t.Fatal(err)
	}
	if got := backgroundModelChannelForConversation(2); got.Model != "tail-a" || got.APIKey != "tail-a-key" {
		t.Fatalf("blank Tail B did not fall back to Tail A: %#v", got)
	}
}

func TestBackgroundChannelUsesLatestSavedBFieldsAndFallsBackPerField(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(replyChannel, modelChannelInput{
		APIKey: modelChannelString("a-key"), BaseURL: modelChannelString("https://a.example/v1"), Model: modelChannelString("a-model"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveModelChannel(rhysMemoryChannel, modelChannelInput{
		BaseURL: modelChannelString("https://b.example/v1"), Model: modelChannelString("b-model"),
	}); err != nil {
		t.Fatal(err)
	}

	got := loadModelChannel(rhysMemoryChannel)
	if got.APIKey != "a-key" || got.BaseURL != "https://b.example/v1" || got.Model != "b-model" {
		t.Fatalf("partial B config did not inherit per field: %#v", got)
	}

	if err := saveModelChannel(rhysMemoryChannel, modelChannelInput{
		APIKey: modelChannelString("b-key-2"), BaseURL: modelChannelString("https://b2.example/v1"), Model: modelChannelString("b-model-2"),
	}); err != nil {
		t.Fatal(err)
	}
	got = loadModelChannel(rhysMemoryChannel)
	if got.APIKey != "b-key-2" || got.BaseURL != "https://b2.example/v1" || got.Model != "b-model-2" {
		t.Fatalf("latest saved B config was not loaded: %#v", got)
	}
}

func TestSavedChannelValuesReachUpstreamRequest(t *testing.T) {
	setupAPITestDB(t)
	if err := saveModelChannel(rhysMemoryChannel, modelChannelInput{
		APIKey: modelChannelString("synced-test-key"), BaseURL: modelChannelString("https://synced.example/v1"), Model: modelChannelString("synced-test-model"),
	}); err != nil {
		t.Fatal(err)
	}

	originalClient := httpClient
	t.Cleanup(func() { httpClient = originalClient })
	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload chatRequest
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if req.URL.String() != "https://synced.example/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer synced-test-key" || payload.Model != "synced-test-model" {
			t.Fatalf("upstream request was not synchronized: url=%s auth=%q model=%q", req.URL, req.Header.Get("Authorization"), payload.Model)
		}
		t.Logf("verified synchronized upstream request: url=%s auth=%q model=%q", req.URL, req.Header.Get("Authorization"), payload.Model)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
	})}

	cfg := loadModelChannel(rhysMemoryChannel)
	if _, err := CallWithToolsChoiceChat(cfg.APIKey, cfg.BaseURL, cfg.Model, []ChatMessage{{Role: "user", Content: "sync check"}}, 32, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMissingBackgroundChannelDoesNotUseLegacyMemoryEnvironment(t *testing.T) {
	setupAPITestDB(t)
	t.Setenv("MEMORY_API_KEY", "legacy-env-key")
	t.Setenv("MEMORY_BASE_URL", "https://legacy-env.example/v1")
	t.Setenv("MEMORY_MODEL", "legacy-env-model")
	if err := saveModelChannel(replyChannel, modelChannelInput{APIKey: modelChannelString("rhys-a-key"), BaseURL: modelChannelString("https://rhys-a.example/v1"), Model: modelChannelString("rhys-a")}); err != nil {
		t.Fatal(err)
	}
	if got := backgroundModelChannelForConversation(1); got.Model != "rhys-a" || got.APIKey != "rhys-a-key" {
		t.Fatalf("legacy MEMORY_* environment hijacked background routing: %#v", got)
	}
}

func TestSavingChannelsRetriesPendingMemoryImmediately(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO global_memory_candidates(conversation_id,user_message,assistant_message,status,attempts,last_error,next_attempt_at) VALUES(1,'u','a','pending',3,'old failure','2099-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	body := `{"reply":{"model":"reply-model","api_key":"reply-key"},"grok":{"model":"grok-model","api_key":"grok-key"},"memory":{"model":"memory-model","api_key":"memory-key"}}`
	recorder := httptest.NewRecorder()
	handleModelChannels(recorder, httptest.NewRequest(http.MethodPost, "/api/model-channels", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var attempts int
	var lastError, nextAttempt string
	if err := db.DB.QueryRow(`SELECT attempts,last_error,next_attempt_at FROM global_memory_candidates LIMIT 1`).Scan(&attempts, &lastError, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || lastError != "" || strings.HasPrefix(nextAttempt, "2099-") {
		t.Fatalf("attempts=%d lastError=%q nextAttempt=%q", attempts, lastError, nextAttempt)
	}
}
