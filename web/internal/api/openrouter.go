package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"myapp/internal/observability"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const defaultOpenRouterURL = "https://openrouter.ai/api/v1/chat/completions"

const (
	defaultProviderRPM = 60
	providerCooldown   = 30 * time.Second
)

type upstreamPriority uint8

const (
	upstreamBackground upstreamPriority = iota
	upstreamTool
	upstreamChat
)

type providerRateLimitError struct {
	message    string
	retryAfter time.Duration
}

func (e *providerRateLimitError) Error() string {
	return e.message
}

type chatEndpoint struct {
	URL          string
	IsOpenRouter bool
}

type ChatMessage struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL string `json:"url"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ChatResult struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        ModelUsage
}

type chatRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	Messages      []ChatMessage   `json:"messages"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    interface{}     `json:"tool_choice,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	StreamOptions map[string]bool `json:"stream_options,omitempty"`
}

type chatResponse struct {
	Usage   json.RawMessage `json:"usage,omitempty"`
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var httpClient = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: validateProviderRedirect}

var streamHTTPClient = newStreamHTTPClient()

var upstreamRequests = newUpstreamRequestLimiter()

func newStreamHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Limit the wait for the provider to begin responding, but do not impose a
	// total deadline on an active stream. The browser and chat handler monitor
	// stream activity separately.
	transport.ResponseHeaderTimeout = 3 * time.Minute
	return &http.Client{Transport: transport, CheckRedirect: validateProviderRedirect, Timeout: 5 * time.Minute}
}

func Call(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int) (string, error) {
	result, err := callWithToolsChoicePriority(apiKey, apiBaseURL, model, messages, maxTokens, nil, nil, nil, upstreamBackground)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func CallWithTools(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool) (ChatResult, error) {
	return CallWithToolsChoice(apiKey, apiBaseURL, model, messages, maxTokens, tools, nil)
}

func CallWithToolsChoice(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool, toolChoice interface{}) (ChatResult, error) {
	return callWithToolsChoicePriority(apiKey, apiBaseURL, model, messages, maxTokens, tools, toolChoice, nil, upstreamBackground)
}

func CallWithToolsChoiceChat(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool, toolChoice interface{}) (ChatResult, error) {
	return callWithToolsChoicePriority(apiKey, apiBaseURL, model, messages, maxTokens, tools, toolChoice, nil, upstreamChat)
}

// CallWithToolsChoiceStream consumes the provider's SSE response as it arrives.
// onText is called synchronously for every content delta before the next SSE
// event is read, which lets the HTTP handler preserve upstream backpressure.
func CallWithToolsChoiceStream(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool, toolChoice interface{}, onText func(string) error) (ChatResult, error) {
	return callWithToolsChoicePriority(apiKey, apiBaseURL, model, messages, maxTokens, tools, toolChoice, onText, upstreamChat)
}

func CallWithToolsChoiceStreamTool(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool, toolChoice interface{}, onText func(string) error) (ChatResult, error) {
	return callWithToolsChoicePriority(apiKey, apiBaseURL, model, messages, maxTokens, tools, toolChoice, onText, upstreamTool)
}

func callWithToolsChoicePriority(apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool, toolChoice interface{}, onText func(string) error, priority upstreamPriority) (ChatResult, error) {
	return callWithToolsChoicePriorityContext(context.Background(), apiKey, apiBaseURL, model, messages, maxTokens, tools, toolChoice, onText, priority)
}

func callWithToolsChoicePriorityContext(ctx context.Context, apiKey, apiBaseURL, model string, messages []ChatMessage, maxTokens int, tools []Tool, toolChoice interface{}, onText func(string) error, priority upstreamPriority) (returned ChatResult, callErr error) {
	start := time.Now()
	attempted := false
	defer func() {
		if attempted {
			recordModelStats(ctx, model, start, returned.Usage, callErr != nil)
		}
	}()
	messages = fitFinalModelInput(ctx, messages, tools, priority)
	if err := checkModelInputBudget(ctx, messages, tools); err != nil {
		return ChatResult{}, err
	}
	payload := chatRequest{
		Model:      model,
		MaxTokens:  maxTokens,
		Messages:   messages,
		Tools:      tools,
		ToolChoice: toolChoice,
		Stream:     onText != nil,
	}
	if payload.Stream {
		payload.StreamOptions = map[string]bool{"include_usage": true}
	}
	body, _ := json.Marshal(payload)

	endpoint, err := openRouterEndpoint(apiBaseURL)
	if err != nil {
		return ChatResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return ChatResult{}, fmt.Errorf("创建请求失败: %w", err)
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	req.Header.Set("Content-Type", "application/json")
	if endpoint.IsOpenRouter {
		req.Header.Set("HTTP-Referer", "http://localhost")
		req.Header.Set("X-Title", "MyAssistant")
	}
	limiterKey := endpoint.URL + "\x00" + strings.TrimSpace(apiKey)
	if err := upstreamRequests.Acquire(limiterKey, priority); err != nil {
		return ChatResult{}, err
	}

	client := httpClient
	if onText != nil {
		client = streamHTTPClient
	}
	attempted = true
	resp, err := client.Do(req)
	if err != nil {
		return ChatResult{}, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	upstreamRequests.ObserveLimit(limiterKey, providerRPMFromHeaders(resp.Header))
	if onText != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		body := newIdleReadCloser(resp.Body, 120*time.Second)
		defer body.Close()
		return readChatStream(body, onText)
	}

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests {
		upstreamRequests.Record429(limiterKey)
		retryAfter := time.Duration(0)
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); parseErr == nil && seconds > 0 {
			retryAfter = time.Duration(seconds) * time.Second
		} else if when, parseErr := http.ParseTime(resp.Header.Get("Retry-After")); parseErr == nil {
			retryAfter = time.Until(when)
		}
		return ChatResult{}, &providerRateLimitError{message: fmt.Sprintf("API错误: HTTP 429，原始内容: %s", string(raw)), retryAfter: retryAfter}
	}
	var result chatResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return ChatResult{}, fmt.Errorf("解析响应失败: %w，HTTP状态: %d，原始内容: %s", err, resp.StatusCode, string(raw))
	}
	if result.Error != nil {
		return ChatResult{}, fmt.Errorf("API错误: %s", result.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResult{}, fmt.Errorf("API错误: HTTP %d，原始内容: %s", resp.StatusCode, string(raw))
	}
	if len(result.Choices) == 0 {
		return ChatResult{}, fmt.Errorf("API返回空响应，原始内容: %s", string(raw))
	}
	msg := result.Choices[0].Message
	return ChatResult{Content: stripInternalReasoning(msg.Content), ToolCalls: msg.ToolCalls, FinishReason: result.Choices[0].FinishReason, Usage: parseModelUsage(result.Usage)}, nil
}

type idleReadCloser struct {
	io.ReadCloser
	mu       sync.Mutex
	timer    *time.Timer
	timeout  time.Duration
	timedOut bool
}

var errStreamIdleTimeout = errors.New("流式响应连续 120 秒没有新数据")

func newIdleReadCloser(body io.ReadCloser, timeout time.Duration) *idleReadCloser {
	r := &idleReadCloser{ReadCloser: body, timeout: timeout}
	r.timer = time.AfterFunc(timeout, func() {
		r.mu.Lock()
		r.timedOut = true
		r.mu.Unlock()
		_ = body.Close()
	})
	return r
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.mu.Lock()
		r.timer.Reset(r.timeout)
		r.mu.Unlock()
	}
	if err != nil {
		r.mu.Lock()
		timedOut := r.timedOut
		r.mu.Unlock()
		if timedOut {
			return n, errStreamIdleTimeout
		}
	}
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.mu.Lock()
	r.timer.Stop()
	r.mu.Unlock()
	return r.ReadCloser.Close()
}

type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type streamResponse struct {
	Usage   json.RawMessage `json:"usage,omitempty"`
	Choices []struct {
		Delta struct {
			Content   string           `json:"content"`
			ToolCalls []streamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func readChatStream(r io.Reader, onText func(string) error) (ChatResult, error) {
	scanner := bufio.NewScanner(r)
	// Tool arguments and long content can exceed Scanner's small default token.
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	var result ChatResult
	// Buffer content until the stream ends. A provider may emit explanatory
	// text before tool_calls; forwarding it immediately leaks the tool
	// orchestration into the user's chat. Only publish content when this is a
	// normal assistant response with no tool calls.
	var bufferedContent strings.Builder
	toolCalls := map[int]*ToolCall{}
	maxToolIndex := -1
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var event streamResponse
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return result, fmt.Errorf("解析流响应失败: %w", err)
		}
		if usage := parseModelUsage(event.Usage); usage.Reported {
			result.Usage = usage
		}
		if event.Error != nil {
			return result, fmt.Errorf("API错误: %s", event.Error.Message)
		}
		for _, choice := range event.Choices {
			if choice.FinishReason != "" {
				result.FinishReason = choice.FinishReason
			}
			if choice.Delta.Content != "" {
				result.Content += choice.Delta.Content
				bufferedContent.WriteString(choice.Delta.Content)
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := toolCalls[delta.Index]
				if call == nil {
					call = &ToolCall{}
					toolCalls[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Type != "" {
					call.Type = delta.Type
				}
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
				if delta.Index > maxToolIndex {
					maxToolIndex = delta.Index
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("读取流响应失败: %w", err)
	}
	for i := 0; i <= maxToolIndex; i++ {
		if call := toolCalls[i]; call != nil {
			result.ToolCalls = append(result.ToolCalls, *call)
		}
	}
	result.Content = stripInternalReasoning(result.Content)
	if len(result.ToolCalls) == 0 && bufferedContent.Len() > 0 {
		if err := onText(result.Content); err != nil {
			return result, err
		}
	}
	if strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return result, fmt.Errorf("API返回空的流式响应")
	}
	return result, nil
}

var internalReasoningBlockPattern = regexp.MustCompile(`(?is)<\s*(?:reasoning|thinking|analysis)(?:\s[^>]*)?>.*?<\s*/\s*(?:reasoning|thinking|analysis)\s*>`)
var unclosedInternalReasoningPattern = regexp.MustCompile(`(?is)<\s*(?:reasoning|thinking|analysis)(?:\s[^>]*)?>.*$`)

func stripInternalReasoning(content string) string {
	original := content
	content = internalReasoningBlockPattern.ReplaceAllString(content, "")
	content = unclosedInternalReasoningPattern.ReplaceAllString(content, "")
	if recovered, ok := recoverFinalRoleReplyJSON(content); ok {
		return recovered
	}
	if roleTranscriptDraft(content) {
		if recovered, ok := recoverMalformedRoleReplyJSON(content); ok {
			observability.Event("model.reply_recovered", map[string]interface{}{"reason": "malformed_terminal_role_payload", "raw_chars": utf8.RuneCountInString(original), "recovered_chars": utf8.RuneCountInString(recovered)})
			return recovered
		}
		observability.Event("model.reply_filtered", map[string]interface{}{"reason": "invalid_role_transcript", "raw_chars": utf8.RuneCountInString(original), "after_block_chars": utf8.RuneCountInString(content)})
		return ""
	}
	visible := strings.TrimSpace(content)
	if visible == "" {
		reason := "provider_empty_content"
		if strings.TrimSpace(original) != "" {
			reason = "internal_reasoning_only"
		}
		observability.Event("model.reply_filtered", map[string]interface{}{"reason": reason, "raw_chars": utf8.RuneCountInString(original), "visible_chars": 0})
	}
	return visible
}

func openRouterEndpoint(apiBaseURL string) (chatEndpoint, error) {
	raw := strings.TrimSpace(apiBaseURL)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
	}
	if raw == "" {
		return chatEndpoint{URL: defaultOpenRouterURL, IsOpenRouter: true}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return chatEndpoint{}, fmt.Errorf("API Base URL 无效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return chatEndpoint{}, fmt.Errorf("API Base URL 只支持 http 或 https")
	}
	if u.Scheme != "https" {
		return chatEndpoint{}, fmt.Errorf("API Base URL 必须使用 HTTPS")
	}
	if !allowedAIProviderHost(u.Hostname()) {
		return chatEndpoint{}, fmt.Errorf("AI 服务商主机 %q 不是可用的公网主机", u.Hostname())
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path += "/chat/completions"
	}
	return chatEndpoint{URL: u.String(), IsOpenRouter: strings.EqualFold(u.Host, "openrouter.ai")}, nil
}

func allowedAIProviderHost(raw string) bool {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPublicProviderIP(ip)
	}
	if strings.ContainsAny(host, " /\\@:#?") || !strings.Contains(host, ".") {
		return false
	}
	return true
}

func isPublicProviderIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func validateProviderRedirect(req *http.Request, _ []*http.Request) error {
	if req.URL.Scheme != "https" || !allowedAIProviderHost(req.URL.Hostname()) {
		return fmt.Errorf("AI 服务商重定向目标不是可用的公网 HTTPS 主机")
	}
	return nil
}

func hasCustomAPIBaseURL(apiBaseURL string) bool {
	return strings.TrimSpace(apiBaseURL) != "" || strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")) != ""
}
