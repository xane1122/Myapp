package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
)

const (
	defaultFetchURLMaxChars = 12000
	maximumFetchURLMaxChars = 50000
	maximumFetchURLBodySize = 8 << 20
)

type fetchURLArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
}

type fetchURLResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	ContentLength int    `json:"content_length"`
	Truncated     bool   `json:"truncated"`
}

func runFetchURLTool(rawArgs string) string {
	var args fetchURLArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return toolErrorJSON("参数格式错误")
	}
	result, err := fetchReadableURL(args.URL, args.MaxChars, false)
	if err != nil {
		return toolErrorJSON(err.Error())
	}
	b, _ := json.Marshal(result)
	return string(b)
}

func fetchReadableURL(rawURL string, maxChars int, allowPrivate bool) (fetchURLResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parsed, err := validateFetchURL(ctx, rawURL, allowPrivate)
	if err != nil {
		return fetchURLResult{}, err
	}
	if maxChars == 0 {
		maxChars = defaultFetchURLMaxChars
	}
	if maxChars < 200 || maxChars > maximumFetchURLMaxChars {
		return fetchURLResult{}, fmt.Errorf("max_chars须在200到%d之间", maximumFetchURLMaxChars)
	}

	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 8 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolvePublicIPs(ctx, host, allowPrivate)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ip := range ips {
				conn, err := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return errors.New("网页重定向超过3次")
			}
			_, err := validateFetchURL(req.Context(), req.URL.String(), allowPrivate)
			return err
		},
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fetchURLResult{}, errors.New("网址无效")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MyAssistant/1.0; +https://xanelove.com/)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		return fetchURLResult{}, fmt.Errorf("网页读取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fetchURLResult{}, fmt.Errorf("网页返回HTTP %d", resp.StatusCode)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml+xml") {
		return fetchURLResult{}, errors.New("链接返回的不是HTML网页")
	}

	limited := &io.LimitedReader{R: resp.Body, N: maximumFetchURLBodySize + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		return fetchURLResult{}, fmt.Errorf("网页读取失败: %w", err)
	}
	if len(body) > maximumFetchURLBodySize {
		return fetchURLResult{}, errors.New("网页内容超过8MB限制")
	}
	article, err := readability.FromReader(strings.NewReader(string(body)), resp.Request.URL)
	if err != nil {
		return fetchURLResult{}, fmt.Errorf("网页正文提取失败: %w", err)
	}
	content := strings.TrimSpace(article.TextContent)
	if content == "" {
		return fetchURLResult{}, errors.New("网页没有可读取的正文")
	}
	runes := []rune(content)
	contentLength := len(runes)
	truncated := contentLength > maxChars
	if truncated {
		content = string(runes[:maxChars])
	}
	return fetchURLResult{
		Title:         strings.TrimSpace(article.Title),
		URL:           resp.Request.URL.String(),
		Content:       content,
		ContentLength: contentLength,
		Truncated:     truncated,
	}, nil
}

func validateFetchURL(ctx context.Context, rawURL string, allowPrivate bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("网址无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("只支持http或https网址")
	}
	if parsed.User != nil {
		return nil, errors.New("网址不能包含登录凭据")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("网址端口无效")
		}
	}
	if _, err := resolvePublicIPs(ctx, parsed.Hostname(), allowPrivate); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolvePublicIPs(ctx context.Context, host string, allowPrivate bool) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("网址域名无法解析")
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip := address.IP
		if !allowPrivate && isPrivateIP(ip) {
			return nil, errors.New("拒绝访问内网或非公网地址")
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 0 || (v4[0] == 100 && v4[1]&0xc0 == 64)
	}
	return false
}

func toolErrorJSON(message string) string {
	b, _ := json.Marshal(map[string]string{"error": message})
	return string(b)
}
