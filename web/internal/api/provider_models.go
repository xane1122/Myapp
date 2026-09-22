package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type providerModelsRequest struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url"`
}

type providerModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var providerModelsHTTPClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: validateProviderRedirect,
}

func writeProviderModelsJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func providerModelsEndpoint(apiBaseURL string) (string, error) {
	raw := strings.TrimSpace(apiBaseURL)
	if raw == "" {
		raw = "https://openrouter.ai/api/v1"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || !allowedAIProviderHost(u.Hostname()) {
		return "", fmt.Errorf("API Base URL 必须是公网 HTTPS 地址")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func handleProviderModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input providerModelsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil {
		writeProviderModelsJSON(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
		return
	}
	if strings.TrimSpace(input.APIKey) == "" {
		writeProviderModelsJSON(w, http.StatusBadRequest, map[string]string{"error": "请先填写 API Key"})
		return
	}
	endpoint, err := providerModelsEndpoint(input.APIBaseURL)
	if err != nil {
		writeProviderModelsJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeProviderModelsJSON(w, http.StatusBadRequest, map[string]string{"error": "模型列表地址无效"})
		return
	}
	key := strings.TrimSpace(input.APIKey)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("x-api-key", key)
	req.Header.Set("Accept", "application/json")
	resp, err := providerModelsHTTPClient.Do(req)
	if err != nil {
		writeProviderModelsJSON(w, http.StatusBadGateway, map[string]string{"error": "拉取模型失败：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		writeProviderModelsJSON(w, http.StatusBadGateway, map[string]string{"error": "读取模型列表失败"})
		return
	}
	var result providerModelsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		writeProviderModelsJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("服务商返回了无法识别的响应（%d）", resp.StatusCode)})
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("服务商返回错误（%d）", resp.StatusCode)
		if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
			message = result.Error.Message
		}
		writeProviderModelsJSON(w, http.StatusBadGateway, map[string]string{"error": message})
		return
	}
	models := make([]string, 0, len(result.Data))
	seen := make(map[string]bool, len(result.Data))
	for _, item := range result.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		writeProviderModelsJSON(w, http.StatusBadGateway, map[string]string{"error": "服务商没有返回可用模型"})
		return
	}
	writeProviderModelsJSON(w, http.StatusOK, map[string]interface{}{"models": models})
}
