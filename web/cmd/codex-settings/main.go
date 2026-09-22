package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr         = "127.0.0.1:5001"
	codexSettingsHome         = "/opt/myapp/.codex"
	codexSettingsConfigPath   = codexSettingsHome + "/config.toml"
	codexSettingsKeyPath      = codexSettingsHome + "/jiushi.env"
	codexSettingsCookieName   = "codex_settings_session"
	codexSettingsCookiePath   = "/"
	codexSettingsCookieMaxAge = 86400 * 7
)

var httpClient = &http.Client{Timeout: 45 * time.Second}

var reasoningOptions = map[string]bool{
	"":        true,
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
}

type settingsState struct {
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type loginReq struct {
	Password string `json:"password"`
}

type saveReq struct {
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type saveResp struct {
	Status     string `json:"status,omitempty"`
	Warning    string `json:"warning,omitempty"`
	Validation string `json:"validation,omitempty"`
	Error      string `json:"error,omitempty"`
}

func main() {
	listenAddr := strings.TrimSpace(os.Getenv("CODEX_SETTINGS_ADDR"))
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/codex-settings", handleSettings)
	mux.HandleFunc("/api/codex-settings/login", handleLogin)
	mux.HandleFunc("/api/codex-settings/logout", handleLogout)
	mux.HandleFunc("/api/codex-settings/save", handleSave)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAuth(w, r) {
		return
	}

	state, err := loadState()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, state)
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectCrossSitePost(w, r) {
		return
	}

	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, 400, map[string]string{"error": "请求体必须是合法的 JSON。"})
		return
	}

	expected := strings.TrimSpace(os.Getenv("CODEX_SETTINGS_PASSWORD"))
	if expected == "" {
		jsonResp(w, 503, map[string]string{"error": "服务端尚未配置页面密码。"})
		return
	}

	expectedHash := hashText(expected)
	if !secureEqual(hashText(req.Password), expectedHash) {
		jsonResp(w, 401, map[string]string{"error": "密码不正确。"})
		return
	}

	setCookie(w, expectedHash, isHTTPSRequest(r))
	jsonResp(w, 200, map[string]string{"status": "ok"})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectCrossSitePost(w, r) {
		return
	}

	clearCookie(w, isHTTPSRequest(r))
	jsonResp(w, 200, map[string]string{"status": "ok"})
}

func handleSave(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectCrossSitePost(w, r) {
		return
	}
	if !requireAuth(w, r) {
		return
	}

	var req saveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResp(w, 400, saveResp{Error: "请求体必须是合法的 JSON。"})
		return
	}

	baseURL, err := normalizeBaseURL(req.BaseURL)
	if err != nil {
		jsonResp(w, 400, saveResp{Error: err.Error()})
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		jsonResp(w, 400, saveResp{Error: "模型不能为空。"})
		return
	}

	reasoning := normalizeReasoningEffort(req.ReasoningEffort)
	if !reasoningOptions[reasoning] {
		jsonResp(w, 400, saveResp{Error: "推理强度只能填写 default、minimal、low、medium、high 之一；留空等于 default。"})
		return
	}

	if err := ensureSharedHome(); err != nil {
		jsonResp(w, 500, saveResp{Error: "初始化 Codex 配置目录失败：" + err.Error()})
		return
	}

	existingKey, err := readAPIKey(codexSettingsKeyPath)
	if err != nil {
		jsonResp(w, 500, saveResp{Error: "读取当前 API Key 失败：" + err.Error()})
		return
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = existingKey
	}
	if strings.TrimSpace(apiKey) == "" {
		jsonResp(w, 400, saveResp{Error: "API Key 不能为空；如果想沿用原值，请把这个输入框留空后直接保存。"})
		return
	}

	validation, warning, probeErr := probeRelay(baseURL, apiKey, model)
	if probeErr != nil {
		jsonResp(w, 400, saveResp{Error: probeErr.Error()})
		return
	}

	if err := writeConfig(baseURL, model, reasoning); err != nil {
		jsonResp(w, 500, saveResp{Error: "写入配置文件失败：" + err.Error()})
		return
	}
	if err := writeKey(apiKey); err != nil {
		jsonResp(w, 500, saveResp{Error: "写入 API Key 文件失败：" + err.Error()})
		return
	}

	if !isHTTPSRequest(r) {
		httpWarning := "当前页面仍然通过 HTTP 裸 IP 访问。配置已保存，但长期使用仍建议切到 HTTPS，或通过 SSH 隧道访问。"
		if warning == "" {
			warning = httpWarning
		} else {
			warning = warning + "\n" + httpWarning
		}
	}

	jsonResp(w, 200, saveResp{
		Status:     "保存成功。",
		Warning:    warning,
		Validation: validation,
	})
}

func loadState() (*settingsState, error) {
	if err := ensureSharedHome(); err != nil {
		return nil, err
	}

	state := &settingsState{}

	text, err := readTextFileIfExists(codexSettingsConfigPath)
	if err != nil {
		return nil, err
	}
	state.BaseURL = parseConfigValue(text, "base_url")
	state.Model = parseConfigValue(text, "model")
	state.ReasoningEffort = parseConfigValue(text, "model_reasoning_effort")

	return state, nil
}

func probeRelay(baseURL, apiKey, model string) (string, string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("准备中转站校验请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "配置已保存。", "暂时无法完成中转站连通性校验：" + err.Error() + "。请稍后再到 Codex CLI 中实测。", nil
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", "", fmt.Errorf("中转站拒绝了认证请求，请检查 API Key 是否正确（HTTP %d）。", resp.StatusCode)
	case http.StatusNotFound:
		return "配置已保存。", "当前中转站没有暴露 /models 接口，请直接到 Codex CLI 中实测。", nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "配置已保存。", fmt.Sprintf("中转站校验时返回 HTTP %d，请直接到 Codex CLI 中实测。", resp.StatusCode), nil
	}

	ids, err := extractModelIDs(raw)
	if err != nil {
		return "配置已保存。", "中转站已响应，但 /models 返回内容解析失败，请直接到 Codex CLI 中实测。", nil
	}
	if len(ids) == 0 {
		return "配置已保存。", "中转站已响应，但 /models 返回了空列表。", nil
	}
	if hasModelID(ids, model) {
		return "中转站校验通过，当前模型已在 /models 列表中找到。", "", nil
	}
	return "配置已保存。", "中转站已响应，但当前模型没有出现在 /models 列表中。", nil
}

func extractModelIDs(raw []byte) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func hasModelID(ids []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func ensureSharedHome() error {
	if err := os.MkdirAll(codexSettingsHome, 0700); err != nil {
		return err
	}
	return os.Chmod(codexSettingsHome, 0700)
}

func writeConfig(baseURL, model, reasoning string) error {
	text, err := readTextFileIfExists(codexSettingsConfigPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		text = defaultConfig()
	}

	text = setTopLevelQuotedValue(text, "model", model)
	text = setTopLevelQuotedValue(text, "model_provider", "jiushi")
	if reasoning == "" {
		text = deleteTopLevelKey(text, "model_reasoning_effort")
	} else {
		text = setTopLevelQuotedValue(text, "model_reasoning_effort", reasoning)
	}

	text = setOrAddSectionQuotedValue(text, "model_providers.jiushi", "name", "Jiushi Relay")
	text = setOrAddSectionQuotedValue(text, "model_providers.jiushi", "base_url", baseURL)
	text = setOrAddSectionQuotedValue(text, "model_providers.jiushi", "env_key", "OPENAI_API_KEY")
	text = setOrAddSectionQuotedValue(text, "model_providers.jiushi", "wire_api", "responses")
	text = setOrAddSectionRawValue(text, "model_providers.jiushi", "supports_websockets", "false")
	text = setOrAddSectionQuotedValue(text, `projects."/root"`, "trust_level", "trusted")
	text = setOrAddSectionQuotedValue(text, `projects."/opt/myapp"`, "trust_level", "trusted")
	text = strings.TrimRight(normalizeNewlines(text), "\n") + "\n"

	if err := os.WriteFile(codexSettingsConfigPath, []byte(text), 0600); err != nil {
		return err
	}
	return os.Chmod(codexSettingsConfigPath, 0600)
}

func writeKey(apiKey string) error {
	text, err := readTextFileIfExists(codexSettingsKeyPath)
	if err != nil {
		return err
	}

	line := "export OPENAI_API_KEY=" + shellSingleQuote(apiKey)
	text = upsertEnvLine(text, "OPENAI_API_KEY", line)
	text = strings.TrimRight(normalizeNewlines(text), "\n") + "\n"

	if err := os.WriteFile(codexSettingsKeyPath, []byte(text), 0600); err != nil {
		return err
	}
	return os.Chmod(codexSettingsKeyPath, 0600)
}

func defaultConfig() string {
	return strings.TrimSpace(`model = "gpt-5.5"
model_provider = "jiushi"

[model_providers.jiushi]
name = "Jiushi Relay"
base_url = "https://example.com/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
supports_websockets = false

[projects."/root"]
trust_level = "trusted"

[projects."/opt/myapp"]
trust_level = "trusted"`) + "\n"
}

func readTextFileIfExists(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func readAPIKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(normalizeNewlines(string(raw)), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "export ") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
		}
		if strings.HasPrefix(trimmed, "OPENAI_API_KEY=") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "OPENAI_API_KEY="))
			return shellUnquote(value), nil
		}
	}
	return "", nil
}

func upsertEnvLine(text, key, line string) string {
	lines := splitLines(text)
	prefixes := []string{key + "=", "export " + key + "="}
	for i, current := range lines {
		trimmed := strings.TrimSpace(current)
		for _, prefix := range prefixes {
			if strings.HasPrefix(trimmed, prefix) {
				lines[i] = line
				return joinLines(lines)
			}
		}
	}

	if len(lines) == 1 && lines[0] == "" {
		return line + "\n"
	}
	lines = trimTrailingBlankLines(lines)
	lines = append(lines, line)
	return joinLines(lines)
}

func parseConfigValue(text, key string) string {
	needle := key + " = "
	for _, line := range strings.Split(normalizeNewlines(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, needle) {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, needle))
			if unquoted, err := strconv.Unquote(value); err == nil {
				return unquoted
			}
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func setTopLevelQuotedValue(text, key, value string) string {
	return setTopLevelLine(text, key, key+" = "+strconv.Quote(value))
}

func setTopLevelLine(text, key, line string) string {
	lines := splitLines(text)
	firstSection := firstSectionIndex(lines)
	limit := len(lines)
	if firstSection >= 0 {
		limit = firstSection
	}

	for i := 0; i < limit; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), key+" =") {
			lines[i] = line
			return joinLines(lines)
		}
	}

	insertAt := limit
	for insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	lines = insertLine(lines, insertAt, line)
	return joinLines(lines)
}

func deleteTopLevelKey(text, key string) string {
	lines := splitLines(text)
	firstSection := firstSectionIndex(lines)
	limit := len(lines)
	if firstSection >= 0 {
		limit = firstSection
	}

	filtered := make([]string, 0, len(lines))
	for i, line := range lines {
		if i < limit && strings.HasPrefix(strings.TrimSpace(line), key+" =") {
			continue
		}
		filtered = append(filtered, line)
	}
	return joinLines(filtered)
}

func setOrAddSectionQuotedValue(text, section, key, value string) string {
	return setOrAddSectionLine(text, section, key, key+" = "+strconv.Quote(value))
}

func setOrAddSectionRawValue(text, section, key, rawValue string) string {
	return setOrAddSectionLine(text, section, key, key+" = "+rawValue)
}

func setOrAddSectionLine(text, section, key, line string) string {
	lines := splitLines(text)
	header := "[" + section + "]"
	start := -1
	for i, current := range lines {
		if strings.TrimSpace(current) == header {
			start = i
			break
		}
	}

	if start == -1 {
		lines = trimTrailingBlankLines(lines)
		if len(lines) > 0 && !(len(lines) == 1 && lines[0] == "") {
			lines = append(lines, "")
		}
		lines = append(lines, header, line)
		return joinLines(lines)
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isSectionHeader(lines[i]) {
			end = i
			break
		}
	}

	for i := start + 1; i < end; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), key+" =") {
			lines[i] = line
			return joinLines(lines)
		}
	}

	lines = insertLine(lines, end, line)
	return joinLines(lines)
}

func splitLines(text string) []string {
	normalized := normalizeNewlines(text)
	if normalized == "" {
		return []string{""}
	}
	return strings.Split(normalized, "\n")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func trimTrailingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []string{}
	}
	return lines
}

func firstSectionIndex(lines []string) int {
	for i, line := range lines {
		if isSectionHeader(line) {
			return i
		}
	}
	return -1
}

func isSectionHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
}

func insertLine(lines []string, index int, line string) []string {
	if index < 0 {
		index = 0
	}
	if index > len(lines) {
		index = len(lines)
	}
	lines = append(lines, "")
	copy(lines[index+1:], lines[index:])
	lines[index] = line
	return lines
}

func normalizeBaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("Base URL 不能为空。")
	}

	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("Base URL 不是合法的 URL。")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("Base URL 必须使用 https。")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("Base URL 缺少主机名。")
	}
	return strings.TrimRight(value, "/"), nil
}

func requireAuth(w http.ResponseWriter, r *http.Request) bool {
	expected := strings.TrimSpace(os.Getenv("CODEX_SETTINGS_PASSWORD"))
	if expected == "" {
		jsonResp(w, 503, map[string]string{"error": "服务端尚未配置页面密码。"})
		return false
	}

	cookie, err := r.Cookie(codexSettingsCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		jsonResp(w, 401, map[string]string{"error": "请先登录设置页。"})
		return false
	}
	if !secureEqual(cookie.Value, hashText(expected)) {
		jsonResp(w, 401, map[string]string{"error": "登录状态已失效，请重新登录。"})
		return false
	}
	return true
}

func rejectCrossSitePost(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Hostname(), requestHostname(r.Host)) {
			jsonResp(w, 403, map[string]string{"error": "禁止跨站提交设置请求。"})
			return true
		}
		return false
	}

	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if site != "" && site != "same-origin" && site != "same-site" && site != "none" {
		jsonResp(w, 403, map[string]string{"error": "禁止跨站提交设置请求。"})
		return true
	}
	return false
}

func requestHostname(host string) string {
	if strings.Contains(host, ":") {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			return strings.Trim(parsedHost, "[]")
		}
	}
	return strings.Trim(host, "[]")
}

func setCookie(w http.ResponseWriter, hashedPassword string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     codexSettingsCookieName,
		Value:    hashedPassword,
		Path:     codexSettingsCookiePath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   codexSettingsCookieMaxAge,
		Expires:  time.Now().Add(time.Second * codexSettingsCookieMaxAge),
	})
}

func clearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     codexSettingsCookieName,
		Value:    "",
		Path:     codexSettingsCookiePath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func normalizeReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "default" {
		return ""
	}
	return value
}

func shellUnquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		inner := value[1 : len(value)-1]
		return strings.ReplaceAll(inner, "'\\''", "'")
	}
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func jsonResp(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func normalizeNewlines(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}
