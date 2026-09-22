package api
import("testing";"os";"path/filepath";"strings";"context";"myapp/internal/memory";"myapp/internal/contextbuilder")
func TestChatFileFullInputIncludesMiddleAndEndWithoutModelTools(t *testing.T){
 t.Setenv("CONTEXT_TOKEN_BUDGET","65536")
 raw:=strings.Repeat("开头段落",200)+"中间证据"+strings.Repeat("后文段落",300)+"最后证据"
 path:=filepath.Join(t.TempDir(),"reference.md");os.WriteFile(path,[]byte(raw),0600)
 a:=memory.Attachment{ID:7,ConversationID:1,MessageID:11,Kind:"file",FilePath:path,OriginalName:"reference.md"}
 full:=attachmentDocumentText(a,chatDocumentAllowance([]memory.Attachment{a}))
 if !strings.Contains(full,"[自动全文载入完成：")||!strings.Contains(full,"中间证据")||!strings.Contains(full,"最后证据"){t.Fatal("complete body not injected")}
 if contextbuilder.EstimateTextTokens(full)>chatDocumentAllowance([]memory.Attachment{a}){t.Fatal("allowance exceeded")}
 bytes,_:=os.ReadFile(path);if string(bytes)!=raw{t.Fatal("original file changed")}
 msgs:=[]ChatMessage{{Role:"user",Content:[]ContentPart{{Type:"text",Text:full}}}}
 if hasCurrentChatFilePreview(msgs)||!hasCompleteChatFileContext(msgs){t.Fatal("whole file incorrectly treated as preview")}
 setupAPITestDB(t)
 built,e:=buildJSONContextMessagesWithChunks(1,"",nil,[]memory.Attachment{a},nil);if e!=nil||!hasCompleteChatFileContext(built.Messages){t.Fatal("actual JSON builder did not inject full body")}
 parts:=built.Messages[1].Content.([]ContentPart);if !strings.Contains(parts[1].Text,"最后证据"){t.Fatal("actual outgoing payload omitted document end")}
 original:=callMemoryToolModel;defer func(){callMemoryToolModel=original}()
 callMemoryToolModel=func(_ context.Context,_,_,_ string,_ []ChatMessage,_ int,_ []Tool,choice interface{},_ func(string)error,_ upstreamPriority)(ChatResult,error){if choice!=nil{t.Fatal("complete file unnecessarily forces another read")};return ChatResult{Content:"NO_TOOL"},nil}
 _,err:=collectToolSummaryWithModelChannel(modelChannelConfig{APIKey:"mock",Model:"mock"},"读这个文件",1,msgs,chatTools(),true,false,nil,nil,nil);if err!=nil{t.Fatal(err)}
 hist:=[]memory.Message{{ID:11,Role:"user",ConversationID:1,Attachments:[]memory.Attachment{a}}}
 followup:=recentChatFileDirectory(1,hist,nil,"读取刚才的md")
 if !strings.Contains(followup,"最后证据"){t.Fatal("followup lost end of uploaded file")}
 if strings.Contains(recentChatFileDirectory(1,hist,nil,"天气如何"),"最后证据"){t.Fatal("unrelated turn injected body")}
 if strings.Contains(recentChatFileDirectory(2,hist,nil,"读取刚才的md"),"最后证据"){t.Fatal("cross-conversation file exposed")}
}
func TestChatFileFullInputLeavesOversizedOriginalQueryable(t *testing.T){
 t.Setenv("CONTEXT_TOKEN_BUDGET","65536");path:=filepath.Join(t.TempDir(),"large.md");raw:=strings.Repeat("文",60000)+"最后证据";os.WriteFile(path,[]byte(raw),0600)
 a:=memory.Attachment{ID:8,Kind:"file",FilePath:path,OriginalName:"large.md"}
 preview:=attachmentDocumentText(a,chatDocumentAllowance([]memory.Attachment{a,a}))
 if !strings.Contains(preview,"[分段文件：")||strings.Contains(preview,"[自动全文载入完成："){t.Fatal("oversized file falsely reported complete")}
 whole,_:=extractText(path);last,e:=chatFileSegment(whole,0,768,"最后证据");if e!=nil||!strings.Contains(last["content"].(string),"最后证据"){t.Fatal("full original no longer queryable")}
}
