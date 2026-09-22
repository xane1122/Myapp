package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func finalBudgetContext() context.Context {
	return context.WithValue(context.Background(), statsContextKey{}, newChatStats(0))
}

func finalBudgetFixture(t *testing.T, target int, multipart bool) ([]ChatMessage, []Tool) {
	t.Helper()
	fields := map[string]interface{}{"context_version": "v1", "current_user_message": "现在的问题不能删", "user_profile": map[string]string{"name": "用户"}, "unknown_future_field": map[string]string{"value": "必须保留"}}
	recent := []map[string]string{}
	for i := 0; i < 20; i++ {
		recent = append(recent, map[string]string{"role": "user", "content": "原始历史"})
	}
	fields["recent_messages"] = recent
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "search_memory", Description: "完整工具定义"}}}
	messages := []ChatMessage{{Role: "system", Content: strings.Repeat("固定人设", 1100)+"\n通道 B 执行规则"}, {Role: "user"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1"}}}, {Role: "tool", ToolCallID: "call-1", Content: "真实工具结果不能删"}}
	set := func(n int) {
		fields["memory_index"] = []map[string]interface{}{{"memory_id": 1, "summary": strings.Repeat("a", n)}}
		raw, _ := json.Marshal(fields)
		text := "包装规则\n<context_json>\n"+string(raw)+"\n</context_json>\n保留后缀"
		if multipart {
			messages[1].Content = []ContentPart{{Type: "text", Text: text}, {Type: "text", Text: "文件预览不可删"}, {Type: "image_url", ImageURL: &ImageURLPart{URL: "data:image/png;base64,TEST"}}}
		} else {
			messages[1].Content = text
		}
	}
	set(0)
	base := estimateModelInput(messages, tools)
	if base >= target { t.Fatalf("fixture base %d >= %d", base, target) }
	// Three ASCII characters change the conservative estimate by exactly one.
	set((target-base)*3)
	if n := estimateModelInput(messages, tools); n != target { t.Fatalf("fixture cost %d, want %d", n,target) }
	return messages, tools
}

func TestFinalInputBudgetFitsLoggedEstimatesWithoutChangingProtectedInput(t *testing.T) {
	t.Setenv("CONTEXT_TOKEN_BUDGET", "32768")
	for _, target := range []int{32879, 32897, 32992, 36891} {
		for _, multipart := range []bool{false,true} {
			messages, tools := finalBudgetFixture(t,target,multipart)
			original, _ := json.Marshal(messages)
			fitted := fitFinalModelInput(finalBudgetContext(),messages,tools,upstreamTool)
			if err := checkModelInputBudget(finalBudgetContext(),fitted,tools); err != nil { t.Fatal(err) }
			unchanged, _ := json.Marshal(messages)
			if string(original)!=string(unchanged) { t.Fatal("original context mutated") }
			if !reflect.DeepEqual(fitted[0],messages[0]) || !reflect.DeepEqual(fitted[2:],messages[2:]) { t.Fatal("identity/tool pairs or results changed") }
			text, ok := fitted[1].Content.(string)
			if !ok {
				parts := fitted[1].Content.([]ContentPart); originalParts := messages[1].Content.([]ContentPart)
				if !reflect.DeepEqual(parts[1:],originalParts[1:]) { t.Fatal("attachment changed") }
				text=parts[0].Text
			}
			if !strings.HasPrefix(text,"包装规则\n<context_json>\n") || !strings.HasSuffix(text,"\n</context_json>\n保留后缀") { t.Fatal("wrapper changed") }
			start:=strings.Index(text,"<context_json>")+len("<context_json>"); end:=strings.LastIndex(text,"</context_json>")
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(text[start:end]),&fields)!=nil { t.Fatal("invalid context JSON") }
			var current string;_ = json.Unmarshal(fields["current_user_message"],&current)
			if current!="现在的问题不能删" || !strings.Contains(string(fields["unknown_future_field"]),"必须保留") { t.Fatal("current/unknown field lost") }
			var recent []json.RawMessage;_ = json.Unmarshal(fields["recent_messages"],&recent)
			if len(recent)!=20 { t.Fatal("history reduced before lower-priority directory") }
		}
	}
}

func TestFinalInputBudgetRemovesOldestHistoryAfterOptionalEvidence(t *testing.T) {
	t.Setenv("CONTEXT_TOKEN_BUDGET","32768")
	fields:=map[string]interface{}{"context_version":"v1","current_user_message":"当前问题","recent_messages":[]map[string]string{{"content":strings.Repeat("旧",6000)},{"content":"最新事实"}}}
	raw,_:=json.Marshal(fields)
	messages:=[]ChatMessage{{Role:"system",Content:strings.Repeat("固定",6000)},{Role:"user",Content:"<context_json>"+string(raw)+"</context_json>"}}
	fitted:=fitFinalModelInput(finalBudgetContext(),messages,nil,upstreamChat)
	if err:=checkModelInputBudget(finalBudgetContext(),fitted,nil);err!=nil {t.Fatal(err)}
	if strings.Contains(fitted[1].Content.(string),strings.Repeat("旧",6000)) || !strings.Contains(fitted[1].Content.(string),"最新事实") {t.Fatal("wrong history removed")}
}

func TestFinalInputBudgetLeavesUntrimmableAndUntrackedInputToGuard(t *testing.T) {
	t.Setenv("CONTEXT_TOKEN_BUDGET","32768")
	for _,text:=range []string{strings.Repeat("当前正文",5000),"<context_json>{invalid}</context_json>","<context_json>{\"context_version\":\"v1\",\"current_user_message\":\"保留\",\"recent_messages\":[]}</context_json>"} {
		messages:=[]ChatMessage{{Role:"system",Content:strings.Repeat("固定",9000)},{Role:"user",Content:text}}
		fitted:=fitFinalModelInput(finalBudgetContext(),messages,nil,upstreamChat)
		if !reflect.DeepEqual(messages,fitted) || checkModelInputBudget(finalBudgetContext(),fitted,nil)==nil {t.Fatal("fixed or malformed context was silently shortened")}
		if !reflect.DeepEqual(messages,fitFinalModelInput(context.Background(),messages,nil,upstreamTool)) {t.Fatal("untracked call changed")}
	}
}

type finalBudgetTransport func(*http.Request) (*http.Response,error)
func (f finalBudgetTransport) RoundTrip(r *http.Request) (*http.Response,error) { return f(r) }

func TestFinalInputBudgetAppliedBeforeActualProviderPayloadOnce(t *testing.T) {
	t.Setenv("CONTEXT_TOKEN_BUDGET","32768")
	messages,tools:=finalBudgetFixture(t,36891,true)
	old:=httpClient
	defer func(){httpClient=old}()
	count:=0
	httpClient=&http.Client{Transport:finalBudgetTransport(func(r *http.Request)(*http.Response,error){
		count++
		var payload chatRequest
		if err:=json.NewDecoder(r.Body).Decode(&payload);err!=nil {t.Fatal(err)}
		// Decode ContentPart interfaces back into their typed form for the same estimator.
		body,_:=json.Marshal(payload.Messages[1].Content)
		var parts []ContentPart; if json.Unmarshal(body,&parts)!=nil {t.Fatal("multipart payload lost")};payload.Messages[1].Content=parts
		if estimateModelInput(payload.Messages,payload.Tools)>32768 {t.Fatal("oversized payload sent")}
		if payload.Model!="test-model" || payload.MaxTokens!=900 || !reflect.DeepEqual(payload.Tools,tools) {t.Fatal("model/budget/registry changed")}
		return &http.Response{StatusCode:200,Body:io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"DONE"}}]}`)),Header:make(http.Header)},nil
	})}
	result,err:=callWithToolsChoicePriorityContext(finalBudgetContext(),"test-key","https://api.jiushi.xin/v1","test-model",messages,900,tools,nil,nil,upstreamTool)
	if err!=nil || result.Content!="DONE" || count!=1 {t.Fatalf("result=%#v, err=%v, requests=%d",result,err,count)}
}
