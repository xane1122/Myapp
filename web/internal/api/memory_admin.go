package api

import (
 "database/sql"
 "encoding/json"
 "io"
 "net/http"
 "strconv"
 "strings"
 "myapp/internal/memory"
)

func memoryFilterRequest(r *http.Request) (memory.MemoryFilter,error) {
 q:=r.URL.Query();f:=memory.MemoryFilter{Scope:q.Get("scope"),MemoryType:q.Get("memory_type"),Status:q.Get("status")}
 if raw:=q.Get("conversation_id");raw!="" {id,err:=strconv.ParseInt(raw,10,64);if err!=nil||id<=0{return f,strconv.ErrSyntax};f.ConversationID=id}
 return f,nil
}

func handleMemoryAdmin(w http.ResponseWriter,r *http.Request) {
 f,err:=memoryFilterRequest(r);if err!=nil {jsonResp(w,400,map[string]string{"error":"invalid conversation_id"});return}
 path:=strings.TrimSuffix(r.URL.Path,"/")
 fail:=func(err error){code:=400;if err==sql.ErrNoRows{code=404};jsonResp(w,code,map[string]string{"error":err.Error()})}
 switch {
 case path=="/api/memory/export"&&r.Method==http.MethodGet:
  f.Status="all";chunks,err:=memory.ExportMemories(f);if err!=nil{fail(err);return}
  w.Header().Set("Content-Disposition",`attachment; filename="myapp-memories.json"`);jsonResp(w,200,chunks)
 case path=="/api/memory/search"&&r.Method==http.MethodGet:
  if f.ConversationID<=0{jsonResp(w,400,map[string]string{"error":"conversation_id required"});return}
  if _,err:=memory.GetConversation(f.ConversationID);err!=nil{fail(err);return}
  chunks,err:=memory.SearchChunks(f.ConversationID,r.URL.Query().Get("query"),intQuery(r,"limit",6,100));if err!=nil{fail(err);return};jsonResp(w,200,chunks)
 case path=="/api/memory/clear"&&r.Method==http.MethodPost:
  var body struct{Confirm string `json:"confirm"`};if err:=json.NewDecoder(io.LimitReader(r.Body,4096)).Decode(&body);err!=nil{fail(err);return}
  n,err:=memory.ClearAllMemories(body.Confirm);if err!=nil{fail(err);return};jsonResp(w,200,map[string]int64{"deleted":n})
 case path=="/api/memory"&&r.Method==http.MethodGet:
  if f.Status==""{f.Status="active"};chunks,err:=memory.ExportMemories(f);if err!=nil{fail(err);return};jsonResp(w,200,chunks)
 case path=="/api/memory"&&r.Method==http.MethodDelete:
  if f.MemoryType==""{jsonResp(w,400,map[string]string{"error":"memory_type required; use /clear with confirmation for all memories"});return}
  n,err:=memory.RevokeMemories(f,0);if err!=nil{fail(err);return};jsonResp(w,200,map[string]int64{"revoked":n})
 case strings.HasPrefix(path,"/api/memory/")&&r.Method==http.MethodDelete:
  id,err:=strconv.ParseInt(strings.TrimPrefix(path,"/api/memory/"),10,64);if err!=nil||id<=0{jsonResp(w,400,map[string]string{"error":"invalid memory_id"});return}
  if f.ConversationID>0&&f.Scope==""{f.Scope="all"}
  n,err:=memory.RevokeMemories(f,id);if err!=nil{fail(err);return};jsonResp(w,200,map[string]int64{"revoked":n})
 default:http.Error(w,"method not allowed",405)
 }
}
