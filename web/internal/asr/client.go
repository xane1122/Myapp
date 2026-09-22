package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

const maxResponseBytes = 2 << 20

type Provider struct {
	Name     string
	Endpoint string
	APIKey   string
	Model    string
}

type Client struct {
	Providers []Provider
	HTTP      *http.Client
}

func NewFromEnv() *Client {
	providers := make([]Provider, 0, 2)
	if key := strings.TrimSpace(os.Getenv("GROQ_API_KEY")); key != "" {
		providers = append(providers, Provider{
			Name: "groq", APIKey: key,
			Endpoint: envOr("GROQ_ASR_URL", "https://api.groq.com/openai/v1/audio/transcriptions"),
			Model:    envOr("GROQ_ASR_MODEL", "whisper-large-v3-turbo"),
		})
	}
	if key := strings.TrimSpace(os.Getenv("ZHIPU_API_KEY")); key != "" {
		providers = append(providers, Provider{
			Name: "zhipu", APIKey: key,
			Endpoint: envOr("ZHIPU_ASR_URL", "https://open.bigmodel.cn/api/paas/v4/audio/transcriptions"),
			Model:    envOr("ZHIPU_ASR_MODEL", "glm-asr-2512"),
		})
	}
	return &Client{Providers: providers, HTTP: http.DefaultClient}
}

func (c *Client) Available() []string {
	names := make([]string, 0, len(c.Providers))
	for _, provider := range c.Providers {
		names = append(names, provider.Name)
	}
	return names
}

func (c *Client) Transcribe(ctx context.Context, filename, contentType string, audio []byte) (string, string, error) {
	if len(audio) == 0 {
		return "", "", errors.New("audio is empty")
	}
	if len(c.Providers) == 0 {
		return "", "", errors.New("ASR is not configured")
	}
	var failures []string
	for _, provider := range c.Providers {
		text, err := c.transcribeWith(ctx, provider, filename, contentType, audio)
		if err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text), provider.Name, nil
		}
		if err == nil {
			err = errors.New("empty transcription")
		}
		failures = append(failures, provider.Name+": "+err.Error())
	}
	return "", "", errors.New(strings.Join(failures, "; "))
}

func (c *Client) transcribeWith(ctx context.Context, provider Provider, filename, contentType string, audio []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partHeader := make(textproto.MIMEHeader)
	partHeader["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename="%s"`, filepath.Base(filename))}
	partHeader["Content-Type"] = []string{contentType}
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	_ = writer.WriteField("model", provider.Model)
	_ = writer.WriteField("language", "zh")
	_ = writer.WriteField("response_format", "json")
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.Endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result struct {
		Text string `json:"text"`
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if result.Text != "" {
		return result.Text, nil
	}
	return result.Data.Text, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
