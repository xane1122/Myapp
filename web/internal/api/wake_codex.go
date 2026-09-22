package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultCodexBridgeURL = "http://127.0.0.1:5001"
	wakeCodexCooldown     = 10 * time.Minute
	wakeCodexDecisionRule = `【Codex 决策规则】
结合最近对话与下方 Codex 工程状态，识别是否有证据明确的未完成功能。没有就令 codex_task.needed=false；有且值得继续时，提供 title、可独立执行并含验收标准的 prompt、reason。不得提出部署、服务器运维、密钥、认证或 API Base URL 任务。最终 JSON 必须包含 codex_task。`
)

var wakeCodexHTTPClient = &http.Client{Timeout: 5 * time.Second}

type wakeCodexTask struct {
	Needed bool   `json:"needed"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	Reason string `json:"reason"`
}

type bridgeTask struct {
	ID        int64  `json:"id"`
	Sender    string `json:"sender"`
	Prompt    string `json:"prompt"`
	Result    string `json:"result"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Status    string `json:"status"`
}

func codexBridgeURL() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("CODEX_BRIDGE_URL")), "/"); value != "" {
		return value
	}
	return defaultCodexBridgeURL
}

func fetchBridgeTasks(limit int) ([]bridgeTask, error) {
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/codex-bridge/tasks?limit=%d", codexBridgeURL(), limit), nil)
	if err != nil {
		return nil, err
	}
	response, err := wakeCodexHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bridge 返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Tasks []bridgeTask `json:"tasks"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Tasks, nil
}

func loadWakeCodexContext() (string, error) {
	tasks, err := fetchBridgeTasks(10)
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "没有历史任务。", nil
	}
	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		result := strings.TrimSpace(task.Result)
		if len([]rune(result)) > 1200 {
			chars := []rune(result)
			result = string(chars[len(chars)-1200:])
		}
		lines = append(lines, fmt.Sprintf("#%d [%s] %s\n结果摘要：%s", task.ID, task.Status, truncateRunes(task.Prompt, 400), firstNonEmpty(result, "暂无结果")))
	}
	return strings.Join(lines, "\n\n"), nil
}

func enqueueWakeCodexTask(candidate *wakeCodexTask) (int64, error) {
	if candidate == nil || !candidate.Needed {
		return 0, nil
	}
	candidate.Title = strings.TrimSpace(candidate.Title)
	candidate.Prompt = strings.TrimSpace(candidate.Prompt)
	if candidate.Title == "" || len([]rune(candidate.Prompt)) < 20 {
		return 0, errors.New("任务目标不完整")
	}
	if len([]rune(candidate.Prompt)) > 6000 {
		return 0, errors.New("任务指令过长")
	}
	tasks, err := fetchBridgeTasks(50)
	if err != nil {
		return 0, fmt.Errorf("读取 Bridge 状态失败：%w", err)
	}
	now := time.Now().UTC()
	normalized := normalizeWakeCodexPrompt(candidate.Prompt)
	for _, task := range tasks {
		if task.Status == "queued" || task.Status == "running" {
			return 0, fmt.Errorf("已有活动任务 #%d", task.ID)
		}
		if task.Sender == "autonomous-wake" && strings.Contains(normalizeWakeCodexPrompt(task.Prompt), normalized) {
			return 0, fmt.Errorf("与近期任务 #%d 重复", task.ID)
		}
		if task.Sender == "autonomous-wake" {
			createdAt, parseErr := time.Parse(time.RFC3339, task.CreatedAt)
			if parseErr == nil && now.Sub(createdAt) < wakeCodexCooldown {
				return 0, fmt.Errorf("自主任务仍在十分钟冷却期")
			}
		}
	}
	prompt := "目标：" + candidate.Title + "\n\n" + candidate.Prompt + `

强制边界：
- 只完成上述单一目标并运行相关测试，不扩展到其他功能。
- 不得部署、重启服务或直接修改运行中二进制。
- 不得修改 API Base URL、服务商主机规则、密钥、认证或权限配置。
- 遇到工作区已有改动时保留并兼容，不得回退无关改动。`
	body, _ := json.Marshal(map[string]string{"sender": "autonomous-wake", "prompt": prompt})
	request, err := http.NewRequest(http.MethodPost, codexBridgeURL()+"/api/codex-bridge/tasks", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := wakeCodexHTTPClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	var result struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return 0, err
	}
	if response.StatusCode != http.StatusAccepted || result.ID < 1 {
		return 0, fmt.Errorf("Bridge 拒绝任务：%s", firstNonEmpty(result.Error, response.Status))
	}
	return result.ID, nil
}

func normalizeWakeCodexPrompt(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}
