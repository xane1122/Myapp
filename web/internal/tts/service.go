package tts

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type Service struct {
	Edge     *Client
	MiniMax  *MiniMaxClient
	AudioDir string
}

func NewServiceFromEnv() *Service {
	edge := NewFromEnv()
	return &Service{Edge: edge, MiniMax: NewMiniMaxFromEnv(), AudioDir: edge.AudioDir}
}

func (s *Service) Provider() string {
	if s != nil && s.MiniMax.Configured() {
		return "minimax"
	}
	return "edge"
}

func (s *Service) Synthesize(ctx context.Context, text string, messageID int64) (string, error) {
	if s != nil && s.MiniMax.Configured() {
		url, err := s.MiniMax.Synthesize(ctx, text, messageID)
		if err == nil {
			return url, nil
		}
		if strings.EqualFold(strings.TrimSpace(os.Getenv("EDGE_TTS_ENABLED")), "false") {
			return "", err
		}
		if fallbackURL, fallbackErr := s.Edge.Synthesize(ctx, text, messageID); fallbackErr == nil {
			return fallbackURL, nil
		} else {
			return "", fmt.Errorf("MiniMax failed: %v; Edge fallback failed: %w", err, fallbackErr)
		}
	}
	return s.Edge.Synthesize(ctx, text, messageID)
}

func (s *Service) ExistingURL(messageID int64) string {
	return s.Edge.ExistingURL(messageID)
}
