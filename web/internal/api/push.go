package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"myapp/internal/db"
	"myapp/internal/observability"
)

type pushSubscriptionReq struct {
	UserID   string `json:"user_id"`
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func pushSubscriptionUserID(userID, endpoint string) string {
	if userID = strings.TrimSpace(userID); userID != "" {
		return truncateRunes(userID, 128)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(endpoint)))
	return fmt.Sprintf("web-%x", sum[:12])
}

func ensureVAPIDKeys() (string, string, error) {
	var privateKey, publicKey string
	_ = db.DB.QueryRow(`SELECT value FROM wake_secrets WHERE key='vapid_private'`).Scan(&privateKey)
	_ = db.DB.QueryRow(`SELECT value FROM wake_secrets WHERE key='vapid_public'`).Scan(&publicKey)
	if privateKey != "" && publicKey != "" {
		return privateKey, publicKey, nil
	}
	var err error
	privateKey, publicKey, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{"vapid_private": privateKey, "vapid_public": publicKey} {
		if _, err = tx.Exec(`INSERT INTO wake_secrets(key,value,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, key, value); err != nil {
			return "", "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return privateKey, publicKey, nil
}

func handlePush(w http.ResponseWriter, r *http.Request) {
	_, publicKey, err := ensureVAPIDKeys()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		var count int
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM push_subscriptions`).Scan(&count)
		jsonResp(w, 200, map[string]interface{}{"public_key": publicKey, "subscriptions": count})
	case http.MethodPost:
		var req pushSubscriptionReq
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Endpoint) == "" || strings.TrimSpace(req.Keys.P256dh) == "" || strings.TrimSpace(req.Keys.Auth) == "" {
			jsonResp(w, 400, map[string]string{"error": "推送订阅无效"})
			return
		}
		userID := pushSubscriptionUserID(req.UserID, req.Endpoint)
		keysJSON, _ := json.Marshal(req.Keys)
		_, err = db.DB.Exec(`INSERT INTO push_subscriptions(user_id,endpoint,keys_json,p256dh,auth,user_agent,updated_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id,keys_json=excluded.keys_json,p256dh=excluded.p256dh,auth=excluded.auth,user_agent=excluded.user_agent,updated_at=CURRENT_TIMESTAMP`, userID, req.Endpoint, string(keysJSON), req.Keys.P256dh, req.Keys.Auth, truncateRunes(r.UserAgent(), 300))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]bool{"subscribed": true})
	case http.MethodDelete:
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Endpoint != "" {
			_, _ = db.DB.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, req.Endpoint)
		}
		jsonResp(w, 200, map[string]bool{"subscribed": false})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func shouldDeletePushSubscription(status int) bool {
	return status == http.StatusNotFound || status == http.StatusGone
}

func handlePushTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM push_subscriptions`).Scan(&count); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if count == 0 {
		jsonResp(w, http.StatusConflict, map[string]string{"error": "这台设备尚未建立推送订阅，请先开启手机推送"})
		return
	}
	messageID := time.Now().UnixMilli()
	assistantName := assistantNameForConversation(0)
	go sendWakePush(0, messageID, "推送测试成功。之后 "+assistantName+" 的自主消息会通过这里送达。", true)
	jsonResp(w, http.StatusAccepted, map[string]interface{}{"queued": true, "subscriptions": count})
}

func sendWakePush(conversationID, messageID int64, reply string, preview bool) {
	sendAssistantPushIfAway(conversationID, messageID, reply, preview, "wake", true)
}

const (
	assistantPushAwayDelay = 8 * time.Second
	appForegroundLease     = 7 * time.Second
)

func scheduleAssistantPushIfAway(conversationID, messageID int64, reply string, preview bool, source string, allowBark bool) {
	time.AfterFunc(assistantPushAwayDelay, func() {
		sendAssistantPushIfAway(conversationID, messageID, reply, preview, source, allowBark)
	})
}

func sendAssistantPushIfAway(conversationID, messageID int64, reply string, preview bool, source string, allowBark bool) {
	if appIsForeground(time.Now().UTC()) {
		observability.Event("push.skipped_foreground", map[string]interface{}{"source": source, "conversation_id": conversationID, "message_id": messageID})
		return
	}
	sendAssistantPush(conversationID, messageID, reply, preview, source, allowBark)
}

func sendAssistantPush(conversationID, messageID int64, reply string, preview bool, source string, allowBark bool) {
	if strings.TrimSpace(source) == "" {
		source = "message"
	}
	targetURL := fmt.Sprintf("/?conversation_id=%d&message_id=%d&notification_source=%s", conversationID, messageID, url.QueryEscape(source))
	assistantName := assistantNameForConversation(conversationID)
	if allowBark {
		sendBarkPush(assistantName, reply, source, targetURL)
	} else {
		observability.Event("bark.skipped_native_client", map[string]interface{}{"source": source, "conversation_id": conversationID, "message_id": messageID})
	}
	privateKey, publicKey, err := ensureVAPIDKeys()
	if err != nil {
		return
	}
	body := assistantName + " 给你发了一条消息"
	if preview {
		body = truncateRunes(strings.TrimSpace(reply), 120)
	}
	payload, _ := json.Marshal(map[string]interface{}{"title": assistantName, "body": body, "source": source, "conversation_id": conversationID, "message_id": messageID, "url": targetURL})
	rows, err := db.DB.Query(`SELECT endpoint,p256dh,auth FROM push_subscriptions`)
	if err != nil {
		return
	}
	defer rows.Close()
	type subRow struct{ endpoint, p256dh, auth string }
	subs := []subRow{}
	for rows.Next() {
		var s subRow
		if rows.Scan(&s.endpoint, &s.p256dh, &s.auth) == nil {
			subs = append(subs, s)
		}
	}
	if err = rows.Err(); err != nil {
		observability.Event("push.failed", map[string]interface{}{"error": err.Error(), "stage": "read_subscriptions"})
		return
	}
	rows.Close()
	for _, s := range subs {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		resp, sendErr := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: s.endpoint, Keys: webpush.Keys{P256dh: s.p256dh, Auth: s.auth}}, &webpush.Options{Subscriber: "https://xanelove.com", VAPIDPublicKey: publicKey, VAPIDPrivateKey: privateKey, TTL: 86400, Urgency: webpush.UrgencyHigh})
		cancel()
		if sendErr != nil {
			observability.Event("push.failed", map[string]interface{}{"error": sendErr.Error()})
			continue
		}
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if shouldDeletePushSubscription(resp.StatusCode) {
			if _, deleteErr := db.DB.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, s.endpoint); deleteErr != nil {
				observability.Event("push.failed", map[string]interface{}{"error": deleteErr.Error(), "stage": "delete_expired_subscription", "status": resp.StatusCode})
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			observability.Event("push.failed", map[string]interface{}{"status": resp.StatusCode, "response": truncateRunes(strings.TrimSpace(string(responseBody)), 500), "conversation_id": conversationID, "message_id": messageID})
			continue
		}
		observability.Event("push.sent", map[string]interface{}{"status": resp.StatusCode, "conversation_id": conversationID, "message_id": messageID})
	}
}

func usesMyAppBetaNotifications(r *http.Request) bool {
	return r != nil && strings.Contains(r.UserAgent(), "MyAppBeta/")
}

func appIsForeground(now time.Time) bool {
	var lastSeen, source string
	if err := db.DB.QueryRow(`SELECT COALESCE(last_seen_at,''),COALESCE(source,'') FROM wake_activity WHERE id=1`).Scan(&lastSeen, &source); err != nil {
		return false
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(source)), "_hidden") || strings.EqualFold(strings.TrimSpace(source), "hidden") {
		return false
	}
	seen := parseSQLiteTime(lastSeen)
	return !seen.IsZero() && now.Sub(seen) >= 0 && now.Sub(seen) < appForegroundLease
}

func sendBellPush(b dueBell) {
	messageID := time.Now().UnixMilli()
	body := generateBellNotificationText(b)
	sendBarkPush("铃铛", body, "bell", "/bell")
	privateKey, publicKey, err := ensureVAPIDKeys()
	if err != nil {
		return
	}
	payload, _ := json.Marshal(map[string]interface{}{"title": "铃铛", "body": truncateRunes(body, 120), "source": "bell", "message_id": messageID, "url": "/bell"})
	rows, err := db.DB.Query(`SELECT endpoint,p256dh,auth FROM push_subscriptions`)
	if err != nil {
		return
	}
	defer rows.Close()
	type subRow struct{ endpoint, p256dh, auth string }
	var subs []subRow
	for rows.Next() {
		var s subRow
		if rows.Scan(&s.endpoint, &s.p256dh, &s.auth) == nil {
			subs = append(subs, s)
		}
	}
	rows.Close()
	for _, s := range subs {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		resp, sendErr := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: s.endpoint, Keys: webpush.Keys{P256dh: s.p256dh, Auth: s.auth}}, &webpush.Options{Subscriber: "https://xanelove.com", VAPIDPublicKey: publicKey, VAPIDPrivateKey: privateKey, TTL: 3600, Urgency: webpush.UrgencyHigh})
		cancel()
		if sendErr != nil {
			observability.Event("bell.push_failed", map[string]interface{}{"bell_id": b.ID, "error": sendErr.Error()})
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if shouldDeletePushSubscription(resp.StatusCode) {
			_, _ = db.DB.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, s.endpoint)
		}
		observability.Event("bell.push_sent", map[string]interface{}{"bell_id": b.ID, "status": resp.StatusCode})
	}
}

func sendBarkPush(title, body, source, targetURL string) {
	barkKey := strings.TrimSpace(os.Getenv("BARK_KEY"))
	if barkKey == "" {
		return
	}
	body = truncateRunes(strings.TrimSpace(body), 100)
	if body == "" {
		body = firstNonEmpty(strings.TrimSpace(title), assistantNameForConversation(0)) + " 给你发了一条消息"
	}
	query := url.Values{"icon": {strings.TrimSpace(os.Getenv("BARK_ICON"))}, "group": {title}, "isArchive": {"1"}}
	if targetURL != "" {
		query.Set("url", "https://xanelove.com"+targetURL)
	}
	barkURL := fmt.Sprintf("https://api.day.app/%s/%s/%s?%s", barkKey, url.PathEscape(title), url.PathEscape(body), query.Encode())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, barkURL, nil)
	if err != nil {
		observability.Event("bark.failed", map[string]interface{}{"error": err.Error(), "source": source})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		observability.Event("bark.failed", map[string]interface{}{"error": err.Error(), "source": source})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		observability.Event("bark.failed", map[string]interface{}{"status": resp.StatusCode, "response": truncateRunes(strings.TrimSpace(string(responseBody)), 500), "source": source})
		return
	}
	observability.Event("bark.sent", map[string]interface{}{"status": resp.StatusCode, "source": source})
}
