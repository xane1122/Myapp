package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"myapp/internal/db"
)

type roleplayRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn roleplayRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestRoleplayAdvancePromptKeepsEveryScene(t *testing.T) {
	scenes := make([]roleplayScene, 12)
	for i := range scenes {
		scenes[i].Text = "scene-marker-" + string(rune('A'+i))
	}
	line := roleplayLine{Character: "角色", Chapters: []roleplayChapter{
		{Title: "第一章", Summary: "此前事件"},
		{Title: "第二章", Scenes: scenes},
	}}
	prompt := buildRoleplayAdvancePrompt(line, line.Chapters[1], "继续")
	for _, scene := range scenes {
		if !strings.Contains(prompt, scene.Text) {
			t.Fatalf("prompt missing scene %q", scene.Text)
		}
	}
	if !strings.Contains(prompt, "此前事件") {
		t.Fatal("prompt missing completed chapter summary")
	}
	for _, requirement := range []string{"350 至 650 个汉字", "自然互动节点", "只输出本次续写正文"} {
		if !strings.Contains(prompt, requirement) {
			t.Fatalf("prompt missing concise output requirement %q", requirement)
		}
	}
}

func TestRoleplayAdvanceUsesReducedOutputBudget(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-output-budget.sqlite"))
	req := roleplayGenerateReq{Mode: "advance", Line: roleplayLine{Chapters: []roleplayChapter{{Title: "第一章"}}}}
	_, maxTokens, err := roleplayPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if maxTokens != roleplayAdvanceMaxTokens || maxTokens >= 1800 {
		t.Fatalf("advance max tokens = %d, want reduced budget %d", maxTokens, roleplayAdvanceMaxTokens)
	}
}

func TestRoleplayModelRequestUsesNonStreamingCompletion(t *testing.T) {
	originalClient := httpClient
	t.Cleanup(func() { httpClient = originalClient })

	var requestBody struct {
		Stream bool `json:"stream"`
	}
	httpClient = &http.Client{Transport: roleplayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"生成正文"},"finish_reason":"stop"}]}`)),
		}, nil
	})}

	result, err := callRoleplayModelRequest("key", "https://api.openai.com/v1", "model", []ChatMessage{{Role: "user", Content: "继续"}}, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Stream {
		t.Fatal("roleplay request unexpectedly enabled provider streaming")
	}
	if result.Content != "生成正文" || result.FinishReason != "stop" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestMinimallyCleanRoleplayEndingKeepsTextThroughComma(t *testing.T) {
	got, ok := minimallyCleanRoleplayEnding("屋外的雨声渐渐低了下来，他停在门边，像是终于下定了什么决心，伸手想要")
	if !ok || got != "屋外的雨声渐渐低了下来，他停在门边，像是终于下定了什么决心。" {
		t.Fatalf("cleaned text = %q, ok=%v", got, ok)
	}
}

func TestMinimallyCleanRoleplayEndingIgnoresCommaInsideDialogue(t *testing.T) {
	text := "他低声说：“等等，我还没有想好"
	if got, ok := minimallyCleanRoleplayEnding(text); ok || got != text {
		t.Fatalf("dialogue comma should not be a cleanup point: %q, ok=%v", got, ok)
	}
}

func TestMinimallyCleanRoleplayEndingRefusesLargeDeletion(t *testing.T) {
	text := "他点头，" + strings.Repeat("仍在继续的残缺内容", 8)
	if got, ok := minimallyCleanRoleplayEnding(text); ok || got != text {
		t.Fatalf("large deletion should require repair: %q, ok=%v", got, ok)
	}
}

func TestCompleteRoleplayEndingNeedsNoCleanup(t *testing.T) {
	if !hasCompleteRoleplayEnding("他看着她，安静地问：“你愿意吗？”") {
		t.Fatal("quoted sentence should count as a complete ending")
	}
}

func TestSampleRoleplayScenesHonorsTotalBudget(t *testing.T) {
	scenes := make([]roleplayScene, 300)
	for i := range scenes {
		scenes[i].Text = strings.Repeat("很长的场景内容", 100)
	}
	const budget = 40000
	got := sampleRoleplayScenes(scenes, budget)
	if size := utf8.RuneCountInString(got); size > budget {
		t.Fatalf("sample contains %d runes, budget is %d", size, budget)
	}
	if !strings.Contains(got, "[场景 300]") {
		t.Fatal("sample dropped the final scene despite having room for all scene labels")
	}
	if !strings.Contains(got, "[场景 1]") {
		t.Fatal("sample dropped the earliest scene instead of compressing it")
	}
}

func TestSampleRoleplayScenesPrioritizesRecentDetail(t *testing.T) {
	scenes := []roleplayScene{{Text: strings.Repeat("早", 1000)}, {Text: strings.Repeat("中", 1000)}, {Text: strings.Repeat("近", 1000)}}
	got := sampleRoleplayScenes(scenes, 1500)
	if strings.Count(got, "近") <= strings.Count(got, "早") {
		t.Fatal("recent scene did not receive more context than the earliest scene")
	}
	if !strings.Contains(got, "早") || !strings.Contains(got, "中") {
		t.Fatal("older scenes were dropped instead of compressed")
	}
}

func TestCompletedChapterContextHonorsTotalBudget(t *testing.T) {
	chapters := make([]roleplayChapter, 20)
	for i := range chapters {
		chapters[i] = roleplayChapter{Title: "章节", Summary: strings.Repeat("关键事件", 100)}
	}
	got := completedChapterContext(chapters)
	if size := utf8.RuneCountInString(got); size > roleplayCompletedBudget {
		t.Fatalf("completed context contains %d runes, budget is %d", size, roleplayCompletedBudget)
	}
	if !strings.Contains(got, "1. 章节：") || !strings.Contains(got, "19. 章节：") {
		t.Fatal("completed context dropped chapters instead of compressing every summary")
	}
	if !strings.Contains(got, "[压缩]") {
		t.Fatal("completed context did not mark compressed summaries")
	}
}

func TestParseRoleplayNodes(t *testing.T) {
	nodes, err := parseRoleplayNodes("```json\n" + `[{"title":"决定离开","summary":"两人决定一起离开，并约定在车站会合。"}]` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Title != "决定离开" {
		raw, _ := json.Marshal(nodes)
		t.Fatalf("unexpected nodes: %s", raw)
	}
}

func TestValidateRoleplayStateRejectsObjects(t *testing.T) {
	err := validateRoleplayState(roleplayState{Lines: json.RawMessage(`{}`), Templates: json.RawMessage(`[]`)})
	if err == nil {
		t.Fatal("expected invalid state error")
	}
}

func TestRoleplayStateRejectsStaleUpdatedAt(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-lock.sqlite"))
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/roleplay", strings.NewReader(body))
		res := httptest.NewRecorder()
		handleRoleplayState(res, req)
		return res
	}
	first := put(`{"lines":[],"templates":[],"updated_at":""}`)
	if first.Code != http.StatusOK {
		t.Fatalf("initial save: status=%d body=%s", first.Code, first.Body.String())
	}
	var saved map[string]interface{}
	if err := json.Unmarshal(first.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	updatedAt, _ := saved["updated_at"].(string)
	second := put(`{"lines":[],"templates":[],"updated_at":` + strconvQuote(updatedAt) + `}`)
	if second.Code != http.StatusOK {
		t.Fatalf("fresh save: status=%d body=%s", second.Code, second.Body.String())
	}
	stale := put(`{"lines":[],"templates":[],"updated_at":` + strconvQuote(updatedAt) + `}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale save: status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestRoleplayStateGETVersionCanBeSaved(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-roundtrip.sqlite"))
	initial := httptest.NewRecorder()
	handleRoleplayState(initial, httptest.NewRequest(http.MethodPut, "/api/roleplay", strings.NewReader(`{"lines":[],"templates":[],"updated_at":""}`)))
	if initial.Code != http.StatusOK {
		t.Fatalf("initial save: status=%d body=%s", initial.Code, initial.Body.String())
	}
	loaded := httptest.NewRecorder()
	handleRoleplayState(loaded, httptest.NewRequest(http.MethodGet, "/api/roleplay", nil))
	var state roleplayState
	if err := json.Unmarshal(loaded.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(state)
	saved := httptest.NewRecorder()
	handleRoleplayState(saved, httptest.NewRequest(http.MethodPut, "/api/roleplay", strings.NewReader(string(body))))
	if saved.Code != http.StatusOK {
		t.Fatalf("round-trip save: status=%d body=%s", saved.Code, saved.Body.String())
	}
}

func TestRoleplayGenerateRejectsStaleStoryVersion(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-stale-version.sqlite"))
	initial := httptest.NewRecorder()
	handleRoleplayState(initial, httptest.NewRequest(http.MethodPut, "/api/roleplay", strings.NewReader(`{"lines":[],"templates":[],"updated_at":""}`)))
	if initial.Code != http.StatusOK {
		t.Fatalf("create state: %d %s", initial.Code, initial.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(`{"request_id":"play-stale-version-123","mode":"advance","state_updated_at":"stale-version","api_key":"key","line":{"id":1,"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`))
	response := httptest.NewRecorder()
	handleRoleplayGenerate(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "其他页面更新") {
		t.Fatalf("stale generation: status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM roleplay_generation_tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale request created %d generation tasks", count)
	}
}

func TestRoleplayGenerationDeliveryCanBeClaimed(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-delivery.sqlite"))
	_, err := db.DB.Exec(`INSERT INTO roleplay_generation_deliveries(request_id,line_id,chapter_index,response_json) VALUES(?,?,?,?)`, "play-delivery-123", 42, 1, `{"text":"已生成正文"}`)
	if err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	handleRoleplayGenerations(list, httptest.NewRequest(http.MethodGet, "/api/roleplay/generations", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "已生成正文") || !strings.Contains(list.Body.String(), `"line_id":42`) {
		t.Fatalf("list: status=%d body=%s", list.Code, list.Body.String())
	}
	claimed := httptest.NewRecorder()
	handleRoleplayGenerations(claimed, httptest.NewRequest(http.MethodDelete, "/api/roleplay/generations?request_id=play-delivery-123", nil))
	if claimed.Code != http.StatusOK {
		t.Fatalf("claim: status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	after := httptest.NewRecorder()
	handleRoleplayGenerations(after, httptest.NewRequest(http.MethodGet, "/api/roleplay/generations", nil))
	if strings.Contains(after.Body.String(), "play-delivery-123") {
		t.Fatalf("claimed generation still listed: %s", after.Body.String())
	}
}

func TestRoleplayGenerateUsesClientAPIConfig(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay.sqlite"))
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	callRoleplayModel = func(key, baseURL, model string, _ []ChatMessage, _ int) (ChatResult, error) {
		if key != "client-key" || baseURL != "https://api.openai.com/v1" || model != "client-model" {
			t.Fatalf("unexpected provider config: key=%q base=%q model=%q", key, baseURL, model)
		}
		return ChatResult{Content: "生成的场景"}, nil
	}

	body := `{"mode":"advance","input":"继续","api_key":"client-key","api_base_url":"https://api.openai.com/v1","model":"client-model","line":{"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body))
	res := httptest.NewRecorder()
	handleRoleplayGenerate(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "生成的场景") {
		t.Fatalf("unexpected response: status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestRoleplayGenerateRequestIDReusesPaidResult(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-idempotent.sqlite"))
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	calls := 0
	callRoleplayModel = func(string, string, string, []ChatMessage, int) (ChatResult, error) {
		calls++
		return ChatResult{Content: "只应生成一次"}, nil
	}
	body := `{"request_id":"play-test-123456","mode":"advance","api_key":"client-key","line":{"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	for i := 0; i < 2; i++ {
		res := pollRoleplayGenerate(t, body)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "只应生成一次") {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, res.Code, res.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("model calls=%d, want 1", calls)
	}
}

func TestRoleplayGenerateRequestIDSurvivesDisconnectedClient(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-disconnect.sqlite"))
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	var calls atomic.Int32
	modelStarted := make(chan struct{})
	releaseModel := make(chan struct{})
	callRoleplayModel = func(string, string, string, []ChatMessage, int) (ChatResult, error) {
		calls.Add(1)
		close(modelStarted)
		<-releaseModel
		return ChatResult{Content: "断线后可领取"}, nil
	}
	body := `{"request_id":"play-disconnect-123","mode":"advance","api_key":"client-key","line":{"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		req := httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body)).WithContext(ctx)
		handleRoleplayGenerate(httptest.NewRecorder(), req)
	}()
	<-modelStarted
	cancel()
	<-firstDone
	close(releaseModel)

	deadline := time.Now().Add(time.Second)
	for {
		roleplayGenerateTasks.Lock()
		task := roleplayGenerateTasks.items["play-disconnect-123"]
		roleplayGenerateTasks.Unlock()
		if task != nil {
			select {
			case <-task.Done:
				goto completed
			default:
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("generation task did not finish")
		}
		time.Sleep(time.Millisecond)
	}

completed:
	res := httptest.NewRecorder()
	handleRoleplayGenerate(res, httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body)))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "断线后可领取") {
		t.Fatalf("retry: status=%d body=%s", res.Code, res.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls=%d, want 1", calls.Load())
	}
}

func TestRoleplayGenerateRequestIDSurvivesMemoryCacheReset(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-persisted.sqlite"))
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	calls := 0
	callRoleplayModel = func(string, string, string, []ChatMessage, int) (ChatResult, error) {
		calls++
		return ChatResult{Content: "重启后仍可领取"}, nil
	}
	body := `{"request_id":"play-persisted-123","mode":"advance","api_key":"client-key","line":{"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	first := pollRoleplayGenerate(t, body)
	if first.Code != http.StatusOK {
		t.Fatalf("initial generation: status=%d body=%s", first.Code, first.Body.String())
	}
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()

	second := pollRoleplayGenerate(t, body)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "重启后仍可领取") {
		t.Fatalf("persisted retry: status=%d body=%s", second.Code, second.Body.String())
	}
	if calls != 1 {
		t.Fatalf("model calls=%d, want 1", calls)
	}
}

func TestRoleplayRunningTaskIsNotExpired(t *testing.T) {
	t.Setenv("ROLEPLAY_GENERATE_TASK_TIMEOUT_MINUTES", "1")
	old := time.Now().Add(-2 * time.Minute)
	fingerprint := sha256.Sum256([]byte("running"))
	running := &roleplayGenerateTask{RequestID: "play-running-old", CreatedAt: old, LastActive: old, Status: "running", Fingerprint: fingerprint, Done: make(chan struct{})}
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = map[string]*roleplayGenerateTask{"play-running-old": running}
	roleplayGenerateTasks.Unlock()
	t.Cleanup(func() {
		roleplayGenerateTasks.Lock()
		roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
		roleplayGenerateTasks.Unlock()
	})
	db.Init(filepath.Join(t.TempDir(), "roleplay-running-ttl.sqlite"))
	_, _, _ = getOrCreateRoleplayGenerateTask("play-another-task", sha256.Sum256([]byte("another")), 1, 0, "advance", "test-version")
	roleplayGenerateTasks.Lock()
	defer roleplayGenerateTasks.Unlock()
	if roleplayGenerateTasks.items["play-running-old"] == nil {
		t.Fatal("running task was expired")
	}
}

func TestRoleplayGenerationEndToEnd(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-e2e.sqlite"))
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	var calls atomic.Int32
	callRoleplayModel = func(string, string, string, []ChatMessage, int) (ChatResult, error) {
		calls.Add(1)
		return ChatResult{Content: "端到端生成正文"}, nil
	}
	state := httptest.NewRecorder()
	handleRoleplayState(state, httptest.NewRequest(http.MethodPut, "/api/roleplay", strings.NewReader(`{"lines":[],"templates":[],"updated_at":""}`)))
	if state.Code != http.StatusOK {
		t.Fatalf("create state: %d %s", state.Code, state.Body.String())
	}
	body := `{"request_id":"play-e2e-123456","mode":"advance","input":"继续","api_key":"key","line":{"id":99,"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	created := httptest.NewRecorder()
	handleRoleplayGenerate(created, httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body)))
	if created.Code != http.StatusAccepted {
		t.Fatalf("create generation: %d %s", created.Code, created.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		status := httptest.NewRecorder()
		handleRoleplayGenerationRoute(status, httptest.NewRequest(http.MethodGet, "/api/roleplay/generations/play-e2e-123456/status", nil))
		if strings.Contains(status.Body.String(), `"status":"done"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation status: %s", status.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
	result := httptest.NewRecorder()
	handleRoleplayGenerationRoute(result, httptest.NewRequest(http.MethodGet, "/api/roleplay/generations/play-e2e-123456", nil))
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "端到端生成正文") {
		t.Fatalf("result: %d %s", result.Code, result.Body.String())
	}
	duplicate := pollRoleplayGenerate(t, body)
	if duplicate.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("duplicate: status=%d calls=%d", duplicate.Code, calls.Load())
	}
}

func TestRoleplayRestartDoesNotRecallRunningTask(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-restart-running.sqlite"))
	fingerprint := sha256.Sum256([]byte("same"))
	_, err := db.DB.Exec(`INSERT INTO roleplay_generation_tasks(request_id,fingerprint,line_id,chapter_index,mode,status) VALUES(?,?,?,?,?,'running')`, "play-restart-running", fingerprint[:], 1, 0, "advance")
	if err != nil {
		t.Fatal(err)
	}
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	task, created, err := getOrCreateRoleplayGenerateTask("play-restart-running", fingerprint, 1, 0, "advance", "")
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	<-task.Done
	if task.Result.Code != http.StatusServiceUnavailable || !strings.Contains(task.Result.Err.Error(), "未再次调用模型") {
		t.Fatalf("result=%+v", task.Result)
	}
}

func TestRestoreRoleplayGenerateTasksFailsInterruptedWithoutRecall(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-startup-restore.sqlite"))
	fingerprint := sha256.Sum256([]byte("startup-restore"))
	_, err := db.DB.Exec(`INSERT INTO roleplay_generation_tasks(request_id,fingerprint,line_id,chapter_index,mode,status) VALUES(?,?,?,?,?,'running')`, "play-startup-restore", fingerprint[:], 7, 2, "advance")
	if err != nil {
		t.Fatal(err)
	}
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	if err := RestoreRoleplayGenerateTasks(); err != nil {
		t.Fatal(err)
	}
	roleplayGenerateTasks.Lock()
	task := roleplayGenerateTasks.items["play-startup-restore"]
	roleplayGenerateTasks.Unlock()
	if task == nil || task.Status != "failed" || task.Result.Code != http.StatusServiceUnavailable {
		t.Fatalf("restored task=%+v", task)
	}
	var status, errorText string
	if err := db.DB.QueryRow(`SELECT status,error_text FROM roleplay_generation_tasks WHERE request_id=?`, "play-startup-restore").Scan(&status, &errorText); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(errorText, "未再次调用模型") {
		t.Fatalf("status=%q error=%q", status, errorText)
	}
}

func TestRoleplayTimeoutBecomesTerminalRetryableFailure(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-timeout.sqlite"))
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	callRoleplayModel = func(string, string, string, []ChatMessage, int) (ChatResult, error) {
		return ChatResult{}, context.DeadlineExceeded
	}
	body := `{"request_id":"play-timeout-123456","mode":"advance","api_key":"key","line":{"id":1,"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	result := pollRoleplayGenerate(t, body)
	if result.Code != http.StatusGatewayTimeout || !strings.Contains(result.Body.String(), "生成超时，请重试") {
		t.Fatalf("result: status=%d body=%s", result.Code, result.Body.String())
	}
	status := httptest.NewRecorder()
	handleRoleplayGenerationRoute(status, httptest.NewRequest(http.MethodGet, "/api/roleplay/generations/play-timeout-123456/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"failed"`) || !strings.Contains(status.Body.String(), "生成超时，请重试") {
		t.Fatalf("status: code=%d body=%s", status.Code, status.Body.String())
	}
}

func TestRoleplayFrontendPreservesFailedDraftAndAvoidsDuplicateSystemPrompt(t *testing.T) {
	source, err := os.ReadFile("../../static/roleplay/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(source)
	for _, required := range []string{"id=\"endChapterButton\"", "button.textContent='结束中…'", "let pending;try{await save()", "else alert(err.message||'结束本章失败')"} {
		if !strings.Contains(page, required) {
			t.Fatalf("end chapter recovery missing %q", required)
		}
	}
	for _, required := range []string{"activePendingGenerations", "markGenerationFailed", "输入草稿已保留", "retryGeneration"} {
		if !strings.Contains(page, required) {
			t.Fatalf("frontend missing %q", required)
		}
	}
	for _, required := range []string{"state_updated_at:stateUpdatedAt", "故事已更新，不能把旧上下文的生成结果写入当前剧情", "当前 Play 线的人设、既有剧情事实和专属规则优先"} {
		if !strings.Contains(page, required) {
			t.Fatalf("frontend missing version or priority guard %q", required)
		}
	}
	for _, required := range []string{"let lines=[],templates=[]", "initializeRoleplay()", "roleplayUpdatedAt", "GENERATION_TTL=30*60*1000", "deliveryErrors"} {
		if !strings.Contains(page, required) {
			t.Fatalf("frontend recovery protocol missing %q", required)
		}
	}
	if strings.Contains(page, "for(let attempt=0;attempt<2;attempt++)") || strings.Contains(page, "setTimeout(queueRoleplaySync") {
		t.Fatal("frontend still retries stale snapshots or syncs before server initialization")
	}
	recovery, err := os.ReadFile("../../static/roleplay-local-recovery.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recovery), "method:'PUT'") {
		t.Fatal("local recovery script can still overwrite authoritative server state")
	}
	start := strings.Index(page, "function serverRoleplayLine")
	end := strings.Index(page, "function normalizeStoryText")
	if start < 0 || end <= start {
		t.Fatal("serverRoleplayLine source not found")
	}
	if strings.Contains(page[start:end], "system_prompt") {
		t.Fatal("frontend still injects system_prompt")
	}

	const systemPrompt = "你是故事中的角色。保持人物设定、关系边界、既有事实和叙事语气一致，只输出故事正文。"
	req := roleplayGenerateReq{Mode: "advance", Line: roleplayLine{Character: "TA", Chapters: []roleplayChapter{{Title: "第一章"}}}}
	currentPrompt, _, err := roleplayPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(currentPrompt, systemPrompt) || strings.Contains(currentPrompt, "【主设置的基础 Prompt】") {
		t.Fatal("roleplay prompt still includes the main chat system prompt")
	}
}

func TestRoleplayPersistenceFailureReturns500(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-write-failure.sqlite"))
	if err := db.DB.Close(); err != nil {
		t.Fatal(err)
	}
	roleplayGenerateTasks.Lock()
	roleplayGenerateTasks.items = make(map[string]*roleplayGenerateTask)
	roleplayGenerateTasks.Unlock()
	body := `{"request_id":"play-write-failure","mode":"advance","api_key":"key","line":{"id":1,"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	res := httptest.NewRecorder()
	handleRoleplayGenerate(res, httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body)))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func pollRoleplayGenerate(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		res := httptest.NewRecorder()
		handleRoleplayGenerate(res, httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body)))
		if res.Code != http.StatusAccepted {
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation remained pending: body=%s", res.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRoleplayGenerateTurnsModelRefusalIntoHelpfulError(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-refusal.sqlite"))
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	callRoleplayModel = func(string, string, string, []ChatMessage, int) (ChatResult, error) {
		return ChatResult{Content: "I can't continue this content."}, nil
	}

	body := `{"mode":"advance","api_key":"client-key","api_base_url":"https://api.openai.com/v1","line":{"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body))
	res := httptest.NewRecorder()
	handleRoleplayGenerate(res, req)
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "所选模型没有继续") {
		t.Fatalf("unexpected response: status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestRoleplayServerKeyRequiresAuthenticatedContext(t *testing.T) {
	db.Init(filepath.Join(t.TempDir(), "roleplay-server-key.sqlite"))
	t.Setenv("ALLOW_SERVER_API_KEY", "true")
	t.Setenv("OPENROUTER_API_KEY", "server-key")
	t.Setenv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")
	original := callRoleplayModel
	t.Cleanup(func() { callRoleplayModel = original })
	called := false
	callRoleplayModel = func(key, _, _ string, _ []ChatMessage, _ int) (ChatResult, error) {
		called = true
		if key != "server-key" {
			t.Fatalf("key = %q, want server key", key)
		}
		return ChatResult{Content: "生成的场景"}, nil
	}
	body := `{"mode":"advance","line":{"character":"TA","chapters":[{"title":"第一章","scenes":[],"nodes":[]}]}}`
	unauthenticated := httptest.NewRecorder()
	handleRoleplayGenerate(unauthenticated, httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body)))
	if unauthenticated.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("unauthenticated status=%d called=%v", unauthenticated.Code, called)
	}
	authenticatedReq := httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(body))
	authenticatedReq = authenticatedReq.WithContext(context.WithValue(authenticatedReq.Context(), apiAuthContextKey{}, true))
	authenticated := httptest.NewRecorder()
	handleRoleplayGenerate(authenticated, authenticatedReq)
	if authenticated.Code != http.StatusOK || !called {
		t.Fatalf("authenticated status=%d body=%s called=%v", authenticated.Code, authenticated.Body.String(), called)
	}
}

func TestRoleplayGenerateRateLimit(t *testing.T) {
	t.Setenv("ROLEPLAY_GENERATE_PER_MINUTE", "1")
	roleplayGenerateRates.Lock()
	roleplayGenerateRates.clients = make(map[string]roleplayRateWindow)
	roleplayGenerateRates.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/api/roleplay/generate", strings.NewReader(`{}`))
	if !roleplayGenerateAllowed(req) || roleplayGenerateAllowed(req) {
		t.Fatal("expected the second request in one minute to be rejected")
	}
}

func TestRoleplayModelRefusalDetection(t *testing.T) {
	if !isRoleplayModelRefusal("The narrative is structured as sexually explicit material.") {
		t.Fatal("expected refusal to be detected")
	}
	if isRoleplayModelRefusal("两人把争执留到明天再谈。") {
		t.Fatal("ordinary story text must not be treated as a refusal")
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
