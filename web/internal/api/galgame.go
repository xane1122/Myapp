package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"myapp/internal/db"
)

type galgameStoryInput struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	CoverImage   string `json:"cover_image"`
	StartNodeKey string `json:"start_node_key"`
}

type galgameNodeInput struct {
	NodeKey    string          `json:"node_key"`
	Content    string          `json:"content"`
	Options    json.RawMessage `json:"options"`
	IsEnding   bool            `json:"is_ending"`
	EndingName string          `json:"ending_name"`
}

type galgameNodeImport struct {
	Nodes []galgameNodeInput `json:"nodes"`
}

type galgameSaveInput struct {
	NodeKey string          `json:"node_key"`
	Flags   json.RawMessage `json:"flags"`
}

type galgameOption struct {
	ID          int             `json:"id"`
	Label       string          `json:"label"`
	NextNodeKey string          `json:"next_node_key"`
	SetFlags    json.RawMessage `json:"set_flags"`
}

func handleGalgame(w http.ResponseWriter, r *http.Request) {
	parts := splitGalgamePath(r.URL.Path)
	if len(parts) == 1 && parts[0] == "stories" {
		switch r.Method {
		case http.MethodGet:
			listGalgameStories(w)
		case http.MethodPost:
			createGalgameStory(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if len(parts) < 2 || parts[0] != "stories" {
		http.NotFound(w, r)
		return
	}
	storyID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || storyID <= 0 {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPut {
		updateGalgameStory(w, r, storyID)
		return
	}
	if len(parts) == 3 && parts[2] == "play" && r.Method == http.MethodGet {
		writeGalgameNode(w, storyID, "", map[string]any{})
		return
	}
	if len(parts) == 3 && parts[2] == "choose" && r.Method == http.MethodPost {
		chooseGalgameNode(w, r, storyID)
		return
	}
	if len(parts) == 4 && parts[2] == "nodes" && parts[3] == "import" && r.Method == http.MethodPost {
		importGalgameNodes(w, r, storyID)
		return
	}
	if len(parts) == 3 && parts[2] == "saves" {
		switch r.Method {
		case http.MethodGet:
			listGalgameSaves(w, storyID)
		case http.MethodPost:
			createGalgameSave(w, r, storyID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if len(parts) == 4 && parts[2] == "saves" && r.Method == http.MethodGet {
		saveID, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || saveID <= 0 {
			http.NotFound(w, r)
			return
		}
		loadGalgameSave(w, storyID, saveID)
		return
	}
	if len(parts) == 3 && parts[2] == "endings" && r.Method == http.MethodGet {
		listGalgameEndings(w, storyID)
		return
	}
	http.NotFound(w, r)
}

func splitGalgamePath(path string) []string {
	value := strings.Trim(strings.TrimPrefix(path, "/api/galgame/"), "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func decodeGalgame(r *http.Request, dest any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dest)
}

func validGalgameStory(in galgameStoryInput) (galgameStoryInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.CoverImage = strings.TrimSpace(in.CoverImage)
	in.StartNodeKey = strings.TrimSpace(in.StartNodeKey)
	if in.StartNodeKey == "" {
		in.StartNodeKey = "start"
	}
	if in.Title == "" || len([]rune(in.Title)) > 100 || len([]rune(in.Description)) > 1000 || len(in.CoverImage) > 2000 || len(in.StartNodeKey) > 80 {
		return in, errBadGalgameInput
	}
	return in, nil
}

var errBadGalgameInput = &galgameInputError{}

type galgameInputError struct{}

func (*galgameInputError) Error() string { return "故事参数无效" }

func listGalgameStories(w http.ResponseWriter) {
	rows, err := db.DB.Query(`SELECT s.id,s.title,s.description,s.cover_image,s.start_node_key,s.created_at,s.updated_at,
		(SELECT COUNT(*) FROM galgame_nodes n WHERE n.story_id=s.id AND n.is_ending=1),
		(SELECT COUNT(DISTINCT sv.node_key) FROM galgame_saves sv JOIN galgame_nodes n ON n.story_id=sv.story_id AND n.node_key=sv.node_key WHERE sv.story_id=s.id AND n.is_ending=1)
		FROM galgame_stories s ORDER BY s.updated_at DESC,s.id DESC`)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取故事失败"})
		return
	}
	defer rows.Close()
	stories := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var title, description, cover, start, created, updated string
		var total, unlocked int
		if err := rows.Scan(&id, &title, &description, &cover, &start, &created, &updated, &total, &unlocked); err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取故事失败"})
			return
		}
		stories = append(stories, map[string]any{"id": id, "title": title, "description": description, "cover_image": cover, "start_node_key": start, "created_at": created, "updated_at": updated, "total_endings": total, "unlocked_endings": unlocked})
	}
	jsonResp(w, 200, map[string]any{"stories": stories})
}

func createGalgameStory(w http.ResponseWriter, r *http.Request) {
	var in galgameStoryInput
	if err := decodeGalgame(r, &in); err != nil {
		jsonResp(w, 400, map[string]string{"error": "故事参数无效"})
		return
	}
	in, err := validGalgameStory(in)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	result, err := db.DB.Exec(`INSERT INTO galgame_stories(title,description,cover_image,start_node_key) VALUES(?,?,?,?)`, in.Title, in.Description, in.CoverImage, in.StartNodeKey)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "创建故事失败"})
		return
	}
	id, _ := result.LastInsertId()
	_, err = db.DB.Exec(`INSERT INTO galgame_nodes(story_id,node_key,content) VALUES(?,?,?)`, id, in.StartNodeKey, "故事尚未续写")
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "创建起始节点失败"})
		return
	}
	jsonResp(w, 201, map[string]any{"id": id, "title": in.Title, "description": in.Description, "cover_image": in.CoverImage, "start_node_key": in.StartNodeKey})
}

func updateGalgameStory(w http.ResponseWriter, r *http.Request, id int64) {
	var in galgameStoryInput
	if err := decodeGalgame(r, &in); err != nil {
		jsonResp(w, 400, map[string]string{"error": "故事参数无效"})
		return
	}
	in, err := validGalgameStory(in)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}
	result, err := db.DB.Exec(`UPDATE galgame_stories SET title=?,description=?,cover_image=?,start_node_key=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, in.Title, in.Description, in.CoverImage, in.StartNodeKey, id)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "保存故事失败"})
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		http.NotFound(w, r)
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func importGalgameNodes(w http.ResponseWriter, r *http.Request, storyID int64) {
	var in galgameNodeImport
	if err := decodeGalgame(r, &in); err != nil || len(in.Nodes) == 0 || len(in.Nodes) > 500 {
		jsonResp(w, 400, map[string]string{"error": "节点数据无效"})
		return
	}
	tx, err := db.DB.Begin()
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "保存节点失败"})
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM galgame_nodes WHERE story_id=?`, storyID); err != nil {
		jsonResp(w, 500, map[string]string{"error": "保存节点失败"})
		return
	}
	seen := map[string]bool{}
	for _, node := range in.Nodes {
		node.NodeKey = strings.TrimSpace(node.NodeKey)
		node.Content = strings.TrimSpace(node.Content)
		node.EndingName = strings.TrimSpace(node.EndingName)
		if node.NodeKey == "" || len(node.NodeKey) > 80 || node.Content == "" || len([]rune(node.Content)) > 20000 || seen[node.NodeKey] {
			jsonResp(w, 400, map[string]string{"error": "节点数据无效"})
			return
		}
		seen[node.NodeKey] = true
		if !json.Valid(node.Options) {
			node.Options = []byte("[]")
		}
		if _, err = tx.Exec(`INSERT INTO galgame_nodes(story_id,node_key,content,options_json,is_ending,ending_name) VALUES(?,?,?,?,?,?)`, storyID, node.NodeKey, node.Content, string(node.Options), boolToInt(node.IsEnding), node.EndingName); err != nil {
			jsonResp(w, 500, map[string]string{"error": "保存节点失败"})
			return
		}
	}
	if err = tx.Commit(); err != nil {
		jsonResp(w, 500, map[string]string{"error": "保存节点失败"})
		return
	}
	jsonResp(w, 200, map[string]bool{"ok": true})
}

func writeGalgameNode(w http.ResponseWriter, storyID int64, nodeKey string, flags map[string]any) {
	if nodeKey == "" {
		if err := db.DB.QueryRow(`SELECT start_node_key FROM galgame_stories WHERE id=?`, storyID).Scan(&nodeKey); err != nil {
			http.NotFound(w, nil)
			return
		}
	}
	var key, content, options, endingName string
	var ending int
	if err := db.DB.QueryRow(`SELECT node_key,content,options_json,is_ending,ending_name FROM galgame_nodes WHERE story_id=? AND node_key=?`, storyID, nodeKey).Scan(&key, &content, &options, &ending, &endingName); err != nil {
		if err == sql.ErrNoRows {
			jsonResp(w, 404, map[string]string{"error": "故事节点不存在"})
		} else {
			jsonResp(w, 500, map[string]string{"error": "读取故事失败"})
		}
		return
	}
	var parsed any = []any{}
	_ = json.Unmarshal([]byte(options), &parsed)
	jsonResp(w, 200, map[string]any{"node": map[string]any{"node_key": key, "content": content, "options": parsed, "is_ending": ending == 1, "ending_name": endingName}, "flags": flags})
}

func chooseGalgameNode(w http.ResponseWriter, r *http.Request, storyID int64) {
	var in struct {
		NodeKey  string         `json:"node_key"`
		OptionID int            `json:"option_id"`
		Flags    map[string]any `json:"flags"`
	}
	if err := decodeGalgame(r, &in); err != nil || strings.TrimSpace(in.NodeKey) == "" {
		jsonResp(w, 400, map[string]string{"error": "选项参数无效"})
		return
	}
	var raw string
	if err := db.DB.QueryRow(`SELECT options_json FROM galgame_nodes WHERE story_id=? AND node_key=?`, storyID, in.NodeKey).Scan(&raw); err != nil {
		jsonResp(w, 404, map[string]string{"error": "故事节点不存在"})
		return
	}
	var options []galgameOption
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		jsonResp(w, 400, map[string]string{"error": "故事选项无效"})
		return
	}
	for _, option := range options {
		if option.ID == in.OptionID && strings.TrimSpace(option.NextNodeKey) != "" {
			if len(option.SetFlags) > 0 {
				var changed map[string]any
				if json.Unmarshal(option.SetFlags, &changed) == nil {
					if in.Flags == nil {
						in.Flags = map[string]any{}
					}
					for k, v := range changed {
						in.Flags[k] = v
					}
				}
			}
			writeGalgameNode(w, storyID, option.NextNodeKey, in.Flags)
			return
		}
	}
	jsonResp(w, 400, map[string]string{"error": "故事选项不存在"})
}

func createGalgameSave(w http.ResponseWriter, r *http.Request, storyID int64) {
	var in galgameSaveInput
	if err := decodeGalgame(r, &in); err != nil || strings.TrimSpace(in.NodeKey) == "" {
		jsonResp(w, 400, map[string]string{"error": "存档参数无效"})
		return
	}
	if !json.Valid(in.Flags) {
		in.Flags = []byte("{}")
	}
	result, err := db.DB.Exec(`INSERT INTO galgame_saves(story_id,node_key,flags_json) VALUES(?,?,?)`, storyID, strings.TrimSpace(in.NodeKey), string(in.Flags))
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "保存存档失败"})
		return
	}
	id, _ := result.LastInsertId()
	jsonResp(w, 201, map[string]any{"id": id, "ok": true})
}

func listGalgameSaves(w http.ResponseWriter, storyID int64) {
	rows, err := db.DB.Query(`SELECT id,node_key,flags_json,is_auto,created_at FROM galgame_saves WHERE story_id=? ORDER BY created_at DESC,id DESC`, storyID)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取存档失败"})
		return
	}
	defer rows.Close()
	saves := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var key, flags, created string
		var auto int
		if err := rows.Scan(&id, &key, &flags, &auto, &created); err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取存档失败"})
			return
		}
		saves = append(saves, map[string]any{"id": id, "node_key": key, "is_auto": auto == 1, "created_at": created})
	}
	jsonResp(w, 200, map[string]any{"saves": saves})
}

func loadGalgameSave(w http.ResponseWriter, storyID, saveID int64) {
	var key, raw string
	if err := db.DB.QueryRow(`SELECT node_key,flags_json FROM galgame_saves WHERE id=? AND story_id=?`, saveID, storyID).Scan(&key, &raw); err != nil {
		http.NotFound(w, nil)
		return
	}
	flags := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &flags)
	writeGalgameNode(w, storyID, key, flags)
}

func listGalgameEndings(w http.ResponseWriter, storyID int64) {
	rows, err := db.DB.Query(`SELECT n.node_key,n.ending_name,MAX(s.created_at) FROM galgame_nodes n LEFT JOIN galgame_saves s ON s.story_id=n.story_id AND s.node_key=n.node_key WHERE n.story_id=? AND n.is_ending=1 GROUP BY n.node_key,n.ending_name ORDER BY n.id`, storyID)
	if err != nil {
		jsonResp(w, 500, map[string]string{"error": "读取结局失败"})
		return
	}
	defer rows.Close()
	endings := make([]map[string]any, 0)
	for rows.Next() {
		var key, name string
		var unlocked sql.NullString
		if err := rows.Scan(&key, &name, &unlocked); err != nil {
			jsonResp(w, 500, map[string]string{"error": "读取结局失败"})
			return
		}
		endings = append(endings, map[string]any{"ending_node_key": key, "ending_name": name, "unlocked": unlocked.Valid, "unlocked_at": unlocked.String})
	}
	jsonResp(w, 200, map[string]any{"endings": endings})
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
