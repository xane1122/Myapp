package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"myapp/internal/db"
	"strings"
	"sync"
	"time"
)

type ModelUsage struct {
	InputTokens    int
	OutputTokens   int
	CacheHitTokens *int
	Reported       bool
}

type modelUsageResponse struct {
	PromptTokens         int  `json:"prompt_tokens"`
	CompletionTokens     int  `json:"completion_tokens"`
	InputTokens          int  `json:"input_tokens"`
	OutputTokens         int  `json:"output_tokens"`
	CacheHitTokens       *int `json:"cache_hit_tokens"`
	CacheReadInputTokens *int `json:"cache_read_input_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokensDetails *struct {
		CachedTokens         *int `json:"cached_tokens"`
		CacheReadInputTokens *int `json:"cache_read_input_tokens"`
	} `json:"input_tokens_details"`
}

func parseModelUsage(raw json.RawMessage) ModelUsage {
	if len(raw) == 0 || string(raw) == "null" {
		return ModelUsage{}
	}
	var u modelUsageResponse
	if json.Unmarshal(raw, &u) != nil {
		return ModelUsage{}
	}
	input, output := u.PromptTokens, u.CompletionTokens
	if input == 0 {
		input = u.InputTokens
	}
	if output == 0 {
		output = u.OutputTokens
	}
	cache := u.CacheHitTokens
	if cache == nil {
		cache = u.CacheReadInputTokens
	}
	if cache == nil && u.PromptTokensDetails != nil {
		cache = u.PromptTokensDetails.CachedTokens
	}
	if cache == nil && u.InputTokensDetails != nil {
		cache = u.InputTokensDetails.CachedTokens
		if cache == nil {
			cache = u.InputTokensDetails.CacheReadInputTokens
		}
	}
	return ModelUsage{InputTokens: input, OutputTokens: output, CacheHitTokens: cache, Reported: true}
}

type statsContextKey struct{}
type chatStats struct {
	mu                           sync.Mutex
	start                        time.Time
	conversation                 int64
	flow                         string
	retrievalMS                  int64
	count                        int
	input, output, cache         int
	reported, cacheKnown, failed bool
	missingCache                 bool
	models                       []string
}

func newChatStats(conversation int64) *chatStats {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return &chatStats{start: time.Now(), conversation: conversation, flow: hex.EncodeToString(id[:])}
}

func (s *chatStats) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		return
	}
	var cache *int
	if s.cacheKnown {
		v := s.cache
		cache = &v
	}
	recordStats(db.RequestStats{RequestedAt: s.start, ConversationID: s.conversation, FlowID: s.flow, Kind: "chat",
		InputTokens: s.input, OutputTokens: s.output, CacheHitTokens: cache, UsageReported: s.reported,
		CacheComplete: s.cacheKnown && !s.missingCache, RetrievalMS: s.retrievalMS, TotalLatencyMS: time.Since(s.start).Milliseconds(),
		Model: strings.Join(s.models, ","), RequestCount: s.count, Failed: s.failed})
}

func recordModelStats(ctx context.Context, model string, start time.Time, usage ModelUsage, failed bool) {
	row := db.RequestStats{RequestedAt: start, Kind: "model", Model: model, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheHitTokens: usage.CacheHitTokens, UsageReported: usage.Reported, CacheComplete: usage.CacheHitTokens != nil,
		TotalLatencyMS: time.Since(start).Milliseconds(), RequestCount: 1, Failed: failed}
	if s, ok := ctx.Value(statsContextKey{}).(*chatStats); ok {
		s.mu.Lock()
		row.ConversationID = s.conversation
		row.FlowID = s.flow
		row.RetrievalMS = s.retrievalMS
		s.count++
		s.input += usage.InputTokens
		s.output += usage.OutputTokens
		s.reported = s.reported || usage.Reported
		s.failed = s.failed || failed
		if usage.CacheHitTokens != nil {
			s.cache += *usage.CacheHitTokens
			s.cacheKnown = true
		} else {
			s.missingCache = true
		}
		found := false
		for _, m := range s.models {
			if m == model {
				found = true
			}
		}
		if !found {
			s.models = append(s.models, model)
		}
		s.mu.Unlock()
	}
	recordStats(row)
}

func recordStats(row db.RequestStats) {
	if db.DB == nil {
		return
	}
	if err := db.RecordRequestStats(row); err != nil {
		log.Printf("request_stats insert failed: %v", err)
	}
}

func optionalParent(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	return context.Background()
}
