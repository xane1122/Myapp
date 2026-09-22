package api
import("context";"encoding/json";"net/http";"net/http/httptest";"path/filepath";"strings";"testing";"myapp/internal/db";"myapp/internal/memory")
func TestChatStateTransferAcknowledgement(t *testing.T){
 for _,s:=range []string{"好的。","嗯嗯","转吧","嗯，那就转吧"}{if !affirmativeTransferAcceptance(s){t.Fatal(s)}}
 for _,s:=range []string{"这个好好看","收到","不要了","这样挺好的","要抱抱"}{if affirmativeTransferAcceptance(s){t.Fatal(s)}}
}
func TestChatStateCompletedTransferNotNewOffer(t *testing.T){
 db.Init(filepath.Join(t.TempDir(),"state.db"));t.Cleanup(func(){db.DB.Close()})
 conv,e:=memory.CreateConversation("regression");if e!=nil{t.Fatal(e)}
 if _,e=memory.SaveMessage(conv.ID,"assistant","给你转账，来一笔？");e!=nil{t.Fatal(e)}
 if !contextualVirtualTransferFollowupRequested(conv.ID,"好的"){t.Fatal("unexecuted offer lost")}
 tr,e:=memory.CreateVirtualTransfer(conv.ID,"assistant",520,"synthetic");if e!=nil{t.Fatal(e)}
 for _,s:=range []string{"好的","这个好好看","收到"}{if contextualVirtualTransferFollowupRequested(conv.ID,s){t.Fatal("completed card forced another transfer")}}
 if _,e=memory.ReceiveVirtualTransfer(conv.ID,tr.ID,"user");e!=nil{t.Fatal(e)}
 state:=recentTransferExecutionState(conv.ID);if !strings.Contains(state,`"status":"received"`)||!strings.Contains(state,`"sent":true`){t.Fatal(state)}
 other,e:=memory.CreateConversation("other");if e!=nil{t.Fatal(e)};if recentTransferExecutionState(other.ID)!=""{t.Fatal("foreign transfer leaked")}
 if !explicitVirtualTransferRequested("再给我转账"){t.Fatal("new explicit request blocked")}
}
func TestChatStateNativeInputReceiptBeforeReply(t *testing.T){
 db.Init(filepath.Join(t.TempDir(),"native.db"));t.Cleanup(func(){db.DB.Close()})
 conv,e:=memory.CreateConversation("receipt");if e!=nil{t.Fatal(e)}
 job,token,e:=newNativeReplyCredentials();if e!=nil{t.Fatal(e)}
 _,e=db.DB.Exec(`INSERT INTO native_reply_jobs(job_id,idempotency_key_hash,token_hash,status,conversation_id,assistant,expires_at) VALUES(?,?,?,'running',?,'rhys',datetime(CURRENT_TIMESTAMP,'+1 hour'))`,job,nativeReplyHash("synthetic-key"),nativeReplyHash(token),conv.ID);if e!=nil{t.Fatal(e)}
 req:=httptest.NewRequest(http.MethodPost,"/api/chat",nil)
 if e=acknowledgeNativeReplyInput(req,conv.ID,7,[]int64{6,7});e!=nil{t.Fatal(e)}
 var id int64;db.DB.QueryRow("SELECT user_message_id FROM native_reply_jobs WHERE job_id=?",job).Scan(&id);if id!=0{t.Fatal("headerless public request attached to job")}
 req=req.WithContext(context.WithValue(req.Context(),nativeReplyJobContextKey{},job))
 if e=acknowledgeNativeReplyInput(req,conv.ID,7,[]int64{6,7});e!=nil{t.Fatal(e)}
 get:=httptest.NewRequest(http.MethodGet,"/api/native-replies/"+job+"/result?progress=1",nil);get.Header.Set(nativeReplyTokenHeader,token)
 rec:=httptest.NewRecorder();handleNativeReplyRoute(rec,get)
 var r nativeReplyResult;if e=json.Unmarshal(rec.Body.Bytes(),&r);e!=nil{t.Fatal(e)}
 if r.Status!="running"||r.UserMessageID!=7||len(r.UserMessageIDs)!=2||r.AssistantMessageID!=0{t.Fatalf("%+v",r)}
 get.Header.Set(nativeReplyTokenHeader,strings.Repeat("a",43));rec=httptest.NewRecorder();handleNativeReplyRoute(rec,get);if rec.Code!=404{t.Fatal("receipt token bypass")}
}
