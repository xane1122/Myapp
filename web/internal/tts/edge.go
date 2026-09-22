package tts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const (
	defaultEndpoint = "wss://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1?TrustedClientToken=6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	defaultVoice    = "zh-CN-YunxiNeural"
	defaultAudioDir = "/opt/myapp/audio"
	maxSegmentRunes = 500
	trustedToken    = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"
	edgeVersion     = "1-143.0.3650.75"
)

type Client struct {
	Voice    string
	AudioDir string
	Endpoint string
	Dial     func(context.Context, string) (socket, error)
	Now      func() time.Time
}

type socket interface {
	Send(string) error
	Receive() ([]byte, error)
	Close() error
}

type websocketSocket struct{ conn *websocket.Conn }

func (s websocketSocket) Send(payload string) error { return websocket.Message.Send(s.conn, payload) }
func (s websocketSocket) Receive() ([]byte, error) {
	var payload []byte
	err := websocket.Message.Receive(s.conn, &payload)
	return payload, err
}
func (s websocketSocket) Close() error { return s.conn.Close() }

func NewFromEnv() *Client {
	return &Client{
		Voice:    envOr("EDGE_TTS_VOICE", defaultVoice),
		AudioDir: envOr("EDGE_TTS_AUDIO_DIR", defaultAudioDir),
		Endpoint: envOr("EDGE_TTS_ENDPOINT", defaultEndpoint),
	}
}

func (c *Client) Synthesize(ctx context.Context, text string, messageID int64) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || messageID <= 0 {
		return "", errors.New("TTS text and message ID are required")
	}
	segments := SplitText(text, maxSegmentRunes)
	if len(segments) == 0 {
		return "", errors.New("TTS text is empty")
	}
	if err := os.MkdirAll(c.audioDir(), 0o755); err != nil {
		return "", fmt.Errorf("create audio directory: %w", err)
	}
	filename := fmt.Sprintf("%s-%d.mp3", c.now().Format("2006-01-02"), messageID)
	finalPath := filepath.Join(c.audioDir(), filename)
	tmp, err := os.CreateTemp(c.audioDir(), ".tts-*.mp3")
	if err != nil {
		return "", fmt.Errorf("create temporary audio: %w", err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	for _, segment := range segments {
		audio, synthErr := c.synthesizeSegment(ctx, segment)
		if synthErr != nil {
			return "", synthErr
		}
		if _, err := tmp.Write(audio); err != nil {
			return "", fmt.Errorf("write audio: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync audio: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close audio: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", fmt.Errorf("publish audio: %w", err)
	}
	ok = true
	return "/audio/" + url.PathEscape(filename), nil
}

func (c *Client) ExistingURL(messageID int64) string {
	if messageID <= 0 {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(c.audioDir(), fmt.Sprintf("????-??-??-%d.mp3", messageID)))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return "/audio/" + url.PathEscape(filepath.Base(matches[len(matches)-1]))
}

func (c *Client) synthesizeSegment(ctx context.Context, text string) ([]byte, error) {
	requestID := fmt.Sprintf("%032x", c.now().UnixNano())
	dial := c.Dial
	if dial == nil {
		dial = c.dial
	}
	conn, err := dial(ctx, endpointWithAuth(c.endpoint(), requestID, c.now()))
	if err != nil {
		return nil, fmt.Errorf("connect Edge TTS: %w", err)
	}
	defer conn.Close()
	config := map[string]interface{}{"context": map[string]interface{}{"synthesis": map[string]interface{}{"audio": map[string]interface{}{"metadataoptions": map[string]bool{"sentenceBoundaryEnabled": false, "wordBoundaryEnabled": false}, "outputFormat": "audio-24khz-48kbitrate-mono-mp3"}}}}
	configJSON, _ := json.Marshal(config)
	if err := conn.Send(fmt.Sprintf("X-Timestamp:%s\r\nContent-Type:application/json; charset=utf-8\r\nPath:speech.config\r\n\r\n%s", edgeTimestamp(c.now()), configJSON)); err != nil {
		return nil, fmt.Errorf("send TTS config: %w", err)
	}
	ssml := buildSSML(c.voice(), text)
	if err := conn.Send(fmt.Sprintf("X-RequestId:%s\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:%s\r\nPath:ssml\r\n\r\n%s", requestID, edgeTimestamp(c.now()), ssml)); err != nil {
		return nil, fmt.Errorf("send TTS text: %w", err)
	}
	var audio bytes.Buffer
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		payload, err := conn.Receive()
		if err != nil {
			return nil, fmt.Errorf("read Edge TTS response: %w", err)
		}
		if len(payload) >= 2 {
			headerLen := int(binary.BigEndian.Uint16(payload[:2]))
			if headerLen <= len(payload)-2 {
				header := string(payload[2 : 2+headerLen])
				if strings.Contains(header, "Path:audio") {
					audio.Write(payload[2+headerLen:])
					continue
				}
			}
		}
		message := string(payload)
		if strings.Contains(message, "Path:turn.end") {
			if audio.Len() == 0 {
				return nil, errors.New("Edge TTS returned no audio")
			}
			return audio.Bytes(), nil
		}
		if strings.Contains(message, "Path:response") && strings.Contains(strings.ToLower(message), "error") {
			return nil, fmt.Errorf("Edge TTS error: %s", message)
		}
	}
}

func (c *Client) dial(ctx context.Context, endpoint string) (socket, error) {
	config, err := websocket.NewConfig(endpoint, "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	if err != nil {
		return nil, err
	}
	config.Header = http.Header{
		"User-Agent":      {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"},
		"Accept-Encoding": {"gzip, deflate, br, zstd"},
		"Accept-Language": {"en-US,en;q=0.9"},
		"Pragma":          {"no-cache"},
		"Cache-Control":   {"no-cache"},
		"Cookie":          {"muid=" + randomMUID() + ";"},
	}
	config.Dialer = nil
	conn, err := websocket.DialConfig(config)
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	return websocketSocket{conn: conn}, nil
}

func SplitText(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	runes := []rune(strings.TrimSpace(text))
	var result []string
	for len(runes) > 0 {
		end := min(limit, len(runes))
		if end < len(runes) {
			for i := end - 1; i >= end/2; i-- {
				if strings.ContainsRune("。！？!?；;\n", runes[i]) {
					end = i + 1
					break
				}
			}
		}
		segment := strings.TrimSpace(string(runes[:end]))
		if segment != "" {
			result = append(result, segment)
		}
		runes = runes[end:]
	}
	return result
}

func buildSSML(voice, text string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(text))
	var escapedVoice bytes.Buffer
	_ = xml.EscapeText(&escapedVoice, []byte(voice))
	return `<speak version='1.0' xml:lang='zh-CN'><voice name='` + escapedVoice.String() + `'><prosody pitch='+0Hz' rate='+0%' volume='+0%'>` + escaped.String() + `</prosody></voice></speak>`
}

func edgeTimestamp(t time.Time) string {
	return t.UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}
func endpointWithAuth(endpoint, id string, now time.Time) string {
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + "Sec-MS-GEC=" + secMSGEC(now) + "&Sec-MS-GEC-Version=" + url.QueryEscape(edgeVersion) + "&ConnectionId=" + url.QueryEscape(id)
}
func secMSGEC(now time.Time) string {
	seconds := now.UTC().Unix() + 11644473600
	seconds -= seconds % 300
	windowsTicks := seconds * 10_000_000
	digest := sha256.Sum256([]byte(strconv.FormatInt(windowsTicks, 10) + trustedToken))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}
func randomMUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%032X", time.Now().UnixNano())
	}
	return strings.ToUpper(hex.EncodeToString(raw[:]))
}
func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func (c *Client) voice() string {
	if strings.TrimSpace(c.Voice) != "" {
		return c.Voice
	}
	return defaultVoice
}
func (c *Client) audioDir() string {
	if strings.TrimSpace(c.AudioDir) != "" {
		return c.AudioDir
	}
	return defaultAudioDir
}
func (c *Client) endpoint() string {
	if strings.TrimSpace(c.Endpoint) != "" {
		return c.Endpoint
	}
	return defaultEndpoint
}
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
