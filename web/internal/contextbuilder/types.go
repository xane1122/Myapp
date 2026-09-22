package contextbuilder

type Sensitivity string

const (
	SensitivityLow    Sensitivity = "low"
	SensitivityMedium Sensitivity = "medium"
	SensitivityHigh   Sensitivity = "high"
)

type ContextJSON struct {
 DynamicRules string `json:"dynamic_rules,omitempty"`
	ContextVersion     string                      `json:"context_version"`
	RequestID          string                      `json:"request_id"`
	GeneratedAt        string                      `json:"generated_at"`
	Locale             string                      `json:"locale"`
	AssistantRole      AssistantRole               `json:"assistant_role"`
	RuntimeRules       RuntimeRules                `json:"runtime_rules"`
	ToolPolicy         ToolPolicy                  `json:"tool_policy"`
	UserProfile        UserProfile                 `json:"user_profile"`
	MemoryPolicy       MemoryPolicy                `json:"memory_policy"`
	ConversationState  ConversationState           `json:"conversation_state"`
	RecentMessages     []RecentMessage             `json:"recent_messages"`
	RetrievedMemory    []RetrievedMemory           `json:"retrieved_memory"`
	RetrievedDocChunks []RetrievedDocChunk         `json:"retrieved_doc_chunks"`
	CoreMemory         []RetrievedMemory           `json:"core_memory"`
	ActiveMemory       []RetrievedMemory           `json:"active_memory"`
	MemoryIndex        []MemoryIndexItem           `json:"memory_index"`
	UploadedDocuments  []UploadedDocumentIndexItem `json:"uploaded_documents"`
	CurrentUserMessage string                      `json:"current_user_message"`
}

type AssistantRole struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	CoreBehavior string `json:"core_behavior"`
}

type RuntimeRules struct {
	MemoryIndexIsNotFullTruth           bool `json:"memory_index_is_not_full_truth"`
	UploadedDocMetadataIsNotBody        bool `json:"uploaded_doc_metadata_is_not_body"`
	MustUseToolsForRealtimeOrPastFacts  bool `json:"must_use_tools_for_realtime_or_past_facts"`
	DoNotExposeHighSensitivityByDefault bool `json:"do_not_expose_high_sensitivity_by_default"`
}

type ToolPolicy struct {
	TimeQuery        ToolRule `json:"time_query"`
	MemoryQuery      ToolRule `json:"memory_query"`
	UploadedDocQuery ToolRule `json:"uploaded_doc_query"`
	ServerQuery      ToolRule `json:"server_query"`
}

type ToolRule struct {
	When         []string `json:"when"`
	RequiredTool string   `json:"required_tool"`
	Rule         string   `json:"rule"`
}

type UserProfile struct {
	Name                   string   `json:"name,omitempty"`
	AssistantName          string   `json:"assistant_name,omitempty"`
	RelationshipStyle      string   `json:"relationship_style,omitempty"`
	PreferredResponseStyle string   `json:"preferred_response_style,omitempty"`
	EmotionHandling        string   `json:"emotion_handling,omitempty"`
	Likes                  []string `json:"likes,omitempty"`
	Dislikes               []string `json:"dislikes,omitempty"`
	SafetyWord             string   `json:"safety_word,omitempty"`
}

type MemoryPolicy struct {
	Mode                                      string   `json:"mode"`
	Description                               string   `json:"description"`
	DefaultUseIntimateMemory                  bool     `json:"default_use_intimate_memory"`
	DefaultInjectRawIntimateTranscript        bool     `json:"default_inject_raw_intimate_transcript"`
	DefaultInjectDistilledIntimatePreferences bool     `json:"default_inject_distilled_intimate_preferences"`
	RespectUserCurrentIntent                  bool     `json:"respect_user_current_intent"`
	RespectSafetyWord                         bool     `json:"respect_safety_word"`
	DefaultBehavior                           string   `json:"default_behavior,omitempty"`
	SensitivityLevels                         []string `json:"sensitivity_levels,omitempty"`
}

type MemoryIndexItem struct {
	MemoryID      int64       `json:"memory_id"`
	Type          string      `json:"type"`
	Title         string      `json:"title,omitempty"`
	Summary       string      `json:"summary"`
	Keywords      []string    `json:"keywords,omitempty"`
	SourceType    string      `json:"source_type"`
	Sensitivity   Sensitivity `json:"sensitivity"`
	DefaultInject bool        `json:"default_inject"`
	RetrieveWhen  []string    `json:"retrieve_when,omitempty"`
}

type RetrievedMemory struct {
 SourceDate string `json:"source_date,omitempty"`
 OriginalSnippet string `json:"original_snippet,omitempty"`
 MatchScore float64 `json:"match_score,omitempty"`
 RankMethod string `json:"rank_method,omitempty"`
	MemoryID        int64       `json:"memory_id"`
	SourceType      string      `json:"source_type"`
	Confidence      string      `json:"confidence"`
	Content         string      `json:"content"`
	EvidenceSummary string      `json:"evidence_summary,omitempty"`
	Sensitivity     Sensitivity `json:"sensitivity"`
	AllowedUsage    string      `json:"allowed_usage,omitempty"`
}

type UploadedDocumentIndexItem struct {
	DocID                   int64       `json:"doc_id"`
	Title                   string      `json:"title"`
	Status                  string      `json:"status"`
	Type                    string      `json:"type,omitempty"`
	Summary                 string      `json:"summary,omitempty"`
	Sensitivity             Sensitivity `json:"sensitivity"`
	RequiresReadUploadedDoc bool        `json:"requires_read_uploaded_doc"`
}

type RetrievedDocChunk struct {
	DocID       int64       `json:"doc_id"`
	Title       string      `json:"title"`
	ChunkID     string      `json:"chunk_id"`
	Content     string      `json:"content"`
	Reason      string      `json:"reason"`
	Sensitivity Sensitivity `json:"sensitivity"`
}

type ConversationState struct {
	CurrentTopic   string   `json:"current_topic,omitempty"`
	ActiveGoals    []string `json:"active_goals,omitempty"`
	KnownDecisions []string `json:"known_decisions,omitempty"`
	OpenQuestions  []string `json:"open_questions,omitempty"`
}

type RecentMessage struct {
	Role      string `json:"role"`
	MessageID int64  `json:"message_id,omitempty"`
	Content   string `json:"content"`
}

type MemoryRouteDecision struct {
	NeedSearchMemory  bool    `json:"need_search_memory"`
	TargetMemoryIDs   []int64 `json:"target_memory_ids"`
	Query             string  `json:"query"`
	RequireQueryMatch bool    `json:"require_query_match,omitempty"`
	Reason            string  `json:"reason"`
}

type DocRouteDecision struct {
	NeedReadUploadedDoc bool    `json:"need_read_uploaded_doc"`
	TargetDocIDs        []int64 `json:"target_doc_ids"`
	Query               string  `json:"query"`
	Reason              string  `json:"reason"`
}

type TimeRouteDecision struct {
	NeedCurrentTime bool   `json:"need_current_time"`
	Reason          string `json:"reason"`
}

type ServerRouteDecision struct {
	NeedLinuxShell bool   `json:"need_linux_shell"`
	Reason         string `json:"reason"`
}

type DebugInfo struct {
	RequestID              string              `json:"request_id"`
	TokenEstimate          int                 `json:"token_estimate"`
	MemoryRoute            MemoryRouteDecision `json:"memory_route"`
	DocRoute               DocRouteDecision    `json:"doc_route"`
	TimeRoute              TimeRouteDecision   `json:"time_route"`
	ServerRoute            ServerRouteDecision `json:"server_route"`
	InjectedMemoryIDs      []int64             `json:"injected_memory_ids"`
	InjectedDocChunks      []string            `json:"injected_doc_chunks"`
	FilteredSensitiveItems []string            `json:"filtered_sensitive_items"`
	FinalContextJSON       ContextJSON         `json:"final_context_json"`
}
