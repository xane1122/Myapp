package api

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStreamHTTPClientHasFiveMinuteDeadline(t *testing.T) {
	client := newStreamHTTPClient()
	if client.Timeout != 5*time.Minute {
		t.Fatalf("stream client timeout = %v, want 5m", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("stream transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 3*time.Minute {
		t.Fatalf("response header timeout = %v, want 3m", transport.ResponseHeaderTimeout)
	}
}

func TestIdleReadCloserTimesOut(t *testing.T) {
	reader, writer := io.Pipe()
	wrapped := newIdleReadCloser(reader, 10*time.Millisecond)
	t.Cleanup(func() { _ = writer.Close(); _ = wrapped.Close() })
	start := time.Now()
	_, err := wrapped.Read(make([]byte, 1))
	if !errors.Is(err, errStreamIdleTimeout) || time.Since(start) > time.Second {
		t.Fatalf("err=%v duration=%v", err, time.Since(start))
	}
}

func TestOpenRouterEndpointAcceptsBaseOrFullURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty uses default",
			in:   "",
			want: defaultOpenRouterURL,
		},
		{
			name: "openai base v1 appends chat completions",
			in:   "https://api.openai.com/v1",
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "full endpoint stays unchanged",
			in:   "https://openrouter.ai/api/v1/chat/completions",
			want: "https://openrouter.ai/api/v1/chat/completions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openRouterEndpoint(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != tt.want {
				t.Fatalf("endpoint = %q, want %q", got.URL, tt.want)
			}
		})
	}
}

func TestOpenRouterEndpointRejectsInvalidURL(t *testing.T) {
	if _, err := openRouterEndpoint("proxy.example.com/v1"); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestOpenRouterEndpointRejectsUnsafeHosts(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:8080/v1",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.8/v1",
		"https://metadata.google.internal/computeMetadata/v1",
		"https://localhost/v1",
	} {
		if _, err := openRouterEndpoint(raw); err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}
}

func TestOpenRouterEndpointAcceptsAnyPublicProviderHost(t *testing.T) {
	got, err := openRouterEndpoint("https://gw.21912101.xyz/private-path/v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://gw.21912101.xyz/private-path/v1/chat/completions" {
		t.Fatalf("endpoint = %q", got.URL)
	}
	if _, err := openRouterEndpoint("https://api.jiushi.xin/v1"); err != nil {
		t.Fatalf("custom public provider should be allowed: %v", err)
	}
}

func TestReadChatStreamForwardsEveryContentDelta(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"你"}}]}`,
		`data: {"choices":[{"delta":{"content":"好"}}]}`,
		`data: [DONE]`,
	}, "\n")
	var deltas []string
	result, err := readChatStream(strings.NewReader(input), func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deltas, "|"); got != "你好" {
		t.Fatalf("forwarded deltas = %q", got)
	}
	if len(deltas) != 1 {
		t.Fatalf("forwarded delta count = %d, want 1", len(deltas))
	}
	if result.Content != "你好" {
		t.Fatalf("result content = %q", result.Content)
	}
}

func TestStreamCallFallsBackToNonStreamingWhenProviderRejectsStream(t *testing.T) {
	originalStreamClient, originalClient := streamHTTPClient, httpClient
	t.Cleanup(func() {
		streamHTTPClient, httpClient = originalStreamClient, originalClient
	})
	streamCalls, regularCalls := 0, 0
	streamHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		streamCalls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"error\":{\"message\":\"流式请求失败\"}}\n"))}, nil
	})}
	httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		regularCalls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"兼容回复"}}]}`))}, nil
	})}

	result, err := CallWithToolsChoiceStream("key", "https://api.openai.com/v1", "model", nil, 64, nil, nil, func(string) error { return nil })
	if err != nil || result.Content != "兼容回复" || streamCalls != 1 || regularCalls != 1 {
		t.Fatalf("result=%#v err=%v stream=%d regular=%d", result, err, streamCalls, regularCalls)
	}
}

func TestStripInternalReasoning(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "thinking block", in: "<thinking>internal draft</thinking>\n最终回答", want: "最终回答"},
		{name: "mixed case analysis", in: "前言\n<Analysis mode=\"deep\">secret</ANALYSIS>\n结论", want: "前言\n\n结论"},
		{name: "unclosed reasoning", in: "可见内容\n<reasoning>unfinished secret", want: "可见内容"},
		{name: "ordinary words", in: "I am thinking about the analysis.", want: "I am thinking about the analysis."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripInternalReasoning(tt.in); got != tt.want {
				t.Fatalf("stripInternalReasoning() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadChatStreamFiltersInternalReasoning(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"<thinking>internal "}}]}`,
		`data: {"choices":[{"delta":{"content":"draft</thinking>最终回答"}}]}`,
		`data: [DONE]`,
	}, "\n")
	var visible string
	result, err := readChatStream(strings.NewReader(input), func(delta string) error {
		visible += delta
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible != "最终回答" || result.Content != "最终回答" {
		t.Fatalf("visible=%q content=%q, want 最终回答", visible, result.Content)
	}
}

func TestReadChatStreamCapturesFinishReason(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"未完"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	}, "\n")
	result, err := readChatStream(strings.NewReader(input), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "length" {
		t.Fatalf("finish reason = %q, want length", result.FinishReason)
	}
}

func TestReadChatStreamReassemblesToolCallDeltas(t *testing.T) {
	input := strings.Join([]string{
		`: keep-alive`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_","arguments":"{\"query\":\""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"memory","arguments":"以前的约定\"}"}}]}}]}`,
		`data: [DONE]`,
	}, "\n")

	result, err := readChatStream(strings.NewReader(input), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{{ID: "call_1", Type: "function"}}
	want[0].Function.Name = "search_memory"
	want[0].Function.Arguments = `{"query":"以前的约定"}`
	if !reflect.DeepEqual(result.ToolCalls, want) {
		t.Fatalf("tool calls = %#v, want %#v", result.ToolCalls, want)
	}
}

func TestReadChatStreamDoesNotForwardContentBeforeToolCall(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"我先查看一下。"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"linux_shell","arguments":"{\"command\":\"date\"}"}}]}}]}`,
		`data: [DONE]`,
	}, "\n")
	var visible strings.Builder
	result, err := readChatStream(strings.NewReader(input), func(delta string) error {
		visible.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible.Len() != 0 {
		t.Fatalf("tool preamble leaked to client: %q", visible.String())
	}
	if result.Content != "我先查看一下。" {
		t.Fatalf("result content = %q", result.Content)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "linux_shell" {
		t.Fatalf("tool calls = %#v", result.ToolCalls)
	}
}

func TestReadChatStreamReturnsProviderError(t *testing.T) {
	input := "data: {\"error\":{\"message\":\"rate limited\"}}\n"
	_, err := readChatStream(strings.NewReader(input), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v, want provider error", err)
	}
}

func TestCallReturnsHTTP429WithoutRetry(t *testing.T) {
	originalLimiter := upstreamRequests
	upstreamRequests = newUpstreamRequestLimiter()
	t.Cleanup(func() { upstreamRequests = originalLimiter })
	attempts := 0
	originalClient := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("request frequency exceeded")),
		}, nil
	})}
	t.Cleanup(func() { httpClient = originalClient })
	result, err := CallWithToolsChoice("key", "https://api.openai.com/v1", "model", nil, 1, nil, nil)
	if err == nil || result.Content != "" || attempts != 1 {
		t.Fatalf("result=%#v err=%v attempts=%d", result, err, attempts)
	}
}

func TestReadChatStreamReturnsDeltaCallbackError(t *testing.T) {
	want := errors.New("client disconnected")
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n"
	_, err := readChatStream(strings.NewReader(input), func(string) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestReadChatStreamRejectsMalformedEvent(t *testing.T) {
	_, err := readChatStream(strings.NewReader("data: {not-json}\n"), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "解析流响应失败") {
		t.Fatalf("error = %v, want parse error", err)
	}
}

func TestReadChatStreamRejectsEmptyResponse(t *testing.T) {
	_, err := readChatStream(strings.NewReader("data: [DONE]\n"), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "空的流式响应") {
		t.Fatalf("error = %v, want empty stream error", err)
	}
}
