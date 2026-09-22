package api

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"myapp/internal/memory"
)

const albumStorageLimit = 500 << 20

const (
	maxAlbumUploadBytes = 20 << 20
	maxAlbumImageSide   = 4096
	maxAlbumImagePixels = 16_000_000
)

var albumSaveMu sync.Mutex

func handleAlbum(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		listAlbum(w, r)
	case http.MethodPost:
		uploadAlbum(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
func handleAlbumRoute(w http.ResponseWriter, r *http.Request) {
	part := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/album/"), "/")
	if part == "stats" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		albumStats(w)
		return
	}
	if part == "from-chat" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		albumFromChat(w, r)
		return
	}
	parts := strings.Split(part, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id < 1 {
		jsonResp(w, 400, map[string]string{"error": "id无效"})
		return
	}
	if len(parts) == 2 && parts[1] == "favorite" {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", 405)
			return
		}
		albumFavorite(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		albumDetail(w, id)
	case http.MethodDelete:
		albumDelete(w, r, id)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
func listAlbum(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	uploader := r.URL.Query().Get("uploader")
	if uploader != "" && uploader != "Xane" && uploader != "Rhys" {
		jsonResp(w, 400, map[string]string{"error": "uploader无效"})
		return
	}
	photos, total, used, err := memory.ListAlbumPhotos(page, per, r.URL.Query().Get("favorite") == "1", uploader)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if page < 1 {
		page = 1
	}
	if per < 1 || per > 100 {
		per = 50
	}
	jsonResp(w, 200, map[string]interface{}{"photos": photos, "total": total, "page": page, "per_page": per, "storage_used_bytes": used, "storage_limit_bytes": albumStorageLimit})
}
func albumDetail(w http.ResponseWriter, id int64) {
	p, err := memory.GetAlbumPhoto(id)
	if err == sql.ErrNoRows {
		jsonResp(w, 404, map[string]string{"error": "图片不存在"})
		return
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"photo": p})
}
func albumStats(w http.ResponseWriter) {
	total, used, by, err := memory.AlbumStats()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResp(w, 200, map[string]interface{}{"total_photos": total, "storage_used_bytes": used, "storage_limit_bytes": albumStorageLimit, "by_uploader": by})
}

func uploadAlbum(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAlbumUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(24 << 20); err != nil {
		jsonResp(w, 400, map[string]string{"error": "图片文件无效或超过 20MB"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": "请选择图片"})
		return
	}
	defer file.Close()
	caption := strings.TrimSpace(r.FormValue("caption"))
	if len([]rune(caption)) > 200 {
		jsonResp(w, 400, map[string]string{"error": "说明不能超过200字"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxAlbumUploadBytes+1))
	if err != nil || len(raw) > maxAlbumUploadBytes {
		jsonResp(w, 400, map[string]string{"error": "读取图片失败"})
		return
	}
	p, err := saveAlbumImage(raw, "Xane", caption, "upload", filepath.Base(header.Filename))
	if err != nil {
		albumError(w, err)
		return
	}
	jsonResp(w, 201, map[string]interface{}{"photo": p})
}
func albumFromChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttachmentID   int64  `json:"attachment_id"`
		Caption        string `json:"caption"`
		ConversationID int64  `json:"conversation_id"`
	}
	if decodeAlbumJSON(w, r, &req) != nil || req.AttachmentID < 1 || req.ConversationID < 1 {
		jsonResp(w, 400, map[string]string{"error": "attachment_id和conversation_id必须有效"})
		return
	}
	a, err := memory.GetAttachment(req.AttachmentID)
	if err != nil {
		jsonResp(w, 404, map[string]string{"error": "聊天图片不存在"})
		return
	}
	if a.ConversationID != req.ConversationID {
		jsonResp(w, 403, map[string]string{"error": "图片不属于当前会话"})
		return
	}
	if !strings.HasPrefix(a.MimeType, "image/") {
		jsonResp(w, 400, map[string]string{"error": "只能存入图片附件"})
		return
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取聊天图片失败"})
		return
	}
	p, err := saveAlbumImage(raw, "Xane", req.Caption, "chat", strconv.FormatInt(a.ID, 10))
	if err != nil {
		albumError(w, err)
		return
	}
	jsonResp(w, 201, map[string]interface{}{"photo": p})
}
func albumFavorite(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		IsFavorite bool `json:"is_favorite"`
	}
	if decodeAlbumJSON(w, r, &req) != nil {
		jsonResp(w, 400, map[string]string{"error": "参数无效"})
		return
	}
	p, err := memory.SetAlbumPhotoFavorite(id, req.IsFavorite)
	if err != nil {
		albumError(w, err)
		return
	}
	jsonResp(w, 200, map[string]interface{}{"photo": p})
}
func albumDelete(w http.ResponseWriter, r *http.Request, id int64) {
	albumSaveMu.Lock()
	defer albumSaveMu.Unlock()
	p, err := memory.GetAlbumPhoto(id)
	if err == sql.ErrNoRows {
		jsonResp(w, 404, map[string]string{"error": "图片不存在"})
		return
	}
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	fullTrash, thumbTrash, err := stageAlbumDelete(p)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "删除图片文件失败"})
		return
	}
	if _, err = memory.DeleteAlbumPhoto(id, p.Uploader); err != nil {
		restoreAlbumDelete(fullTrash, p.FilePath)
		restoreAlbumDelete(thumbTrash, p.ThumbPath)
		albumError(w, err)
		return
	}
	if err := os.Remove(fullTrash); err != nil && !os.IsNotExist(err) {
		log.Printf("album: cleanup staged full image %q: %v", fullTrash, err)
	}
	if err := os.Remove(thumbTrash); err != nil && !os.IsNotExist(err) {
		log.Printf("album: cleanup staged thumbnail %q: %v", thumbTrash, err)
	}
	jsonResp(w, 200, map[string]string{"status": "ok"})
}
func albumError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		jsonResp(w, 404, map[string]string{"error": "图片不存在"})
		return
	}
	jsonResp(w, 400, map[string]string{"error": err.Error()})
}

func decodeAlbumJSON(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func saveAlbumImage(raw []byte, uploader, caption, sourceType, sourceID string) (memory.AlbumPhoto, error) {
	caption = strings.TrimSpace(caption)
	if len([]rune(caption)) > 200 {
		return memory.AlbumPhoto{}, fmt.Errorf("说明不能超过200字")
	}
	if uploader != "Xane" && uploader != "Rhys" {
		return memory.AlbumPhoto{}, fmt.Errorf("上传者无效")
	}
	if sourceType != "upload" && sourceType != "chat" && sourceType != "ai" {
		return memory.AlbumPhoto{}, fmt.Errorf("图片来源无效")
	}
	sourceID = truncateRunes(strings.TrimSpace(sourceID), 200)
	img, err := decodeAlbumImage(raw)
	if err != nil {
		return memory.AlbumPhoto{}, err
	}
	full := resizeImage(img, 1920, 1920, false)
	thumb := squareThumb(img, 200)
	var fullBuf, thumbBuf bytes.Buffer
	if err := jpeg.Encode(&fullBuf, full, &jpeg.Options{Quality: 83}); err != nil {
		return memory.AlbumPhoto{}, err
	}
	if err = jpeg.Encode(&thumbBuf, thumb, &jpeg.Options{Quality: 82}); err != nil {
		return memory.AlbumPhoto{}, err
	}
	albumSaveMu.Lock()
	defer albumSaveMu.Unlock()
	_, used, _, err := memory.AlbumStats()
	if err != nil {
		return memory.AlbumPhoto{}, err
	}
	if used+int64(fullBuf.Len()+thumbBuf.Len()) > albumStorageLimit {
		return memory.AlbumPhoto{}, fmt.Errorf("贴贴存储空间已满（上限500MB）")
	}
	stamp, err := randomAlbumName()
	if err != nil {
		return memory.AlbumPhoto{}, err
	}
	fullDir := filepath.Join(uploadRoot, "album", "full")
	thumbDir := filepath.Join(uploadRoot, "album", "thumb")
	if err = os.MkdirAll(fullDir, 0755); err != nil {
		return memory.AlbumPhoto{}, err
	}
	if err = os.MkdirAll(thumbDir, 0755); err != nil {
		return memory.AlbumPhoto{}, err
	}
	fullPath := filepath.Join(fullDir, stamp+".jpg")
	thumbPath := filepath.Join(thumbDir, stamp+".jpg")
	if err = writeAlbumFile(fullPath, fullBuf.Bytes()); err != nil {
		return memory.AlbumPhoto{}, err
	}
	if err = writeAlbumFile(thumbPath, thumbBuf.Bytes()); err != nil {
		_ = os.Remove(fullPath)
		return memory.AlbumPhoto{}, err
	}
	b := full.Bounds()
	p, err := memory.CreateAlbumPhoto(memory.AlbumPhoto{Uploader: uploader, FilePath: fullPath, ThumbPath: thumbPath, FileSize: int64(fullBuf.Len() + thumbBuf.Len()), Width: b.Dx(), Height: b.Dy(), MimeType: "image/jpeg", Caption: caption, SourceType: sourceType, SourceID: sourceID})
	if err != nil {
		_ = os.Remove(fullPath)
		_ = os.Remove(thumbPath)
	}
	return p, err
}

func decodeAlbumImage(raw []byte) (image.Image, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 1 || config.Height < 1 {
		return nil, fmt.Errorf("文件不是可用图片")
	}
	if config.Width > maxAlbumImageSide || config.Height > maxAlbumImageSide || int64(config.Width)*int64(config.Height) > maxAlbumImagePixels {
		return nil, fmt.Errorf("图片尺寸超过限制（最大4096px、1600万像素）")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("文件不是可用图片")
	}
	return img, nil
}

func randomAlbumName() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("生成图片名称失败: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func writeAlbumFile(path string, contents []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(contents)
	return err
}

func stageAlbumDelete(p memory.AlbumPhoto) (string, string, error) {
	if !albumPathAllowed(p.FilePath, "full") || !albumPathAllowed(p.ThumbPath, "thumb") {
		return "", "", fmt.Errorf("图片文件路径无效")
	}
	suffix, err := randomAlbumName()
	if err != nil {
		return "", "", err
	}
	stage := func(path string) (string, error) {
		staged := path + ".deleting-" + suffix
		if err := os.Rename(path, staged); err != nil {
			if os.IsNotExist(err) {
				return "", nil
			}
			return "", err
		}
		return staged, nil
	}
	fullTrash, err := stage(p.FilePath)
	if err != nil {
		return "", "", err
	}
	thumbTrash, err := stage(p.ThumbPath)
	if err != nil {
		restoreAlbumDelete(fullTrash, p.FilePath)
		return "", "", err
	}
	return fullTrash, thumbTrash, nil
}

func albumPathAllowed(path, kind string) bool {
	root, err := filepath.Abs(filepath.Join(uploadRoot, "album", kind))
	if err != nil {
		return false
	}
	candidate, err := filepath.Abs(filepath.Clean(path))
	if err != nil || filepath.Dir(candidate) != root {
		return false
	}
	name := filepath.Base(candidate)
	return name != "" && name != "." && strings.EqualFold(filepath.Ext(name), ".jpg")
}

func restoreAlbumDelete(stagedPath, originalPath string) {
	if stagedPath == "" {
		return
	}
	if err := os.Rename(stagedPath, originalPath); err != nil && !os.IsNotExist(err) {
		log.Printf("album: restore staged file %q: %v", stagedPath, err)
	}
}
func resizeImage(src image.Image, maxW, maxH int, fill bool) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := 1.0
	if fill {
		if w < h {
			scale = float64(maxW) / float64(w)
		} else {
			scale = float64(maxH) / float64(h)
		}
	} else if w > maxW || h > maxH {
		if float64(w)/float64(maxW) > float64(h)/float64(maxH) {
			scale = float64(maxW) / float64(w)
		} else {
			scale = float64(maxH) / float64(h)
		}
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
func squareThumb(src image.Image, size int) image.Image {
	scaled := resizeImage(src, size, size, true)
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	b := scaled.Bounds()
	x, y := (b.Dx()-size)/2, (b.Dy()-size)/2
	for dy := 0; dy < size; dy++ {
		for dx := 0; dx < size; dx++ {
			out.Set(dx, dy, scaled.At(x+dx, y+dy))
		}
	}
	return out
}

func runAlbumSaveTool(arguments string, conversationID int64) string {
	var args struct {
		AttachmentID int64  `json:"attachment_id"`
		Caption      string `json:"caption"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil || args.AttachmentID < 1 {
		return `{"error":"attachment_id无效"}`
	}
	a, err := memory.GetAttachment(args.AttachmentID)
	if err != nil || a.ConversationID != conversationID || !strings.HasPrefix(a.MimeType, "image/") {
		return `{"error":"只能保存当前会话中的图片附件"}`
	}
	raw, err := os.ReadFile(a.FilePath)
	if err != nil {
		return `{"error":"读取图片失败"}`
	}
	p, err := saveAlbumImage(raw, assistantNameForConversation(conversationID), args.Caption, "ai", strconv.FormatInt(a.ID, 10))
	if err != nil {
		raw, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(raw)
	}
	raw, _ = json.Marshal(map[string]interface{}{"ok": true, "photo": p})
	return string(raw)
}
