package memory

import (
 "database/sql"
 "fmt"
 "strings"
 "myapp/internal/db"
)

type MemoryFilter struct { ConversationID int64; Scope,MemoryType,Status string }

func (f MemoryFilter) where() (string,[]interface{},error) {
 scope:=f.Scope;if scope=="" { if f.ConversationID>0{scope=ScopeConversation}else{scope="all"} }
 if scope!="all"&&scope!=ScopeConversation&&scope!=ScopeGlobal {return "",nil,fmt.Errorf("invalid scope")}
 parts:=[]string{"1=1"};args:=[]interface{}{}
 if f.ConversationID>0 {
  conversation,err:=GetConversation(f.ConversationID);if err!=nil{return "",nil,err}
  parts=append(parts,"assistant=?");args=append(args,conversation.Assistant)
  if scope=="all" {parts=append(parts,"(scope='global' OR conversation_id=?)");args=append(args,f.ConversationID)
  } else if scope==ScopeGlobal {parts=append(parts,"scope='global'")
  } else {parts=append(parts,"scope='conversation' AND conversation_id=?");args=append(args,f.ConversationID)}
 } else if scope!="all" {parts=append(parts,"scope=?");args=append(args,scope)}
 if f.MemoryType!="" {if _,err:=memoryTypeFor("",ChunkMetadata{MemoryType:f.MemoryType});err!=nil{return "",nil,err};parts=append(parts,"memory_type=?");args=append(args,f.MemoryType)}
 if f.Status!=""&&f.Status!="all" {if f.Status!="active"&&f.Status!="superseded"&&f.Status!="revoked"{return "",nil,fmt.Errorf("invalid status")};parts=append(parts,"status=?");args=append(args,f.Status)}
 return strings.Join(parts," AND "),args,nil
}

func ExportMemories(f MemoryFilter) ([]Chunk,error) {
 where,args,err:=f.where();if err!=nil{return nil,err}
 rows,err:=db.DB.Query(`SELECT `+chunkSelectColumns+` FROM memory_chunks WHERE `+where+` ORDER BY id`,args...)
 if err!=nil{return nil,err};defer rows.Close()
 chunks:=[]Chunk{};for rows.Next(){c,err:=scanChunk(rows);if err!=nil{return nil,err};chunks=append(chunks,c)}
 return chunks,rows.Err()
}

// Deletion revokes a memory and retains its version audit trail for export.
func RevokeMemories(f MemoryFilter,id int64) (int64,error) {
 where,args,err:=f.where();if err!=nil{return 0,err}
 if id>0 {where+=" AND id=?";args=append(args,id)}
 result,err:=db.DB.Exec(`UPDATE memory_chunks SET status='revoked',active=0,updated_at=CURRENT_TIMESTAMP WHERE `+where+` AND status<>'revoked'`,args...)
 if err!=nil{return 0,err};count,err:=result.RowsAffected();if id>0&&count==0{return 0,sql.ErrNoRows};return count,err
}

func ClearAllMemories(confirm string) (int64,error) {
 if confirm!="DELETE_ALL_MEMORIES" {return 0,fmt.Errorf("confirm must equal DELETE_ALL_MEMORIES")}
 tx,err:=db.DB.Begin();if err!=nil{return 0,err};defer tx.Rollback()
 result,err:=tx.Exec(`DELETE FROM memory_chunks`);if err!=nil{return 0,err}
 n,err:=result.RowsAffected();if err!=nil{return 0,err};return n,tx.Commit()
}
