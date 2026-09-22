package tts

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSocket struct {
	sent      []string
	responses [][]byte
}

func (s *fakeSocket) Send(payload string) error {
	s.sent = append(s.sent, payload)
	return nil
}
func (s *fakeSocket) Receive() ([]byte, error) {
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}
func (s *fakeSocket) Close() error { return nil }

func TestSplitTextUsesSentenceBoundariesAndLimit(t *testing.T) {
	text := strings.Repeat("甲", 300) + "。" + strings.Repeat("乙", 300)
	segments := SplitText(text, 500)
	if len(segments) != 2 || len([]rune(segments[0])) != 301 {
		t.Fatalf("segments = %#v", segments)
	}
	for _, segment := range segments {
		if len([]rune(segment)) > 500 {
			t.Fatalf("segment exceeds 500 runes: %d", len([]rune(segment)))
		}
	}
}

func TestSynthesizeConcatenatesSegmentsAndNamesFile(t *testing.T) {
	dir := t.TempDir()
	var sockets []*fakeSocket
	client := &Client{
		AudioDir: dir,
		Voice:    "zh-CN-TestNeural",
		Now:      func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) },
		Dial: func(context.Context, string) (socket, error) {
			s := &fakeSocket{responses: [][]byte{audioFrame([]byte("MP3")), []byte("Path:turn.end\r\n")}}
			sockets = append(sockets, s)
			return s, nil
		},
	}
	url, err := client.Synthesize(context.Background(), strings.Repeat("中", 501), 42)
	if err != nil {
		t.Fatal(err)
	}
	if url != "/audio/2026-08-04-42.mp3" || len(sockets) != 2 {
		t.Fatalf("url=%q sockets=%d", url, len(sockets))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "2026-08-04-42.mp3"))
	if err != nil || string(raw) != "MP3MP3" {
		t.Fatalf("audio=%q err=%v", raw, err)
	}
	if !strings.Contains(sockets[0].sent[1], "zh-CN-TestNeural") {
		t.Fatalf("SSML does not contain configured voice: %q", sockets[0].sent[1])
	}
	if got := client.ExistingURL(42); got != url {
		t.Fatalf("ExistingURL=%q want %q", got, url)
	}
}

func TestBuildSSMLEscapesText(t *testing.T) {
	ssml := buildSSML(`voice'&`, `<你好 & 再见>`)
	if strings.Contains(ssml, "<你好") || !strings.Contains(ssml, "&lt;你好 &amp; 再见&gt;") || !strings.Contains(ssml, "voice&#39;&amp;") {
		t.Fatalf("unsafe SSML: %s", ssml)
	}
}

func TestEdgeIntegration(t *testing.T) {
	if os.Getenv("EDGE_TTS_INTEGRATION") != "1" {
		t.Skip("set EDGE_TTS_INTEGRATION=1 to call the Edge Read Aloud service")
	}
	client := NewFromEnv()
	if integrationDir := strings.TrimSpace(os.Getenv("EDGE_TTS_INTEGRATION_DIR")); integrationDir != "" {
		client.AudioDir = integrationDir
	} else {
		client.AudioDir = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url, err := client.Synthesize(ctx, "你好，这是一段语音下载测试。", 9001)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(client.AudioDir, filepath.Base(url)))
	if err != nil || len(raw) < 1000 {
		t.Fatalf("audio bytes=%d err=%v", len(raw), err)
	}
}

func audioFrame(audio []byte) []byte {
	header := []byte("Content-Type:audio/mpeg\r\nPath:audio\r\n")
	payload := make([]byte, 2+len(header)+len(audio))
	binary.BigEndian.PutUint16(payload[:2], uint16(len(header)))
	copy(payload[2:], header)
	copy(payload[2+len(header):], audio)
	return payload
}
