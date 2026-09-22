package api

import (
	"encoding/json"
	"strings"
	"testing"

	"myapp/internal/db"
	"myapp/internal/memory"
)

func TestReadWorldBookToolReturnsEntryContent(t *testing.T) {
	setupAPITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO world_book_entries(entry_name,keywords,secondary_keywords,content,injection_position,depth,priority,token_budget,enabled) VALUES(?,?,?,?,?,?,?,?,1)`, "王都", "王都,首都", "城邦", "王都位于银色河谷。", "before_last_message", 1, 10, 512); err != nil {
		t.Fatal(err)
	}
	raw := runReadWorldBookTool(`{"query":"王都"}`)
	var result struct {
		Count   int              `json:"count"`
		Entries []worldBookEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Entries) != 1 || result.Entries[0].Content != "王都位于银色河谷。" {
		t.Fatalf("unexpected world book result: %s", raw)
	}
}

func TestMatchWorldBookEntriesUsesPrimaryAndSecondaryKeywords(t *testing.T) {
	entries := []worldBookEntry{
		{ID: 1, EntryName: "London", Keywords: "伦敦,London", SecondaryKeywords: "雨,雾", Content: "content", Priority: 20, Enabled: true},
		{ID: 2, EntryName: "Tea", Keywords: "TEA", Content: "content", Priority: 10, Enabled: true},
	}
	matched := matchWorldBookEntries(entries, "London tea under clear skies", worldBookConfig{TokenBudget: 2048})
	if len(matched) != 1 || matched[0].ID != 2 {
		t.Fatalf("matched = %#v, want only case-insensitive TEA entry", matched)
	}
	matched = matchWorldBookEntries(entries, "London 有雾 and tea", worldBookConfig{TokenBudget: 2048})
	if len(matched) != 2 || matched[0].ID != 1 || matched[1].ID != 2 {
		t.Fatalf("matched order = %#v, want descending priority order [1,2]", matched)
	}
}

func TestWorldBookScanTextUsesConfiguredRecentDepth(t *testing.T) {
	history := []memory.Message{{Content: "old"}, {Content: "middle"}, {Content: "recent"}}
	got := worldBookScanText("current", history, 2)
	if got != "middle\nrecent\ncurrent" {
		t.Fatalf("scan text = %q", got)
	}
}

func TestMatchWorldBookEntriesRespectsGlobalAndEntryBudgets(t *testing.T) {
	entries := []worldBookEntry{
		{ID: 1, EntryName: "High", Keywords: "hit", Content: strings.Repeat("甲", 80), Priority: 1, TokenBudget: 24, Enabled: true},
		{ID: 2, EntryName: "Low", Keywords: "hit", Content: strings.Repeat("乙", 80), Priority: 2, Enabled: true},
	}
	matched := matchWorldBookEntries(entries, "hit", worldBookConfig{TokenBudget: 100})
	if len(matched) != 2 || estimateWorldBookTokens(formatWorldBookEntry(matched[1])) > 24 {
		t.Fatalf("entry budget not enforced: %#v", matched)
	}
	total := 0
	for _, entry := range matched {
		total += estimateWorldBookTokens(formatWorldBookEntry(entry))
	}
	if total > 100 {
		t.Fatalf("global budget exceeded: %d", total)
	}
}

func TestInjectWorldBookEntriesPositionsAndPriority(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "current"},
	}
	entries := []worldBookEntry{
		{EntryName: "A", Content: "a", InjectionPosition: "after_system_prompt", Priority: 1},
		{EntryName: "B", Content: "b", InjectionPosition: "before_user_message", Priority: 2},
		{EntryName: "C", Content: "c", InjectionPosition: "before_last_message", Priority: 3},
		{EntryName: "D", Content: "d", InjectionPosition: "author_note_depth", Depth: 1, Priority: 4},
	}
	got := injectWorldBookEntries(messages, entries)
	wantRoles := []string{"system", "system", "user", "system", "assistant", "system", "system", "user"}
	if len(got) != len(wantRoles) {
		t.Fatalf("message count = %d, want %d", len(got), len(wantRoles))
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, got[i].Role, role)
		}
	}
	if !strings.Contains(got[5].Content.(string), "World Book: B") || !strings.Contains(got[6].Content.(string), "World Book: D") {
		t.Fatalf("same-position priority order was not preserved: %#v", got)
	}
}

func TestWorldBookConstantEntriesLeadDescendingPriority(t *testing.T) {
	entries := []worldBookEntry{
		{ID: 1, EntryName: "keyword", EntryType: "keyword", Keywords: "hit", Content: "keyword", Priority: 100, Enabled: true},
		{ID: 2, EntryName: "constant", EntryType: "constant", Content: "constant", Priority: 1, Enabled: true},
		{ID: 3, EntryName: "higher", EntryType: "keyword", Keywords: "hit", Content: "higher", Priority: 80, Enabled: true},
	}
	matched, _ := evaluateWorldBookEntries(entries, "hit", worldBookConfig{BudgetMode: "fixed", TokenBudget: 4096}, nil, 1)
	if len(matched) != 3 || matched[0].ID != 2 || matched[1].ID != 1 || matched[2].ID != 3 {
		t.Fatalf("matched order = %#v, want constant then descending priority", matched)
	}
}

func TestWorldBookWholeWordMatching(t *testing.T) {
	if worldBookKeywordMatches("concatenate cat", "cat", false, true) != true {
		t.Fatal("whole-word cat should match standalone cat")
	}
	if worldBookKeywordMatches("concatenate", "cat", false, true) {
		t.Fatal("whole-word cat matched inside concatenate")
	}
	if !worldBookKeywordMatches("我今天回家", "今天", false, true) {
		t.Fatal("CJK keyword should use contiguous matching")
	}
	if worldBookKeywordMatches("这是克劳德", "克", false, true) {
		t.Fatal("single-character CJK keyword matched inside a longer word")
	}
	if matches := matchingWorldBookKeywords("克", "这是克劳德", false, false, true); len(matches) != 0 {
		t.Fatal("unsafe single-character primary CJK keyword matched inside a longer word")
	}
	if !worldBookKeywordMatches("回答：克。", "克", false, true) {
		t.Fatal("standalone single-character CJK keyword did not match")
	}
}

func TestWorldBookSecondaryKeywordLogic(t *testing.T) {
	for _, tc := range []struct {
		logic string
		text  string
		want  bool
	}{
		{"and_all", "main red blue", true}, {"and_all", "main red", false},
		{"or_any", "main blue", true}, {"or_any", "main green", false},
		{"not_any", "main green", true}, {"not_any", "main red", false},
		{"not_all", "main red", true}, {"not_all", "main red blue", false},
	} {
		entry := worldBookEntry{Keywords: "main", SecondaryKeywords: "red,blue", SecondaryLogic: tc.logic}
		_, got := worldBookEntryMatches(entry, tc.text, false)
		if got != tc.want {
			t.Errorf("logic=%s text=%q got=%v want=%v", tc.logic, tc.text, got, tc.want)
		}
	}
}

func TestWorldBookRecursiveActivationHonorsLimit(t *testing.T) {
	entries := []worldBookEntry{
		{ID: 1, EntryName: "one", EntryType: "keyword", Keywords: "start", Content: "opens-two", Enabled: true},
		{ID: 2, EntryName: "two", EntryType: "keyword", Keywords: "opens-two", Content: "opens-three", Enabled: true},
		{ID: 3, EntryName: "three", EntryType: "keyword", Keywords: "opens-three", Content: "done", Enabled: true},
	}
	matched, report := evaluateWorldBookEntries(entries, "start", worldBookConfig{BudgetMode: "fixed", TokenBudget: 4096, Recursive: true, MaxRecursion: 1}, nil, 1)
	if len(matched) != 2 || len(report.RecursionPaths) != 1 || matched[0].ID != 1 || matched[1].ID != 2 {
		t.Fatalf("matched=%#v paths=%#v", matched, report.RecursionPaths)
	}
}

func TestWorldBookRecursiveActivationRequiresEntryOptIn(t *testing.T) {
	entries := []worldBookEntry{
		{ID: 1, EntryName: "parent", EntryType: "keyword", Keywords: "start", Content: "opens-child", PreventRecursion: true, Enabled: true},
		{ID: 2, EntryName: "child", EntryType: "keyword", Keywords: "opens-child", Content: "child", ExcludeRecursion: true, Enabled: true},
	}
	cfg := worldBookConfig{BudgetMode: "fixed", TokenBudget: 4096, Recursive: true, MaxRecursion: 2}
	matched, _ := evaluateWorldBookEntries(entries, "start", cfg, nil, 1)
	if len(matched) != 1 || matched[0].ID != 1 {
		t.Fatalf("protected recursion matched=%#v, want only parent", matched)
	}
	entries[0].PreventRecursion = false
	entries[1].ExcludeRecursion = false
	matched, _ = evaluateWorldBookEntries(entries, "start", cfg, nil, 1)
	if len(matched) != 2 {
		t.Fatalf("opted-in recursion matched=%#v, want parent and child", matched)
	}
}

func TestWorldBookOversizedDirectEntryFitsGlobalBudget(t *testing.T) {
	entry := worldBookEntry{ID: 1, EntryName: "large", EntryType: "keyword", Keywords: "hit", Content: strings.Repeat("内容", 300), Enabled: true}
	matched, report := evaluateWorldBookEntries([]worldBookEntry{entry}, "hit", worldBookConfig{BudgetMode: "fixed", TokenBudget: 80}, nil, 1)
	if len(matched) != 1 || estimateWorldBookTokens(formatWorldBookEntry(matched[0])) > 80 {
		t.Fatalf("oversized entry was not fitted: %#v", matched)
	}
	if len(report.Activated) != 1 || report.Activated[0]["truncated"] != true {
		t.Fatalf("truncation missing from report: %#v", report.Activated)
	}
}

func TestWorldBookStickyCooldownAndSameTurnRetry(t *testing.T) {
	entry := worldBookEntry{ID: 1, EntryName: "stateful", EntryType: "keyword", Keywords: "hit", Content: "content", Enabled: true}
	cfg := worldBookConfig{BudgetMode: "fixed", TokenBudget: 4096}
	sticky, _ := evaluateWorldBookEntries([]worldBookEntry{entry}, "miss", cfg, map[int64]worldBookActivationState{1: {StickyUntil: 4, CooldownUntil: 6, LastActivated: 2}}, 4)
	if len(sticky) != 1 {
		t.Fatal("sticky entry was not active")
	}
	cooled, _ := evaluateWorldBookEntries([]worldBookEntry{entry}, "hit", cfg, map[int64]worldBookActivationState{1: {CooldownUntil: 6, LastActivated: 2}}, 4)
	if len(cooled) != 0 {
		t.Fatal("cooldown entry activated on a later turn")
	}
	retry, _ := evaluateWorldBookEntries([]worldBookEntry{entry}, "hit", cfg, map[int64]worldBookActivationState{1: {CooldownUntil: 6, LastActivated: 4}}, 4)
	if len(retry) != 1 {
		t.Fatal("same-turn retry was incorrectly blocked")
	}
}

func TestWorldBookGroupCompetitionAndPercentBudget(t *testing.T) {
	entries := []worldBookEntry{
		{ID: 1, EntryName: "winner", EntryType: "keyword", Keywords: "hit", Content: "one", Priority: 20, GroupName: "tone", GroupCompetition: true, Enabled: true},
		{ID: 2, EntryName: "loser", EntryType: "keyword", Keywords: "hit", Content: "two", Priority: 10, GroupName: "tone", GroupCompetition: true, Enabled: true},
	}
	cfg := worldBookConfig{BudgetMode: "percent", BudgetPercent: 15}
	matched, report := evaluateWorldBookEntries(entries, "hit", cfg, nil, 1)
	if report.Budget != contextWindowEstimate*15/100 || len(matched) != 1 || matched[0].ID != 1 {
		t.Fatalf("budget=%d matched=%#v", report.Budget, matched)
	}
	if len(report.Dropped) != 1 || report.Dropped[0]["reason"] != "group_competition" {
		t.Fatalf("dropped=%#v", report.Dropped)
	}
}
