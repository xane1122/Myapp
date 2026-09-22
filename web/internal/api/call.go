package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxCallAudioBytes = 25 << 20

func handleCallConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	providers := callASR.Available()
	jsonResp(w, http.StatusOK, map[string]interface{}{
		"asr_providers": providers,
		"tts_provider":  edgeTTS.Provider(),
		"ready":         len(providers) > 0,
	})
}

func handleCallTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCallAudioBytes)
	if err := r.ParseMultipartForm(maxCallAudioBytes); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "录音无效或超过 25MB"})
		return
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "没有收到录音"})
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, maxCallAudioBytes+1))
	if err != nil || len(audio) == 0 || len(audio) > maxCallAudioBytes {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "无法读取录音"})
		return
	}
	contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	text, provider, err := callASR.Transcribe(ctx, header.Filename, contentType, audio)
	if err != nil {
		jsonResp(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("语音识别失败: %v", err)})
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"text": text, "provider": provider})
}
