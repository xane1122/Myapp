package api

import (
 "context"
 "fmt"
 "encoding/json"
 "io"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "myapp/internal/db"
 "myapp/internal/memory"
 "database/sql"
)

func TestChatStatsIncludesInitialAndToolFollowupUsage(t *testing.T) {
 setupAPITestDB(t)
 oldClient,oldStream:=httpClient,streamHTTPClient;t.Cleanup(func(){httpClient=oldClient;streamHTTPClient=oldStream})
 calls:=0
 httpClient=&http.Client{Transport:roundTripFunc(func(r *http.Request)(*http.Response,error){
  calls++;body:=`{"choices":[{"message":{"content":"回答"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`
  if calls==1 {body=`{"choices":[{"message":{"content":"","tool_calls":[{"id":"tool1","type":"function","function":{"name":"get_current_time","arguments":"{}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":3}}`}
  return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader(body))},nil
 })}
 s:=newChatStats(1);s.retrievalMS=7;ctx:=context.WithValue(context.Background(),statsContextKey{},s)
 first,err:=callWithToolsChoicePriorityContext(ctx,"key","https://api.openai.com/v1","test-model",nil,100,nil,nil,nil,upstreamChat);if err!=nil{t.Fatal(err)}
 if _,_,_,_,err:=resolveToolCalls("key","https://api.openai.com/v1","test-model",1,nil,first,nil,nil,nil,ctx);err!=nil{t.Fatal(err)}
 s.finish()
 var input,output,count,cache,complete,retrieval int
 if err:=db.DB.QueryRow(`SELECT input_tokens,output_tokens,request_count,cache_hit_tokens,cache_complete,retrieval_ms FROM request_stats WHERE record_kind='chat'`).Scan(&input,&output,&count,&cache,&complete,&retrieval);err!=nil{t.Fatal(err)}
 if input!=30||output!=5||count!=2||cache!=4||complete!=0||retrieval!=7{t.Fatalf("aggregate %d %d %d %d %d %d",input,output,count,cache,complete,retrieval)}
 if err:=db.DB.QueryRow(`SELECT COUNT(*) FROM request_stats WHERE record_kind='model'`).Scan(&count);err!=nil||count!=2{t.Fatalf("model rows=%d err=%v",count,err)}
}

func TestStreamFinalUsageAndUnknownCache(t *testing.T) {
 stream:=`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}
data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":0}}}
data: [DONE]
`
 result,err:=readChatStream(strings.NewReader(stream),func(string)error{return nil});if err!=nil{t.Fatal(err)}
 if !result.Usage.Reported||result.Usage.InputTokens!=12||result.Usage.CacheHitTokens==nil||*result.Usage.CacheHitTokens!=0{t.Fatal("final usage chunk lost")}
 if parseModelUsage(json.RawMessage(`{"input_tokens":3}`)).CacheHitTokens!=nil{t.Fatal("unknown cache became zero")}
}

func TestMemoryAdminUsesExistingAuthAndScopeIsolation(t *testing.T) {
 setupAPITestDB(t);t.Setenv("APP_ALLOWED_ORIGIN","https://xanelove.com");t.Setenv("APP_BEARER_TOKEN","")
 other,err:=memory.CreateConversation("private");if err!=nil{t.Fatal(err)}
 id,err:=memory.SaveChunkWithMetadataReturningID(other.ID,memory.ScopeConversation,"manual",sql.NullInt64{},"private data","private summary","private",memory.ChunkMetadata{});if err!=nil{t.Fatal(err)}
 h:=Handler()
 request:=func(method,url,body,origin string)*httptest.ResponseRecorder{r:=httptest.NewRequest(method,url,strings.NewReader(body));if origin!=""{r.Header.Set("Origin",origin)};w:=httptest.NewRecorder();h.ServeHTTP(w,r);return w}
 if w:=request("GET","/api/memory?conversation_id=1","","");w.Code!=200||strings.Contains(w.Body.String(),"private data"){t.Fatal("conversation isolation failed")}
 if w:=request("GET","/api/memory/export","","https://evil.example");w.Code!=403{t.Fatal("auth middleware bypassed")}
 if w:=request("DELETE","/api/memory/"+strconv64(id)+"?conversation_id=1","","");w.Code!=404{t.Fatal("wrong conversation deletion accepted")}
 if w:=request("DELETE","/api/memory?scope=typo&memory_type=fact","","");w.Code!=400{t.Fatal("invalid scope expanded")}
 if w:=request("POST","/api/memory/clear",`{"confirm":"true"}`,"");w.Code!=400{t.Fatal("weak confirmation accepted")}
 w:=request("GET","/api/memory/export","","");if w.Code!=200||!strings.Contains(w.Header().Get("Content-Disposition"),".json"){t.Fatal("export missing download headers")}
 if w:=request("DELETE","/api/memory/"+strconv64(id)+"?conversation_id="+strconv64(other.ID),"","");w.Code!=200{t.Fatalf("correct deletion: %d",w.Code)}
}

func strconv64(n int64)string{return fmt.Sprintf("%d",n)}
