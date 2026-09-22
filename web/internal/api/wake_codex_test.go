package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func withWakeCodexServer(t *testing.T, tasks []bridgeTask, onCreate func(map[string]string)) {
	t.Helper()
	oldClient := wakeCodexHTTPClient
	wakeCodexHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header)}
		if r.Method == http.MethodGet {
			body, _ := json.Marshal(map[string]interface{}{"tasks": tasks})
			response.Body = io.NopCloser(strings.NewReader(string(body)))
			return response, nil
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if onCreate != nil {
			onCreate(payload)
		}
		body, _ := json.Marshal(map[string]interface{}{"id": 42, "status": "queued"})
		response.StatusCode = http.StatusAccepted
		response.Status = "202 Accepted"
		response.Body = io.NopCloser(strings.NewReader(string(body)))
		return response, nil
	})}
	t.Cleanup(func() { wakeCodexHTTPClient = oldClient })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestEnqueueWakeCodexTaskRejectsActiveTask(t *testing.T) {
	withWakeCodexServer(t, []bridgeTask{{ID: 7, Status: "running"}}, nil)
	_, err := enqueueWakeCodexTask(&wakeCodexTask{Needed: true, Title: "完成页面", Prompt: "完成已经明确设计但尚未实现的页面，并运行相关测试。"})
	if err == nil || !strings.Contains(err.Error(), "已有活动任务 #7") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnqueueWakeCodexTaskRejectsRecentAutonomousTask(t *testing.T) {
	withWakeCodexServer(t, []bridgeTask{{ID: 8, Sender: "autonomous-wake", Status: "succeeded", CreatedAt: time.Now().UTC().Format(time.RFC3339), Prompt: "different task"}}, nil)
	_, err := enqueueWakeCodexTask(&wakeCodexTask{Needed: true, Title: "完成页面", Prompt: "完成已经明确设计但尚未实现的页面，并运行相关测试。"})
	if err == nil || !strings.Contains(err.Error(), "十分钟冷却期") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnqueueWakeCodexTaskRejectsPreviouslyWrappedDuplicate(t *testing.T) {
	prompt := "完成已经明确设计但尚未实现的页面，并运行相关测试。"
	withWakeCodexServer(t, []bridgeTask{{ID: 9, Sender: "autonomous-wake", Status: "succeeded", CreatedAt: time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339), Prompt: "目标：完成页面\n\n" + prompt + "\n\n强制边界：不得部署。"}}, nil)
	_, err := enqueueWakeCodexTask(&wakeCodexTask{Needed: true, Title: "完成页面", Prompt: prompt})
	if err == nil || !strings.Contains(err.Error(), "与近期任务 #9 重复") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnqueueWakeCodexTaskAddsSafetyConstraints(t *testing.T) {
	var created map[string]string
	withWakeCodexServer(t, nil, func(payload map[string]string) { created = payload })
	id, err := enqueueWakeCodexTask(&wakeCodexTask{Needed: true, Title: "完成页面", Prompt: "完成已经明确设计但尚未实现的页面，并运行相关测试。"})
	if err != nil || id != 42 {
		t.Fatalf("id=%d error=%v", id, err)
	}
	if created["sender"] != "autonomous-wake" || !strings.Contains(created["prompt"], "不得部署") || !strings.Contains(created["prompt"], "API Base URL") {
		t.Fatalf("unsafe task payload: %#v", created)
	}
}

func TestLoadWakeCodexContextIncludesRecentResult(t *testing.T) {
	withWakeCodexServer(t, []bridgeTask{{ID: 9, Status: "failed", Prompt: "implement feature", Result: "test failed"}}, nil)
	context, err := loadWakeCodexContext()
	if err != nil || !strings.Contains(context, "#9 [failed]") || !strings.Contains(context, "test failed") {
		t.Fatalf("context=%q error=%v", context, err)
	}
}
