package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MiniMaxClient struct {
	APIKey   string
	GroupID  string
	VoiceID  string
	Model    string
	Endpoint string
	AudioDir string
	HTTP     *http.Client
}

func NewMiniMaxFromEnv() *MiniMaxClient {
	return &MiniMaxClient{
		APIKey: strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")), GroupID: strings.TrimSpace(os.Getenv("MINIMAX_GROUP_ID")),
		VoiceID: strings.TrimSpace(os.Getenv("MINIMAX_VOICE_ID")), Model: envOr("MINIMAX_TTS_MODEL", "speech-02-hd"),
		Endpoint: envOr("MINIMAX_TTS_URL", "https://api.minimax.chat/v1/t2a_v2"), AudioDir: envOr("EDGE_TTS_AUDIO_DIR", defaultAudioDir),
		HTTP: &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *MiniMaxClient) Configured() bool {
	return c != nil && c.APIKey != "" && c.GroupID != "" && c.VoiceID != ""
}

func (c *MiniMaxClient) Synthesize(ctx context.Context, text string, messageID int64) (string, error) {
	if !c.Configured() {
		return "", errors.New("MiniMax TTS is not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" || messageID <= 0 {
		return "", errors.New("TTS text and message ID are required")
	}
	payload := map[string]interface{}{
		"model": c.Model, "text": text, "stream": false,
		"voice_setting": map[string]interface{}{"voice_id": c.VoiceID, "speed": 1, "vol": 1, "pitch": 0},
		"audio_setting": map[string]interface{}{"sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("GroupId", c.GroupID)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("MiniMax HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var result struct {
		Data struct {
			Audio string `json:"audio"`
		} `json:"data"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("decode MiniMax response: %w", err)
	}
	if result.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("MiniMax: %s (%d)", result.BaseResp.StatusMsg, result.BaseResp.StatusCode)
	}
	audio, err := decodeMiniMaxAudio(result.Data.Audio)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(c.AudioDir, 0o755); err != nil {
		return "", err
	}
	filename := fmt.Sprintf("%s-%d.mp3", time.Now().Format("2006-01-02"), messageID)
	finalPath := filepath.Join(c.AudioDir, filename)
	tmp, err := os.CreateTemp(c.AudioDir, ".minimax-*.mp3")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write(audio); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", err
	}
	return "/audio/" + url.PathEscape(filename), nil
}

func decodeMiniMaxAudio(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("MiniMax returned no audio")
	}
	if audio, err := hex.DecodeString(value); err == nil {
		return audio, nil
	}
	if audio, err := base64.StdEncoding.DecodeString(value); err == nil {
		return audio, nil
	}
	return nil, errors.New("MiniMax returned invalid audio encoding")
}
