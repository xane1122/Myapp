package api

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"

	"myapp/internal/db"
)

func validLudoGameID(value string) bool {
	return len(value) >= 8 && len(value) <= 80
}

func handleLudoStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		GameID string `json:"game_id"`
	}
	if err := decodeTraining(w, r, &in); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	in.GameID = strings.TrimSpace(in.GameID)
	if !validLudoGameID(in.GameID) {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "棋局编号无效"})
		return
	}
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO ludo_games(game_id) VALUES(?)`, in.GameID); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "棋局创建失败"})
		return
	}
	var pos int
	var finished bool
	if err := db.DB.QueryRow(`SELECT player_pos,finished FROM ludo_games WHERE game_id=?`, in.GameID).Scan(&pos, &finished); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "棋局读取失败"})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"player_pos": pos, "finished": finished})
}

func ludoDestination(pos int) int {
	moves := map[int]int{4: 1, 6: -1, 10: 1, 11: -2, 13: -1, 17: 2, 20: -2, 21: -3, 24: 2, 28: -1, 29: -2, 32: 1, 37: -2, 38: -3, 41: 2, 45: -3, 48: -1, 49: 3}
	seen := map[int]bool{}
	for !seen[pos] {
		seen[pos] = true
		move, ok := moves[pos]
		if !ok {
			break
		}
		pos += move
		if pos < 1 {
			pos = 1
		}
		if pos > 51 {
			pos = 51
		}
	}
	return pos
}

func handleLudoRoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		GameID string `json:"game_id"`
	}
	if err := decodeTraining(w, r, &in); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	in.GameID = strings.TrimSpace(in.GameID)
	if !validLudoGameID(in.GameID) {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "棋局编号无效"})
		return
	}
	var random [1]byte
	if _, err := rand.Read(random[:]); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "骰子生成失败"})
		return
	}
	dice := int(random[0]%6) + 1
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "棋局更新失败"})
		return
	}
	defer tx.Rollback()
	var pos int
	var finished bool
	if err = tx.QueryRow(`SELECT player_pos,finished FROM ludo_games WHERE game_id=?`, in.GameID).Scan(&pos, &finished); err != nil {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "棋局不存在"})
		return
	}
	if finished {
		jsonResp(w, http.StatusConflict, map[string]string{"error": "棋局已经结束"})
		return
	}
	landing := pos + dice
	if landing > 51 {
		landing = 51
	}
	destination := ludoDestination(landing)
	finished = destination == 51
	if _, err = tx.Exec(`UPDATE ludo_games SET player_pos=?,finished=?,updated_at=CURRENT_TIMESTAMP WHERE game_id=?`, destination, finished, in.GameID); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "棋局更新失败"})
		return
	}
	if err = tx.Commit(); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "棋局更新失败"})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"dice": dice, "landing_pos": landing, "player_pos": destination, "finished": finished})
}

type ludoActionRequest struct {
	APIKey         string          `json:"api_key"`
	APIBaseURL     string          `json:"api_base_url"`
	Model          string          `json:"model"`
	Kind           string          `json:"kind"`
	ConversationID int64           `json:"conversation_id"`
	Context        json.RawMessage `json:"context"`
}

type ludoActionResponse struct {
	Dice     int    `json:"dice,omitempty"`
	Approved *bool  `json:"approved,omitempty"`
	Message  string `json:"message"`
}

func handleLudoAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in ludoActionRequest
	if err := decodeTraining(w, r, &in); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if in.Kind != "turn" && in.Kind != "review" && in.Kind != "message" {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "游戏动作无效"})
		return
	}
	if len(in.Context) == 0 || len(in.Context) > 16<<10 || !json.Valid(in.Context) {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "游戏上下文无效"})
		return
	}
	apiKey := resolveAPIKey(in.APIKey)
	if apiKey == "" && !hasCustomAPIBaseURL(in.APIBaseURL) {
		jsonResp(w, http.StatusUnauthorized, map[string]string{"error": "请先在设置中配置模型"})
		return
	}
	assistantName := assistantNameForConversation(in.ConversationID)
	prompt := `你是情侣飞行棋中的AI玩家` + assistantName + `。只根据给出的游戏状态行动，不扩展规则。必须只返回一个JSON对象，不要Markdown。` +
		`kind=turn时返回{"dice":1到6,"message":"一句简短自然的中文游戏发言"}；` +
		`kind=review时返回{"approved":true或false,"message":"简短审核回复"}；` +
		`kind=message时返回{"message":"一句简短自然的中文回应"}。` +
		`不得要求危险行为；涉及身体任务时强调自愿、可跳过和安全。`
	result, err := Call(apiKey, strings.TrimSpace(in.APIBaseURL), resolveModel(in.Model), []ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: "kind=" + in.Kind + "\ncontext=" + string(in.Context)},
	}, 180)
	if err != nil {
		jsonResp(w, http.StatusBadGateway, map[string]string{"error": "AI回合生成失败"})
		return
	}
	var out ludoActionResponse
	if err := json.Unmarshal([]byte(extractJSONObject(result)), &out); err != nil {
		jsonResp(w, http.StatusBadGateway, map[string]string{"error": "AI返回格式无效"})
		return
	}
	out.Message = strings.TrimSpace(out.Message)
	if len([]rune(out.Message)) > 120 {
		out.Message = string([]rune(out.Message)[:120])
	}
	if in.Kind == "turn" && (out.Dice < 1 || out.Dice > 6) {
		jsonResp(w, http.StatusBadGateway, map[string]string{"error": "AI骰子点数无效"})
		return
	}
	if in.Kind == "review" && out.Approved == nil {
		jsonResp(w, http.StatusBadGateway, map[string]string{"error": "AI审核结果无效"})
		return
	}
	if out.Message == "" {
		out.Message = "轮到我了。"
	}
	jsonResp(w, http.StatusOK, out)
}

func extractJSONObject(value string) string {
	start, end := strings.IndexByte(value, '{'), strings.LastIndexByte(value, '}')
	if start < 0 || end < start {
		return value
	}
	return value[start : end+1]
}

func handleLudoReward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		GameID string `json:"game_id"`
		TileID int    `json:"tile_id"`
	}
	if err := decodeTraining(w, r, &in); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	in.GameID = strings.TrimSpace(in.GameID)
	if !validLudoGameID(in.GameID) {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "奖励内容无效"})
		return
	}
	type reward struct {
		points int
		item   string
	}
	rewards := map[int]reward{8: {15, ""}, 16: {10, ""}, 25: {25, "红丝带"}, 33: {20, ""}, 42: {30, "黑色项圈"}, 50: {50, "胜利徽记"}}
	value, ok := rewards[in.TileID]
	if !ok {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "奖励格无效"})
		return
	}
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "奖励发放失败"})
		return
	}
	defer tx.Rollback()
	var playerPos int
	if err = tx.QueryRow(`SELECT player_pos FROM ludo_games WHERE game_id=?`, in.GameID).Scan(&playerPos); err != nil || playerPos != in.TileID {
		jsonResp(w, http.StatusConflict, map[string]string{"error": "尚未到达该奖励格"})
		return
	}
	if _, err = tx.Exec(`INSERT INTO ludo_reward_claims(game_id,tile_id,points,cabinet_item) VALUES(?,?,?,?)`, in.GameID, in.TileID, value.points, value.item); err != nil {
		jsonResp(w, http.StatusConflict, map[string]string{"error": "本局奖励已领取"})
		return
	}
	if value.points > 0 {
		if _, err = tx.Exec(`UPDATE training_profile SET total_points=total_points+?,daily_earned=daily_earned+? WHERE id=1`, value.points, value.points); err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "积分发放失败"})
			return
		}
	}
	if value.item != "" {
		if _, err = tx.Exec(`INSERT INTO cabinet_items(name,source) VALUES(?,?)`, value.item, "飞行棋奖励"); err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "柜子奖励发放失败"})
			return
		}
	}
	if err = tx.Commit(); err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": "奖励发放失败"})
		return
	}
	jsonResp(w, http.StatusOK, map[string]interface{}{"points": value.points, "cabinet_item": value.item})
}
