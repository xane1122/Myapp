package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"myapp/internal/db"
	"myapp/internal/memory"
	"myapp/internal/observability"
)

const (
	defaultWorldBookScanDepth   = 4
	defaultWorldBookTokenBudget = 4096
	defaultWorldBookBudgetPct   = 15
	defaultWorldBookRecursion   = 2
)

type worldBookEntry struct {
	ID                int64  `json:"id"`
	EntryName         string `json:"entry_name"`
	Keywords          string `json:"keywords"`
	SecondaryKeywords string `json:"secondary_keywords"`
	Content           string `json:"content"`
	InjectionPosition string `json:"injection_position"`
	Depth             int    `json:"depth"`
	Priority          int    `json:"priority"`
	TokenBudget       int    `json:"token_budget"`
	EntryType         string `json:"entry_type"`
	MatchWholeWords   bool   `json:"match_whole_words"`
	SecondaryLogic    string `json:"secondary_logic"`
	StickyRounds      int    `json:"sticky_rounds"`
	CooldownRounds    int    `json:"cooldown_rounds"`
	GroupName         string `json:"group_name"`
	GroupCompetition  bool   `json:"group_competition"`
	ExcludeRecursion  bool   `json:"exclude_recursion"`
	PreventRecursion  bool   `json:"prevent_recursion"`
	Enabled           bool   `json:"enabled"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type worldBookConfig struct {
	ScanDepth     int    `json:"world_book_scan_depth"`
	TokenBudget   int    `json:"world_book_token_budget"`
	CaseSensitive bool   `json:"world_book_case_sensitive"`
	BudgetMode    string `json:"world_book_budget_mode"`
	BudgetPercent int    `json:"world_book_budget_percent"`
	Recursive     bool   `json:"world_book_recursive"`
	MaxRecursion  int    `json:"world_book_max_recursion"`
}

var validWorldBookEntryTypes = map[string]bool{"keyword": true, "constant": true}
var validWorldBookSecondaryLogic = map[string]bool{"and_all": true, "or_any": true, "not_any": true, "not_all": true}

var validWorldBookPositions = map[string]bool{
	"after_system_prompt": true,
	"before_user_message": true,
	"before_last_message": true,
	"author_note_depth":   true,
}

func handleWorldBook(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries, err := listWorldBookEntries(false)
		if err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, http.StatusOK, map[string]interface{}{"entries": entries})
	case http.MethodPost:
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
			return
		}
		var entry worldBookEntry
		var flags struct {
			Enabled          *bool `json:"enabled"`
			MatchWholeWords  *bool `json:"match_whole_words"`
			ExcludeRecursion *bool `json:"exclude_recursion"`
			PreventRecursion *bool `json:"prevent_recursion"`
		}
		if json.Unmarshal(raw, &entry) != nil || json.Unmarshal(raw, &flags) != nil {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
			return
		}
		entry.Enabled = flags.Enabled == nil || *flags.Enabled
		entry.MatchWholeWords = flags.MatchWholeWords == nil || *flags.MatchWholeWords
		entry.ExcludeRecursion = flags.ExcludeRecursion == nil || *flags.ExcludeRecursion
		entry.PreventRecursion = flags.PreventRecursion == nil || *flags.PreventRecursion
		entry, err := cleanWorldBookEntry(entry)
		if err != nil {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		result, err := db.DB.Exec(`INSERT INTO world_book_entries
			(entry_name,keywords,secondary_keywords,content,injection_position,depth,priority,token_budget,entry_type,
			 match_whole_words,secondary_logic,sticky_rounds,cooldown_rounds,group_name,group_competition,
			 exclude_recursion,prevent_recursion,enabled)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, entry.EntryName, entry.Keywords, entry.SecondaryKeywords, entry.Content,
			entry.InjectionPosition, entry.Depth, entry.Priority, entry.TokenBudget, entry.EntryType, entry.MatchWholeWords,
			entry.SecondaryLogic, entry.StickyRounds, entry.CooldownRounds, entry.GroupName, entry.GroupCompetition,
			entry.ExcludeRecursion, entry.PreventRecursion, entry.Enabled)
		if err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		entry.ID, _ = result.LastInsertId()
		created, _ := getWorldBookEntry(entry.ID)
		jsonResp(w, http.StatusCreated, map[string]interface{}{"entry": created})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleWorldBookRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/world-book/"), "/")
	if path == "config" {
		handleWorldBookConfig(w, r)
		return
	}
	if path == "preview" {
		handleWorldBookPreview(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "id无效"})
		return
	}
	if len(parts) == 2 && parts[1] == "toggle" {
		if r.Method != http.MethodPatch {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		toggleWorldBookEntry(w, id)
		return
	}
	if len(parts) != 1 {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "接口不存在"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		updateWorldBookEntry(w, r, id)
	case http.MethodDelete:
		result, err := db.DB.Exec(`DELETE FROM world_book_entries WHERE id=?`, id)
		if err != nil {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if n, _ := result.RowsAffected(); n == 0 {
			jsonResp(w, http.StatusNotFound, map[string]string{"error": "世界书条目不存在"})
			return
		}
		jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func updateWorldBookEntry(w http.ResponseWriter, r *http.Request, id int64) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
		return
	}
	var entry worldBookEntry
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil || json.Unmarshal(raw, &fields) != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "请求内容无效"})
		return
	}
	existing, err := getWorldBookEntry(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonResp(w, http.StatusNotFound, map[string]string{"error": "世界书条目不存在"})
		} else {
			jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	preserveMissingWorldBookAdvancedFields(&entry, existing, fields)
	entry, err = cleanWorldBookEntry(entry)
	if err != nil {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := db.DB.Exec(`UPDATE world_book_entries SET entry_name=?,keywords=?,secondary_keywords=?,content=?,
		injection_position=?,depth=?,priority=?,token_budget=?,entry_type=?,match_whole_words=?,secondary_logic=?,
		sticky_rounds=?,cooldown_rounds=?,group_name=?,group_competition=?,exclude_recursion=?,prevent_recursion=?,
		enabled=?,updated_at=datetime('now') WHERE id=?`,
		entry.EntryName, entry.Keywords, entry.SecondaryKeywords, entry.Content, entry.InjectionPosition,
		entry.Depth, entry.Priority, entry.TokenBudget, entry.EntryType, entry.MatchWholeWords, entry.SecondaryLogic,
		entry.StickyRounds, entry.CooldownRounds, entry.GroupName, entry.GroupCompetition, entry.ExcludeRecursion,
		entry.PreventRecursion, entry.Enabled, id)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "世界书条目不存在"})
		return
	}
	updated, _ := getWorldBookEntry(id)
	jsonResp(w, http.StatusOK, map[string]interface{}{"entry": updated})
}

func preserveMissingWorldBookAdvancedFields(entry *worldBookEntry, existing worldBookEntry, fields map[string]json.RawMessage) {
	if _, ok := fields["entry_type"]; !ok {
		entry.EntryType = existing.EntryType
	}
	if _, ok := fields["match_whole_words"]; !ok {
		entry.MatchWholeWords = existing.MatchWholeWords
	}
	if _, ok := fields["secondary_logic"]; !ok {
		entry.SecondaryLogic = existing.SecondaryLogic
	}
	if _, ok := fields["sticky_rounds"]; !ok {
		entry.StickyRounds = existing.StickyRounds
	}
	if _, ok := fields["cooldown_rounds"]; !ok {
		entry.CooldownRounds = existing.CooldownRounds
	}
	if _, ok := fields["group_name"]; !ok {
		entry.GroupName = existing.GroupName
	}
	if _, ok := fields["group_competition"]; !ok {
		entry.GroupCompetition = existing.GroupCompetition
	}
	if _, ok := fields["exclude_recursion"]; !ok {
		entry.ExcludeRecursion = existing.ExcludeRecursion
	}
	if _, ok := fields["prevent_recursion"]; !ok {
		entry.PreventRecursion = existing.PreventRecursion
	}
	if _, ok := fields["enabled"]; !ok {
		entry.Enabled = existing.Enabled
	}
}

func toggleWorldBookEntry(w http.ResponseWriter, id int64) {
	result, err := db.DB.Exec(`UPDATE world_book_entries SET enabled=CASE enabled WHEN 1 THEN 0 ELSE 1 END,
		updated_at=datetime('now') WHERE id=?`, id)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		jsonResp(w, http.StatusNotFound, map[string]string{"error": "世界书条目不存在"})
		return
	}
	entry, _ := getWorldBookEntry(id)
	jsonResp(w, http.StatusOK, map[string]interface{}{"entry": entry})
}

func handleWorldBookConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResp(w, http.StatusOK, map[string]interface{}{"config": loadWorldBookConfig()})
	case http.MethodPut:
		var patch struct {
			ScanDepth     *int    `json:"world_book_scan_depth"`
			TokenBudget   *int    `json:"world_book_token_budget"`
			CaseSensitive *bool   `json:"world_book_case_sensitive"`
			BudgetMode    *string `json:"world_book_budget_mode"`
			BudgetPercent *int    `json:"world_book_budget_percent"`
			Recursive     *bool   `json:"world_book_recursive"`
			MaxRecursion  *int    `json:"world_book_max_recursion"`
		}
		if json.NewDecoder(r.Body).Decode(&patch) != nil {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "世界书全局配置无效"})
			return
		}
		cfg := loadWorldBookConfig()
		if patch.ScanDepth != nil {
			cfg.ScanDepth = *patch.ScanDepth
		}
		if patch.TokenBudget != nil {
			cfg.TokenBudget = *patch.TokenBudget
		}
		if patch.CaseSensitive != nil {
			cfg.CaseSensitive = *patch.CaseSensitive
		}
		if patch.BudgetMode != nil {
			cfg.BudgetMode = *patch.BudgetMode
		}
		if patch.BudgetPercent != nil {
			cfg.BudgetPercent = *patch.BudgetPercent
		}
		if patch.Recursive != nil {
			cfg.Recursive = *patch.Recursive
		}
		if patch.MaxRecursion != nil {
			cfg.MaxRecursion = *patch.MaxRecursion
		}
		if cfg.ScanDepth < 1 || cfg.ScanDepth > 20 ||
			cfg.TokenBudget < 1 || cfg.TokenBudget > 131072 || cfg.BudgetPercent < 1 || cfg.BudgetPercent > 100 ||
			(cfg.BudgetMode != "fixed" && cfg.BudgetMode != "percent") || cfg.MaxRecursion < 0 || cfg.MaxRecursion > 10 {
			jsonResp(w, http.StatusBadRequest, map[string]string{"error": "世界书全局配置无效"})
			return
		}
		prefs := map[string]string{
			"world_book_scan_depth": strconv.Itoa(cfg.ScanDepth), "world_book_token_budget": strconv.Itoa(cfg.TokenBudget),
			"world_book_case_sensitive": strconv.FormatBool(cfg.CaseSensitive), "world_book_budget_mode": cfg.BudgetMode,
			"world_book_budget_percent": strconv.Itoa(cfg.BudgetPercent), "world_book_recursive": strconv.FormatBool(cfg.Recursive),
			"world_book_max_recursion": strconv.Itoa(cfg.MaxRecursion),
		}
		for key, value := range prefs {
			if err := memory.SetPref(key, value); err != nil {
				jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		jsonResp(w, http.StatusOK, map[string]interface{}{"config": cfg})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleWorldBookPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Text) == "" {
		jsonResp(w, http.StatusBadRequest, map[string]string{"error": "请输入用于测试触发的文字"})
		return
	}
	entries, err := listWorldBookEntries(true)
	if err != nil {
		jsonResp(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	matched, report := evaluateWorldBookEntries(entries, input.Text, loadWorldBookConfig(), nil, 1)
	jsonResp(w, http.StatusOK, map[string]interface{}{"entry_ids": worldBookEntryIDs(matched), "report": report})
}

func cleanWorldBookEntry(entry worldBookEntry) (worldBookEntry, error) {
	entry.EntryName = strings.TrimSpace(entry.EntryName)
	entry.Keywords = normalizeKeywordList(entry.Keywords)
	entry.SecondaryKeywords = normalizeKeywordList(entry.SecondaryKeywords)
	entry.Content = strings.TrimSpace(entry.Content)
	entry.GroupName = strings.TrimSpace(entry.GroupName)
	if entry.EntryName == "" || utf8.RuneCountInString(entry.EntryName) > 100 {
		return entry, errors.New("条目名称须为 1 至 100 字")
	}
	if entry.EntryType == "" {
		entry.EntryType = "keyword"
	}
	if !validWorldBookEntryTypes[entry.EntryType] {
		return entry, errors.New("词条类型无效")
	}
	if entry.EntryType == "keyword" && entry.Keywords == "" {
		return entry, errors.New("至少需要一个主关键词")
	}
	if entry.SecondaryLogic == "" {
		entry.SecondaryLogic = "and_all"
	}
	if !validWorldBookSecondaryLogic[entry.SecondaryLogic] {
		return entry, errors.New("次关键词逻辑无效")
	}
	if entry.Content == "" {
		return entry, errors.New("注入正文不能为空")
	}
	if entry.InjectionPosition == "" {
		entry.InjectionPosition = "before_user_message"
	}
	if !validWorldBookPositions[entry.InjectionPosition] {
		return entry, errors.New("注入位置无效")
	}
	if entry.Depth < 0 || entry.Depth > 100 {
		return entry, errors.New("注入深度须为 0 至 100")
	}
	if entry.TokenBudget < 0 || entry.TokenBudget > 32768 {
		return entry, errors.New("单条 token 预算须为 0 至 32768")
	}
	if entry.StickyRounds < 0 || entry.StickyRounds > 1000 || entry.CooldownRounds < 0 || entry.CooldownRounds > 1000 {
		return entry, errors.New("黏性和冷却轮数须为 0 至 1000")
	}
	if utf8.RuneCountInString(entry.GroupName) > 100 {
		return entry, errors.New("分组名称不能超过 100 字")
	}
	return entry, nil
}

func normalizeKeywordList(value string) string {
	parts := splitKeywords(value)
	return strings.Join(parts, ",")
}

func splitKeywords(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '，' })
	result := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" && !seen[field] {
			seen[field] = true
			result = append(result, field)
		}
	}
	return result
}

func scanWorldBookEntry(scanner interface{ Scan(...interface{}) error }) (worldBookEntry, error) {
	var entry worldBookEntry
	var enabled, matchWholeWords, groupCompetition, excludeRecursion, preventRecursion int
	err := scanner.Scan(&entry.ID, &entry.EntryName, &entry.Keywords, &entry.SecondaryKeywords, &entry.Content,
		&entry.InjectionPosition, &entry.Depth, &entry.Priority, &entry.TokenBudget, &entry.EntryType, &matchWholeWords,
		&entry.SecondaryLogic, &entry.StickyRounds, &entry.CooldownRounds, &entry.GroupName, &groupCompetition,
		&excludeRecursion, &preventRecursion,
		&enabled, &entry.CreatedAt, &entry.UpdatedAt)
	entry.Enabled = enabled != 0
	entry.MatchWholeWords = matchWholeWords != 0
	entry.GroupCompetition = groupCompetition != 0
	entry.ExcludeRecursion = excludeRecursion != 0
	entry.PreventRecursion = preventRecursion != 0
	return entry, err
}

const worldBookColumns = `id,entry_name,keywords,secondary_keywords,content,injection_position,depth,priority,token_budget,
	entry_type,match_whole_words,secondary_logic,sticky_rounds,cooldown_rounds,group_name,group_competition,
	exclude_recursion,prevent_recursion,enabled,created_at,updated_at`

func listWorldBookEntries(enabledOnly bool) ([]worldBookEntry, error) {
	query := `SELECT ` + worldBookColumns + ` FROM world_book_entries`
	if enabledOnly {
		query += ` WHERE enabled=1`
	}
	query += ` ORDER BY CASE entry_type WHEN 'constant' THEN 0 ELSE 1 END,priority DESC,id ASC`
	rows, err := db.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []worldBookEntry{}
	for rows.Next() {
		entry, err := scanWorldBookEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func getWorldBookEntry(id int64) (worldBookEntry, error) {
	return scanWorldBookEntry(db.DB.QueryRow(`SELECT `+worldBookColumns+` FROM world_book_entries WHERE id=?`, id))
}

func runReadWorldBookTool(arguments string) string {
	var args struct {
		EntryID     int64  `json:"entry_id"`
		Query       string `json:"query"`
		EnabledOnly *bool  `json:"enabled_only"`
		Limit       int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error":"参数格式错误"}`
	}
	if args.EntryID > 0 {
		entry, err := getWorldBookEntry(args.EntryID)
		if errors.Is(err, sql.ErrNoRows) {
			return `{"error":"世界书词条不存在"}`
		}
		if err != nil {
			return worldBookToolError(err)
		}
		encoded, _ := json.Marshal(map[string]interface{}{"count": 1, "entries": []worldBookEntry{entry}})
		return string(encoded)
	}
	enabledOnly := true
	if args.EnabledOnly != nil {
		enabledOnly = *args.EnabledOnly
	}
	entries, err := listWorldBookEntries(enabledOnly)
	if err != nil {
		return worldBookToolError(err)
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	filtered := make([]worldBookEntry, 0, len(entries))
	for _, entry := range entries {
		haystack := strings.ToLower(strings.Join([]string{entry.EntryName, entry.Keywords, entry.SecondaryKeywords, entry.Content}, "\n"))
		if query == "" || strings.Contains(haystack, query) {
			filtered = append(filtered, entry)
		}
	}
	limit := args.Limit
	if limit < 1 || limit > 20 {
		limit = 10
	}
	total := len(filtered)
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	encoded, _ := json.Marshal(map[string]interface{}{"query": args.Query, "count": len(filtered), "total_matches": total, "entries": filtered})
	return string(encoded)
}

func worldBookToolError(err error) string {
	encoded, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(encoded)
}

func loadWorldBookConfig() worldBookConfig {
	scanDepth, err := strconv.Atoi(memory.GetPref("world_book_scan_depth", strconv.Itoa(defaultWorldBookScanDepth)))
	if err != nil || scanDepth < 1 || scanDepth > 20 {
		scanDepth = defaultWorldBookScanDepth
	}
	tokenBudget, err := strconv.Atoi(memory.GetPref("world_book_token_budget", strconv.Itoa(defaultWorldBookTokenBudget)))
	if err != nil || tokenBudget < 1 || tokenBudget > 131072 {
		tokenBudget = defaultWorldBookTokenBudget
	}
	budgetMode := memory.GetPref("world_book_budget_mode", "fixed")
	if budgetMode != "fixed" && budgetMode != "percent" {
		budgetMode = "fixed"
	}
	budgetPercent, err := strconv.Atoi(memory.GetPref("world_book_budget_percent", strconv.Itoa(defaultWorldBookBudgetPct)))
	if err != nil || budgetPercent < 1 || budgetPercent > 100 {
		budgetPercent = defaultWorldBookBudgetPct
	}
	maxRecursion, err := strconv.Atoi(memory.GetPref("world_book_max_recursion", strconv.Itoa(defaultWorldBookRecursion)))
	if err != nil || maxRecursion < 0 || maxRecursion > 10 {
		maxRecursion = defaultWorldBookRecursion
	}
	caseSensitive, _ := strconv.ParseBool(memory.GetPref("world_book_case_sensitive", "false"))
	recursive, err := strconv.ParseBool(memory.GetPref("world_book_recursive", "true"))
	if err != nil {
		recursive = true
	}
	return worldBookConfig{ScanDepth: scanDepth, TokenBudget: tokenBudget, CaseSensitive: caseSensitive,
		BudgetMode: budgetMode, BudgetPercent: budgetPercent, Recursive: recursive, MaxRecursion: maxRecursion}
}

type worldBookActivationState struct {
	StickyUntil   int
	CooldownUntil int
	LastActivated int
}

type worldBookCandidate struct {
	Entry    worldBookEntry
	Reason   string
	Keywords []string
	Path     []int64
	Depth    int
}

type worldBookMatchReport struct {
	ConversationID int64                    `json:"conversation_id"`
	Turn           int                      `json:"turn"`
	ScanDepth      int                      `json:"scan_depth"`
	BudgetMode     string                   `json:"budget_mode"`
	Budget         int                      `json:"budget_tokens"`
	Used           int                      `json:"used_tokens"`
	Scanned        []map[string]interface{} `json:"scanned_messages"`
	Matches        []map[string]interface{} `json:"matches"`
	Activated      []map[string]interface{} `json:"activated"`
	Dropped        []map[string]interface{} `json:"dropped"`
	RecursionPaths [][]int64                `json:"recursion_paths"`
}

func applyWorldBook(conversationID int64, messages []ChatMessage, userText string, hist []memory.Message) ([]ChatMessage, []worldBookEntry) {
	entries, err := listWorldBookEntries(true)
	if err != nil {
		observability.Event("world_book.match_report", map[string]interface{}{"conversation_id": conversationID, "error": err.Error()})
		return messages, nil
	}
	cfg := loadWorldBookConfig()
	turn := worldBookTurn(hist)
	states := loadWorldBookActivationStates(conversationID)
	matched, report := evaluateWorldBookEntries(entries, worldBookScanText(userText, hist, cfg.ScanDepth), cfg, states, turn)
	report.ConversationID = conversationID
	report.Scanned = worldBookScannedMessages(userText, hist, cfg.ScanDepth)
	persistWorldBookActivations(conversationID, turn, matched, report)
	observability.Event("world_book.match_report", map[string]interface{}{"report": report})
	if len(matched) == 0 {
		return messages, nil
	}
	return injectWorldBookEntries(messages, matched), matched
}

func worldBookTurn(hist []memory.Message) int {
	turn := 1
	for _, message := range hist {
		if strings.EqualFold(message.Role, "user") {
			turn++
		}
	}
	return turn
}

func worldBookScannedMessages(userText string, hist []memory.Message, depth int) []map[string]interface{} {
	start := len(hist) - depth
	if start < 0 {
		start = 0
	}
	items := make([]map[string]interface{}, 0, len(hist)-start+1)
	for _, message := range hist[start:] {
		items = append(items, map[string]interface{}{"id": message.ID, "role": message.Role, "chars": utf8.RuneCountInString(message.Content)})
	}
	items = append(items, map[string]interface{}{"id": 0, "role": "current_user", "chars": utf8.RuneCountInString(userText)})
	return items
}

func loadWorldBookActivationStates(conversationID int64) map[int64]worldBookActivationState {
	states := map[int64]worldBookActivationState{}
	rows, err := db.DB.Query(`SELECT entry_id,sticky_until_turn,cooldown_until_turn,last_activated_turn FROM world_book_activation_state WHERE conversation_id=?`, conversationID)
	if err != nil {
		return states
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var state worldBookActivationState
		if rows.Scan(&id, &state.StickyUntil, &state.CooldownUntil, &state.LastActivated) == nil {
			states[id] = state
		}
	}
	return states
}

func resolvedWorldBookBudget(cfg worldBookConfig) int {
	if cfg.BudgetMode == "percent" {
		return contextWindowEstimate * cfg.BudgetPercent / 100
	}
	return cfg.TokenBudget
}

func evaluateWorldBookEntries(entries []worldBookEntry, scanText string, cfg worldBookConfig, states map[int64]worldBookActivationState, turn int) ([]worldBookEntry, worldBookMatchReport) {
	report := worldBookMatchReport{Turn: turn, ScanDepth: cfg.ScanDepth, BudgetMode: cfg.BudgetMode, Budget: resolvedWorldBookBudget(cfg),
		Matches: []map[string]interface{}{}, Activated: []map[string]interface{}{}, Dropped: []map[string]interface{}{}, RecursionPaths: [][]int64{}}
	activated := map[int64]worldBookCandidate{}
	frontier := []worldBookCandidate{}
	for _, entry := range entries {
		if !entry.Enabled {
			continue
		}
		state := states[entry.ID]
		var candidate worldBookCandidate
		switch {
		case entry.EntryType == "constant":
			candidate = worldBookCandidate{Entry: entry, Reason: "constant", Path: []int64{entry.ID}}
		case state.StickyUntil >= turn:
			candidate = worldBookCandidate{Entry: entry, Reason: "sticky", Path: []int64{entry.ID}}
		case state.CooldownUntil >= turn && state.LastActivated < turn:
			report.Dropped = append(report.Dropped, worldBookReportItem(entry, "cooldown", nil, nil))
			continue
		default:
			keywords, ok := worldBookEntryMatches(entry, scanText, cfg.CaseSensitive)
			if !ok {
				continue
			}
			candidate = worldBookCandidate{Entry: entry, Reason: "keyword", Keywords: keywords, Path: []int64{entry.ID}}
			report.Matches = append(report.Matches, worldBookReportItem(entry, "keyword", keywords, candidate.Path))
		}
		activated[entry.ID] = candidate
		frontier = append(frontier, candidate)
	}
	if cfg.Recursive {
		for depth := 1; depth <= cfg.MaxRecursion && len(frontier) > 0; depth++ {
			next := []worldBookCandidate{}
			for _, parent := range frontier {
				if parent.Entry.PreventRecursion {
					continue
				}
				for _, entry := range entries {
					if !entry.Enabled || entry.EntryType == "constant" || entry.ExcludeRecursion {
						continue
					}
					if _, exists := activated[entry.ID]; exists {
						continue
					}
					state := states[entry.ID]
					if state.CooldownUntil >= turn && state.LastActivated < turn {
						continue
					}
					keywords, ok := worldBookEntryMatches(entry, parent.Entry.Content, cfg.CaseSensitive)
					if !ok {
						continue
					}
					path := append(append([]int64{}, parent.Path...), entry.ID)
					candidate := worldBookCandidate{Entry: entry, Reason: "recursive", Keywords: keywords, Path: path, Depth: depth}
					activated[entry.ID] = candidate
					next = append(next, candidate)
					report.Matches = append(report.Matches, worldBookReportItem(entry, "recursive", keywords, path))
					report.RecursionPaths = append(report.RecursionPaths, path)
				}
			}
			frontier = next
		}
	}
	candidates := make([]worldBookCandidate, 0, len(activated))
	for _, candidate := range activated {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if (candidates[i].Entry.EntryType == "constant") != (candidates[j].Entry.EntryType == "constant") {
			return candidates[i].Entry.EntryType == "constant"
		}
		if candidates[i].Entry.Priority != candidates[j].Entry.Priority {
			return candidates[i].Entry.Priority > candidates[j].Entry.Priority
		}
		return candidates[i].Entry.ID < candidates[j].Entry.ID
	})
	selectedGroups := map[string]int64{}
	selected := []worldBookEntry{}
	remaining := report.Budget
	for _, candidate := range candidates {
		entry := candidate.Entry
		if entry.GroupCompetition && entry.GroupName != "" {
			if winner, exists := selectedGroups[entry.GroupName]; exists {
				report.Dropped = append(report.Dropped, worldBookReportItem(entry, "group_competition", nil, []int64{winner}))
				continue
			}
			selectedGroups[entry.GroupName] = entry.ID
		}
		if entry.TokenBudget > 0 {
			var ok bool
			entry, ok = fitWorldBookEntry(entry, entry.TokenBudget)
			if !ok {
				report.Dropped = append(report.Dropped, worldBookReportItem(entry, "entry_budget", nil, nil))
				continue
			}
		}
		used := estimateWorldBookTokens(formatWorldBookEntry(entry))
		originalTokens := used
		truncated := false
		if used > remaining && remaining > 0 {
			var ok bool
			entry, ok = fitWorldBookEntry(entry, remaining)
			if ok {
				used = estimateWorldBookTokens(formatWorldBookEntry(entry))
				truncated = true
			}
		}
		if used > remaining || remaining <= 0 {
			report.Dropped = append(report.Dropped, worldBookReportItem(entry, "global_budget", nil, nil))
			continue
		}
		remaining -= used
		report.Used += used
		selected = append(selected, entry)
		item := worldBookReportItem(entry, candidate.Reason, candidate.Keywords, candidate.Path)
		item["tokens"] = used
		if truncated {
			item["truncated"] = true
			item["original_tokens"] = originalTokens
		}
		report.Activated = append(report.Activated, item)
	}
	return selected, report
}

func worldBookReportItem(entry worldBookEntry, reason string, keywords []string, path []int64) map[string]interface{} {
	return map[string]interface{}{"entry_id": entry.ID, "entry_name": entry.EntryName, "reason": reason, "keywords": keywords, "path": path, "priority": entry.Priority}
}

func persistWorldBookActivations(conversationID int64, turn int, selected []worldBookEntry, report worldBookMatchReport) {
	reasons := map[int64]string{}
	for _, item := range report.Activated {
		id, _ := item["entry_id"].(int64)
		reasons[id], _ = item["reason"].(string)
	}
	for _, entry := range selected {
		if reasons[entry.ID] != "keyword" && reasons[entry.ID] != "recursive" {
			continue
		}
		stickyUntil := 0
		if entry.StickyRounds > 0 {
			stickyUntil = turn + entry.StickyRounds - 1
		}
		cooldownUntil := 0
		if entry.CooldownRounds > 0 {
			cooldownUntil = turn + entry.CooldownRounds
		}
		_, _ = db.DB.Exec(`INSERT INTO world_book_activation_state(conversation_id,entry_id,sticky_until_turn,cooldown_until_turn,last_activated_turn,updated_at)
			VALUES(?,?,?,?,?,datetime('now')) ON CONFLICT(conversation_id,entry_id) DO UPDATE SET sticky_until_turn=excluded.sticky_until_turn,
			cooldown_until_turn=excluded.cooldown_until_turn,last_activated_turn=excluded.last_activated_turn,updated_at=datetime('now')`,
			conversationID, entry.ID, stickyUntil, cooldownUntil, turn)
	}
}

func worldBookScanText(userText string, hist []memory.Message, depth int) string {
	start := len(hist) - depth
	if start < 0 {
		start = 0
	}
	parts := make([]string, 0, len(hist)-start+1)
	for _, message := range hist[start:] {
		parts = append(parts, message.Content)
	}
	parts = append(parts, userText)
	return strings.Join(parts, "\n")
}

func matchWorldBookEntries(entries []worldBookEntry, scanText string, cfg worldBookConfig) []worldBookEntry {
	if cfg.BudgetMode == "" {
		cfg.BudgetMode = "fixed"
	}
	matched, _ := evaluateWorldBookEntries(entries, scanText, cfg, nil, 1)
	return matched
}

func fitWorldBookEntry(entry worldBookEntry, budget int) (worldBookEntry, bool) {
	if budget <= 0 || estimateWorldBookTokens(formatWorldBookEntry(entry)) <= budget {
		return entry, true
	}
	empty := entry
	empty.Content = ""
	contentBudget := budget - estimateWorldBookTokens(formatWorldBookEntry(empty))
	if contentBudget <= 0 {
		return entry, false
	}
	entry.Content = truncateWorldBookContent(entry.Content, contentBudget)
	return entry, strings.TrimSpace(entry.Content) != "" && estimateWorldBookTokens(formatWorldBookEntry(entry)) <= budget
}

func worldBookEntryMatches(entry worldBookEntry, scanText string, caseSensitive bool) ([]string, bool) {
	primary := matchingWorldBookKeywords(entry.Keywords, scanText, caseSensitive, entry.MatchWholeWords, true)
	if len(primary) == 0 {
		return nil, false
	}
	secondaryAll := splitKeywords(entry.SecondaryKeywords)
	if len(secondaryAll) == 0 {
		return primary, true
	}
	secondary := matchingWorldBookKeywords(entry.SecondaryKeywords, scanText, caseSensitive, entry.MatchWholeWords, false)
	matched := false
	switch entry.SecondaryLogic {
	case "", "or_any":
		matched = len(secondary) > 0
	case "not_any":
		matched = len(secondary) == 0
	case "not_all":
		matched = len(secondary) < len(secondaryAll)
	default:
		matched = len(secondary) == len(secondaryAll)
	}
	return append(primary, secondary...), matched
}

func matchingWorldBookKeywords(value, scanText string, caseSensitive, wholeWords, protectSingleCJK bool) []string {
	matches := []string{}
	for _, keyword := range splitKeywords(value) {
		effectiveWholeWords := wholeWords || (protectSingleCJK && utf8.RuneCountInString(keyword) == 1 && containsCJK(keyword))
		if worldBookKeywordMatches(scanText, keyword, caseSensitive, effectiveWholeWords) {
			matches = append(matches, keyword)
		}
	}
	return matches
}

func worldBookKeywordMatches(text, keyword string, caseSensitive, wholeWords bool) bool {
	if !caseSensitive {
		text, keyword = strings.ToLower(text), strings.ToLower(keyword)
	}
	keywordRunes := []rune(keyword)
	if !wholeWords || (containsCJK(keyword) && len(keywordRunes) > 1) {
		return strings.Contains(text, keyword)
	}
	textRunes := []rune(text)
	for start := 0; start+len(keywordRunes) <= len(textRunes); start++ {
		if string(textRunes[start:start+len(keywordRunes)]) != keyword {
			continue
		}
		beforeOK := start == 0 || !isWorldBookWordRune(textRunes[start-1])
		after := start + len(keywordRunes)
		afterOK := after == len(textRunes) || !isWorldBookWordRune(textRunes[after])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func containsCJK(value string) bool {
	for _, r := range value {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func isWorldBookWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
}

func estimateWorldBookTokens(value string) int {
	runes := utf8.RuneCountInString(value)
	if runes == 0 {
		return 0
	}
	return (runes + 1) / 2
}

func truncateWorldBookContent(content string, budget int) string {
	if budget <= 0 || estimateWorldBookTokens(content) <= budget {
		return content
	}
	runes := []rune(content)
	limit := budget * 2
	if limit > len(runes) {
		limit = len(runes)
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func formatWorldBookEntry(entry worldBookEntry) string {
	return "[World Book: " + entry.EntryName + "]\n" + entry.Content + "\n[/World Book]"
}

func injectWorldBookEntries(messages []ChatMessage, entries []worldBookEntry) []ChatMessage {
	buckets := map[int][]ChatMessage{}
	for _, entry := range entries {
		index := worldBookInsertionIndex(messages, entry)
		buckets[index] = append(buckets[index], ChatMessage{Role: "system", Content: formatWorldBookEntry(entry)})
	}
	result := make([]ChatMessage, 0, len(messages)+len(entries))
	for i := 0; i <= len(messages); i++ {
		result = append(result, buckets[i]...)
		if i < len(messages) {
			result = append(result, messages[i])
		}
	}
	return result
}

func worldBookInsertionIndex(messages []ChatMessage, entry worldBookEntry) int {
	switch entry.InjectionPosition {
	case "after_system_prompt":
		if len(messages) > 0 && messages[0].Role == "system" {
			return 1
		}
		return 0
	case "before_last_message":
		return worldBookMaxInt(0, len(messages)-2)
	case "author_note_depth":
		return worldBookMaxInt(0, len(messages)-entry.Depth)
	default:
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				return i
			}
		}
		return len(messages)
	}
}

func worldBookEntryIDs(entries []worldBookEntry) []int64 {
	ids := make([]int64, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return ids
}

func worldBookMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
