package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"

	"myapp/internal/db"
	"myapp/internal/memory"
)

const (
	replyChannel      = "reply"
	grokChannel       = "grok"
	rhysMemoryChannel = "memory_rhys"
	grokMemoryChannel = "memory_grok"
	roleplayChannel   = "roleplay"
)

type modelChannelConfig struct {
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	AssistantName string `json:"assistant_name"`
	APIKey        string `json:"-"`
}

type modelChannelView struct {
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	HasAPIKey     bool   `json:"has_api_key"`
	APIKey        string `json:"api_key"`
	AssistantName string `json:"assistant_name"`
}

type modelChannelInput struct {
	BaseURL       *string `json:"base_url"`
	Model         *string `json:"model"`
	APIKey        *string `json:"api_key"`
	AssistantName *string `json:"assistant_name"`
}

var modelChannelMu sync.RWMutex

func modelChannelString(value string) *string { return &value }

func assistantNameForChannel(channel string) string {
	cfg := loadModelChannel(channel)
	if name := strings.TrimSpace(cfg.AssistantName); name != "" {
		return name
	}
	if channel == grokChannel {
		return "Tail"
	}
	if channel == replyChannel {
		return "Rhys"
	}
	return strings.TrimSpace(channel)
}

func assistantNameForConversation(conversationID int64) string {
	return assistantNameForAssistant(memory.ConversationAssistant(conversationID))
}

func assistantNameForAssistant(assistant string) string {
	if strings.EqualFold(strings.TrimSpace(assistant), "grok") {
		return assistantNameForChannel(grokChannel)
	}
	return assistantNameForChannel(replyChannel)
}

func memoryChannelForAssistant(assistant string) string {
	if strings.EqualFold(strings.TrimSpace(assistant), "grok") {
		return grokMemoryChannel
	}
	return rhysMemoryChannel
}

func replyModelChannelForConversation(conversationID int64) modelChannelConfig {
	if strings.EqualFold(strings.TrimSpace(memory.ConversationAssistant(conversationID)), "grok") {
		return loadModelChannel(grokChannel)
	}
	return loadModelChannel(replyChannel)
}

func backgroundModelChannelForConversation(conversationID int64) modelChannelConfig {
	assistant := memory.ConversationAssistant(conversationID)
	main := replyModelChannelForConversation(conversationID)
	return mergeModelChannel(loadModelChannel(memoryChannelForAssistant(assistant)), main)
}

func modelChannelComplete(cfg modelChannelConfig) bool {
	return strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.Model) != ""
}

// Channel B inherits only its blank fields from channel A. This keeps every
// value explicitly saved for B effective while preserving the all-blank
// "use channel A" setting shown in the UI.
func mergeModelChannel(preferred, fallback modelChannelConfig) modelChannelConfig {
	if strings.TrimSpace(preferred.APIKey) == "" {
		preferred.APIKey = fallback.APIKey
	}
	if strings.TrimSpace(preferred.BaseURL) == "" {
		preferred.BaseURL = fallback.BaseURL
	}
	if strings.TrimSpace(preferred.Model) == "" {
		preferred.Model = fallback.Model
	}
	if strings.TrimSpace(preferred.AssistantName) == "" {
		preferred.AssistantName = fallback.AssistantName
	}
	return preferred
}

func loadModelChannel(channel string) modelChannelConfig {
	cfg := loadStoredModelChannel(channel)
	if channel == rhysMemoryChannel {
		return mergeModelChannel(cfg, loadStoredModelChannel(replyChannel))
	}
	if channel == grokMemoryChannel {
		return mergeModelChannel(cfg, loadStoredModelChannel(grokChannel))
	}
	return cfg
}

func loadStoredModelChannel(channel string) modelChannelConfig {
	modelChannelMu.RLock()
	defer modelChannelMu.RUnlock()
	var cfg modelChannelConfig
	err := db.DB.QueryRow(`SELECT base_url,model,COALESCE(assistant_name,''),api_key FROM model_channel_configs WHERE channel=?`, channel).
		Scan(&cfg.BaseURL, &cfg.Model, &cfg.AssistantName, &cfg.APIKey)
	if err == nil {
		return cfg
	}
	if channel == rhysMemoryChannel || channel == grokMemoryChannel {
		return cfg
	}
	cfg.APIKey = os.Getenv("OPENROUTER_API_KEY")
	cfg.BaseURL = os.Getenv("OPENROUTER_BASE_URL")
	cfg.Model = firstNonEmpty(os.Getenv("MAIN_MODEL"), "anthropic/claude-sonnet-4-6")
	return cfg
}

func hasStoredModelChannel(channel string) bool {
	var count int
	return db.DB.QueryRow(`SELECT COUNT(*) FROM model_channel_configs WHERE channel=?`, channel).Scan(&count) == nil && count > 0
}

func saveModelChannel(channel string, input modelChannelInput) error {
	modelChannelMu.Lock()
	defer modelChannelMu.Unlock()
	var baseURL, model, assistantName, key string
	_ = db.DB.QueryRow(`SELECT base_url,model,COALESCE(assistant_name,''),api_key FROM model_channel_configs WHERE channel=?`, channel).Scan(&baseURL, &model, &assistantName, &key)
	if input.APIKey != nil {
		key = strings.TrimSpace(*input.APIKey)
	}
	if input.BaseURL != nil {
		baseURL = strings.TrimSpace(*input.BaseURL)
	}
	if input.Model != nil {
		model = strings.TrimSpace(*input.Model)
	}
	if input.AssistantName != nil {
		assistantName = strings.TrimSpace(*input.AssistantName)
	}
	if channel == grokChannel && strings.EqualFold(assistantName, "Grok") {
		assistantName = "Tail"
	}
	_, err := db.DB.Exec(`INSERT INTO model_channel_configs(channel,base_url,model,assistant_name,api_key,updated_at)
		VALUES(?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(channel) DO UPDATE SET
		base_url=excluded.base_url,model=excluded.model,assistant_name=excluded.assistant_name,api_key=excluded.api_key,updated_at=CURRENT_TIMESTAMP`,
		channel, baseURL, model, assistantName, key)
	return err
}

func modelChannelViews() map[string]modelChannelView {
	result := make(map[string]modelChannelView, 5)
	for _, channel := range []string{replyChannel, grokChannel, rhysMemoryChannel, grokMemoryChannel, roleplayChannel} {
		cfg := loadStoredModelChannel(channel)
		result[channel] = modelChannelView{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, AssistantName: assistantNameForChannel(channel), HasAPIKey: strings.TrimSpace(cfg.APIKey) != ""}
	}
	return result
}

func handleModelChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResp(w, http.StatusOK, map[string]interface{}{"channels": modelChannelViews()})
	case http.MethodPost:
		var input struct {
			Reply      *modelChannelInput `json:"reply"`
			Grok       *modelChannelInput `json:"grok"`
			RhysMemory *modelChannelInput `json:"memory_rhys"`
			GrokMemory *modelChannelInput `json:"memory_grok"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "配置格式错误"})
			return
		}
		channels := map[string]*modelChannelInput{replyChannel: input.Reply, rhysMemoryChannel: input.RhysMemory, grokMemoryChannel: input.GrokMemory}
		if input.Grok != nil {
			channels[grokChannel] = input.Grok
		}
		for channel, cfg := range channels {
			if cfg == nil {
				continue
			}
			if (channel == replyChannel || channel == grokChannel) && cfg.Model != nil && strings.TrimSpace(*cfg.Model) == "" {
				jsonResp(w, http.StatusBadRequest, map[string]string{"error": channel + " 通道必须填写模型"})
				return
			}
			if cfg.BaseURL != nil && strings.TrimSpace(*cfg.BaseURL) != "" {
				if _, err := openRouterEndpoint(*cfg.BaseURL); err != nil {
					jsonResp(w, http.StatusBadRequest, map[string]string{"error": channel + " 通道 Base URL 无效: " + err.Error()})
					return
				}
			}
		}
		if input.Reply != nil {
			if err := saveModelChannel(replyChannel, *input.Reply); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		if input.Grok != nil {
			if err := saveModelChannel(grokChannel, *input.Grok); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		if input.RhysMemory != nil {
			if err := saveModelChannel(rhysMemoryChannel, *input.RhysMemory); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		if input.GrokMemory != nil {
			if err := saveModelChannel(grokMemoryChannel, *input.GrokMemory); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		_, _ = db.DB.Exec(`UPDATE global_memory_candidates SET status='pending',attempts=0,last_error='',next_attempt_at=CURRENT_TIMESTAMP,failed_at=NULL WHERE status='pending'`)
		signalMemoryWorker()
		jsonResp(w, http.StatusOK, map[string]interface{}{"channels": modelChannelViews()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
