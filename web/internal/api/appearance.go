package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	appearancePath          = "data/appearance.json"
	maxAppearanceRequest    = 512 << 10
	maxAppearanceImageBytes = 320 << 10
	maxAppearanceImageSide  = 4096
)

var (
	appearanceMu        sync.Mutex
	appearanceColorExpr = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	appearanceTargets   = map[string]struct{}{
		"all": {}, "home": {}, "chat": {}, "context": {}, "catroom": {}, "bell": {},
		"album": {}, "games": {}, "ledger": {}, "notes": {}, "roleplay": {}, "training": {},
		"daily-log": {}, "notebook": {}, "days-matter": {}, "mailbox": {}, "diary": {}, "settings": {},
	}
)

type appearanceValue struct {
	Image     string  `json:"image,omitempty"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Zoom      float64 `json:"zoom"`
	IconColor string  `json:"iconColor,omitempty"`
	Deleted   bool    `json:"deleted,omitempty"`
}

type appearanceState struct {
	Revision  uint64                     `json:"revision"`
	UpdatedAt string                     `json:"updated_at,omitempty"`
	Targets   map[string]appearanceValue `json:"targets"`
}

type appearanceUpdate struct {
	Target     string          `json:"target"`
	Appearance appearanceValue `json:"appearance"`
}

func handleAppearance(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		getAppearance(w)
	case http.MethodPut:
		putAppearance(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func getAppearance(w http.ResponseWriter) {
	appearanceMu.Lock()
	defer appearanceMu.Unlock()
	state, err := readAppearanceState()
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "读取外观设置失败"})
		return
	}
	writeAppearanceResponse(w, http.StatusOK, state)
}

func putAppearance(w http.ResponseWriter, r *http.Request) {
	match := strings.TrimSpace(r.Header.Get("If-Match"))
	if match == "" {
		jsonResp(w, http.StatusPreconditionRequired, map[string]string{"error": "缺少外观版本"})
		return
	}
	wanted, err := parseAppearanceETag(match)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "外观版本无效"})
		return
	}
	var update appearanceUpdate
	if err = decodeAppearanceJSON(w, r, &update); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, ok := appearanceTargets[update.Target]; !ok {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "页面目标无效"})
		return
	}
	if err = validateAppearance(update.Appearance); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	appearanceMu.Lock()
	defer appearanceMu.Unlock()
	state, err := readAppearanceState()
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "读取外观设置失败"})
		return
	}
	if state.Revision != wanted {
		writeAppearanceResponse(w, http.StatusConflict, state)
		return
	}
	if state.Targets == nil {
		state.Targets = make(map[string]appearanceValue)
	}
	state.Targets[update.Target] = update.Appearance
	state.Revision++
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = writeAppearanceState(state); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "保存外观设置失败"})
		return
	}
	writeAppearanceResponse(w, http.StatusOK, state)
}

func decodeAppearanceJSON(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAppearanceRequest)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			return errors.New("壁纸数据过大")
		}
		return errors.New("外观数据无效")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func validateAppearance(value appearanceValue) error {
	if value.Deleted {
		if value.Image != "" || value.IconColor != "" || value.X != 0 || value.Y != 0 || value.Zoom != 0 {
			return errors.New("恢复默认不能同时包含外观数据")
		}
		return nil
	}
	if value.X < 0 || value.X > 100 || value.Y < 0 || value.Y > 100 || value.Zoom < 1 || value.Zoom > 2 {
		return errors.New("壁纸位置或缩放无效")
	}
	if value.IconColor != "" && !appearanceColorExpr.MatchString(value.IconColor) {
		return errors.New("图标颜色无效")
	}
	if value.Image == "" {
		return nil
	}
	const prefix = "data:image/jpeg;base64,"
	if !strings.HasPrefix(value.Image, prefix) {
		return errors.New("壁纸必须是处理后的 JPEG 图片")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value.Image, prefix))
	if err != nil || len(raw) == 0 || len(raw) > maxAppearanceImageBytes {
		return errors.New("壁纸图片无效或过大")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "jpeg" || config.Width < 1 || config.Height < 1 || config.Width > maxAppearanceImageSide || config.Height > maxAppearanceImageSide || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return errors.New("壁纸图片尺寸无效")
	}
	return nil
}

func readAppearanceState() (appearanceState, error) {
	state := appearanceState{Targets: make(map[string]appearanceValue)}
	raw, err := os.ReadFile(appearancePath)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		return appearanceState{}, err
	}
	if state.Targets == nil {
		state.Targets = make(map[string]appearanceValue)
	}
	return state, nil
}

func writeAppearanceState(state appearanceState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(appearancePath)
	if err = os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".appearance-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, appearancePath)
}

func appearanceETag(revision uint64) string {
	return fmt.Sprintf(`"appearance-%d"`, revision)
}

func parseAppearanceETag(value string) (uint64, error) {
	value = strings.Trim(value, `"`)
	if !strings.HasPrefix(value, "appearance-") {
		return 0, errors.New("invalid etag")
	}
	return strconv.ParseUint(strings.TrimPrefix(value, "appearance-"), 10, 64)
}

func writeAppearanceResponse(w http.ResponseWriter, status int, state appearanceState) {
	w.Header().Set("ETag", appearanceETag(state.Revision))
	w.Header().Set("Cache-Control", "private, no-store")
	jsonResp(w, status, state)
}
