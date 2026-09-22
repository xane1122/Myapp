package memory

import (
 "database/sql"
 "encoding/json"
 "fmt"
 "math"
 "strings"
 "myapp/internal/db"
)

func (c Chunk) MarshalJSON() ([]byte,error) {
 type plain Chunk
 var superseded,sourceDoc,sourceConversation interface{}
 if c.SupersededBy.Valid { superseded=c.SupersededBy.Int64 }
 if c.SourceDocID.Valid { sourceDoc=c.SourceDocID.Int64 };if c.SourceConversationID.Valid {sourceConversation=c.SourceConversationID.Int64}
 return json.Marshal(struct { plain; Superseded interface{} `json:"superseded_by"`; SourceDoc interface{} `json:"source_doc_id"`; SourceConversation interface{} `json:"source_conversation_id"` }{plain(c),superseded,sourceDoc,sourceConversation})
}

func memoryTypeFor(source string,meta ChunkMetadata) (string,error) {
 kind:=strings.TrimSpace(meta.MemoryType)
 if kind=="" {
  switch { case source=="conversation":kind="summary"; case source=="diary":kind="diary"; case source=="emotion"||meta.Emotion!="":kind="emotion"; default:kind="fact" }
 }
 switch kind { case "fact","preference","diary","emotion","summary":return kind,nil }
 return "",fmt.Errorf("invalid memory_type")
}

func keywordSet(raw string) map[string]bool {
 out:=map[string]bool{}
 for _,s:=range strings.FieldsFunc(raw,func(r rune)bool{return strings.ContainsRune(",，;；\n",r)}) {
  s=strings.ToLower(strings.TrimSpace(s)); if len([]rune(s))>=2 { out[s]=true }
 }
 return out
}

// Only explicit fact/preference topics are replaceable; document chunks and
// historical summaries remain independent evidence even with common keywords.
func chunkConflicts(tx *sql.Tx,conversation int64,assistant,scope,source,kind,content,keywords,topic string) ([]int64,int64,error) {
 if source=="knowledge_doc" {return nil,0,nil}
 rows,err:=tx.Query(`SELECT id,content,COALESCE(keywords,''),COALESCE(topic_label,'') FROM memory_chunks WHERE active=1 AND status='active' AND assistant=? AND scope=? AND source_type=? AND memory_type=? AND (?='global' OR conversation_id=?)`,assistant,scope,source,kind,scope,conversation)
 if err!=nil { return nil,0,err }; defer rows.Close()
 terms:=keywordSet(keywords); ids:=[]int64{}
 for rows.Next() {
  var id int64; var old,keys,oldTopic string
  if err:=rows.Scan(&id,&old,&keys,&oldTopic);err!=nil { return nil,0,err }
  if normalizeChunkContent(old)==normalizeChunkContent(content) { return nil,id,nil }
  if source=="knowledge_doc" || (kind!="fact" && kind!="preference") || len(terms)<2 { continue }
  // Explicitly different topics are independent even with generic keywords.
  if strings.TrimSpace(oldTopic)!=strings.TrimSpace(topic) { continue }
  prior:=keywordSet(keys); common:=0; for key:=range terms { if prior[key] { common++ } }
  union:=len(terms)+len(prior)-common
  if common>=2 && union>0 && float64(common)/float64(union)>=0.75 { ids=append(ids,id) }
 }
 return ids,0,rows.Err()
}

func saveVersionedChunk(conversationID int64,scope,sourceType string,sourceDocID sql.NullInt64,content,summary,keywords string,meta ChunkMetadata) (int64,error) {
 if scope=="" { scope=ScopeConversation }; if scope!=ScopeGlobal&&scope!=ScopeConversation { return 0,fmt.Errorf("invalid memory scope") }
 if sourceType=="" { sourceType=ScopeConversation }
 if strings.TrimSpace(content)=="" { return 0,fmt.Errorf("empty memory content") }
 kind,err:=memoryTypeFor(sourceType,meta); if err!=nil { return 0,err }
 confidence:=1.0; if meta.Confidence!=nil { confidence=*meta.Confidence }
 if math.IsNaN(confidence)||math.IsInf(confidence,0)||confidence<0||confidence>1 { return 0,fmt.Errorf("confidence must be in [0,1]") }
 assistant:=ConversationAssistant(conversationID)
 tx,err:=db.DB.Begin(); if err!=nil { return 0,err }; defer tx.Rollback()
 // Obtain SQLite's writer lock before reading candidates, serializing writers.
 if _,err=tx.Exec(`UPDATE memory_chunks SET id=id WHERE id=-1`);err!=nil { return 0,err }
 conflicts,duplicate,err:=chunkConflicts(tx,conversationID,assistant,scope,sourceType,kind,content,keywords,meta.TopicLabel)
 if err!=nil { return 0,err }; if duplicate>0 { return duplicate,tx.Commit() }
 result,err:=tx.Exec(`INSERT INTO memory_chunks (conversation_id,assistant,scope,source_doc_id,source_type,content,summary,keywords,time_start,time_end,emotion,correction,topic_label,is_correction,importance,last_accessed_at,source_conversation_id,memory_type,confidence,updated_at)
 VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,CURRENT_TIMESTAMP,?,?,?,CURRENT_TIMESTAMP)`,
 conversationID,assistant,scope,sourceDocID,sourceType,content,summary,keywords,meta.TimeStart,meta.TimeEnd,meta.Emotion,meta.Correction,meta.TopicLabel,boolInt(meta.IsCorrection),normalizeImportance(meta.Importance),conversationID,kind,confidence)
 if err!=nil { return 0,err }; id,err:=result.LastInsertId();if err!=nil { return 0,err }
 for _,old:=range conflicts { if _,err=tx.Exec(`UPDATE memory_chunks SET status='superseded',active=0,superseded_by=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,id,old);err!=nil { return 0,err } }
 return id,tx.Commit()
}
