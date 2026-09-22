package memory

import (
 "database/sql"
 "fmt"
 "sync"
 "testing"
 "myapp/internal/db"
)

func TestVersionedMemoryConcurrentWritesAndRoleIsolation(t *testing.T) {
 setupTestDB(t)
 other,err:=CreateConversationForAssistant("other","grok");if err!=nil{t.Fatal(err)}
 meta:=ChunkMetadata{MemoryType:"preference"}
 old,err:=SaveChunkWithMetadataReturningID(1,ScopeGlobal,"manual",sql.NullInt64{},"饮品选择咖啡","旧偏好","饮品偏好,日常选择",meta);if err!=nil{t.Fatal(err)}
 foreign,err:=SaveChunkWithMetadataReturningID(other.ID,ScopeGlobal,"manual",sql.NullInt64{},"饮品选择红茶","独立偏好","饮品偏好,日常选择",meta);if err!=nil{t.Fatal(err)}
 errors:=make(chan error,4);var wg sync.WaitGroup
 for i:=0;i<4;i++{wg.Add(1);go func(i int){defer wg.Done();_,e:=SaveChunkWithMetadataReturningID(1,ScopeGlobal,"manual",sql.NullInt64{},fmt.Sprintf("饮品选择新值%d",i),"新偏好","饮品偏好,日常选择",meta);errors<-e}(i)}
 wg.Wait();close(errors);for err:=range errors{if err!=nil{t.Fatal(err)}}
 var active int;if err:=db.DB.QueryRow(`SELECT COUNT(*) FROM memory_chunks WHERE assistant='rhys' AND status='active'`).Scan(&active);err!=nil||active!=1{t.Fatalf("active=%d err=%v",active,err)}
 c,err:=GetChunk(old);if err!=nil||c.Status!="superseded"||!c.SupersededBy.Valid{t.Fatalf("old status=%s link=%v err=%v",c.Status,c.SupersededBy,err)}
 c,err=GetChunk(foreign);if err!=nil||!c.Active{t.Fatalf("foreign memory changed: %v",err)}
 hits,err:=SearchChunks(1,"饮品选择",10);if err!=nil||len(hits)!=1{t.Fatalf("hits=%d err=%v",len(hits),err)}
 if hits[0].RankMethod!="fts5_bm25"||hits[0].SourceDate==""||hits[0].OriginalSnippet==""{t.Fatal("missing evidence metadata")}
 if _,err:=RevokeMemories(MemoryFilter{ConversationID:other.ID,Scope:"all"},hits[0].ID);err!=sql.ErrNoRows{t.Fatalf("wrong role deletion: %v",err)}
}

func TestDocumentFragmentsAndMemoryControlSafety(t *testing.T) {
 setupTestDB(t)
 doc,err:=CreateKnowledgeDoc("test","test.txt","body","ready","");if err!=nil{t.Fatal(err)}
 for _,body:=range []string{"咖啡文档第一分块","咖啡文档第二分块"}{if err:=SaveChunk(1,ScopeGlobal,"knowledge_doc",sql.NullInt64{Int64:doc.ID,Valid:true},body,"文档","咖啡,文档");err!=nil{t.Fatal(err)}}
 chunks,err:=ListChunks(1,10);if err!=nil||len(chunks)!=2{t.Fatalf("document fragments=%d err=%v",len(chunks),err)}
 hits,err:=SearchChunksBySourceTypes(1,"咖啡",[]string{"knowledge_doc"},10);if err!=nil||len(hits)!=2||hits[0].RankMethod!="short_substring"{t.Fatalf("short search=%d err=%v",len(hits),err)}
 if _,err:=RevokeMemories(MemoryFilter{Scope:"al"},0);err==nil{t.Fatal("unsafe scope accepted")}
 if _,err:=ClearAllMemories("true");err==nil{t.Fatal("weak clear confirmation accepted")}
 if n,err:=RevokeMemories(MemoryFilter{MemoryType:"fact"},0);err!=nil||n!=2{t.Fatalf("type deletion %d %v",n,err)}
 if hits,err:=SearchChunks(1,"咖啡",10);err!=nil||len(hits)!=0{t.Fatal("revoked evidence searchable")}
 exported,err:=ExportMemories(MemoryFilter{Status:"all"});if err!=nil||len(exported)!=2{t.Fatal("audit export missing revoked versions")}
 if n,err:=ClearAllMemories("DELETE_ALL_MEMORIES");err!=nil||n!=2{t.Fatalf("clear %d %v",n,err)}
}
