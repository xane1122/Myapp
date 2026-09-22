package db

import "time"

// RequestStats stores provider-reported usage, never an estimated token count.
// A chat row aggregates the model rows sharing its flow ID.
type RequestStats struct {
 RequestedAt time.Time
 ConversationID int64
 FlowID string
 Kind string
 InputTokens int
 OutputTokens int
 CacheHitTokens *int
 UsageReported bool
 CacheComplete bool
 RetrievalMS int64
 TotalLatencyMS int64
 Model string
 RequestCount int
 Failed bool
}

func RecordRequestStats(s RequestStats) error {
 var conversation, cache interface{}
 if s.ConversationID > 0 { conversation = s.ConversationID }
 if s.CacheHitTokens != nil { cache = *s.CacheHitTokens }
 _, err := DB.Exec(`INSERT INTO request_stats
 (requested_at,conversation_id,flow_id,record_kind,input_tokens,output_tokens,cache_hit_tokens,
 usage_reported,cache_complete,retrieval_ms,total_latency_ms,model,request_count,failed)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.RequestedAt.UTC().Format(time.RFC3339Nano),conversation,s.FlowID,s.Kind,
 s.InputTokens,s.OutputTokens,cache,s.UsageReported,s.CacheComplete,s.RetrievalMS,s.TotalLatencyMS,s.Model,s.RequestCount,s.Failed)
 return err
}
