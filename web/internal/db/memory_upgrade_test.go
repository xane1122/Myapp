package db

import (
 "path/filepath"
 "testing"
)

func TestMemoryUpgradeIsIdempotentAndIndexesUpdates(t *testing.T) {
 Init(filepath.Join(t.TempDir(),"upgrade.db"));t.Cleanup(func(){_ = DB.Close();DB=nil})
 result,err:=DB.Exec(`INSERT INTO memory_chunks(conversation_id,content,summary,keywords) VALUES(1,'咖啡杯测试内容','测试摘要','咖啡杯')`);if err!=nil{t.Fatal(err)}
 id,_:=result.LastInsertId()
 ensureMemoryUpgradeSchema();ensureMemoryUpgradeSchema()
 var count int
 if err:=DB.QueryRow(`SELECT COUNT(*) FROM memory_chunks_fts WHERE memory_chunks_fts MATCH '"咖啡杯"'`).Scan(&count);err!=nil||count!=1{t.Fatalf("initial index %d %v",count,err)}
 if _,err:=DB.Exec(`UPDATE memory_chunks SET content='玻璃杯更新内容',keywords='玻璃杯' WHERE id=?`,id);err!=nil{t.Fatal(err)}
 if err:=DB.QueryRow(`SELECT COUNT(*) FROM memory_chunks_fts WHERE memory_chunks_fts MATCH '"咖啡杯"'`).Scan(&count);err!=nil||count!=0{t.Fatalf("stale index %d %v",count,err)}
 if err:=DB.QueryRow(`SELECT COUNT(*) FROM memory_chunks_fts WHERE memory_chunks_fts MATCH '"玻璃杯"'`).Scan(&count);err!=nil||count!=1{t.Fatalf("updated index %d %v",count,err)}
 if _,err:=DB.Exec(`UPDATE memory_chunks SET confidence=1.1 WHERE id=?`,id);err==nil{t.Fatal("invalid confidence accepted")}
 if _,err:=DB.Exec(`UPDATE memory_chunks SET active=0,superseded_by=999 WHERE id=?`,id);err!=nil{t.Fatal(err)}
 var status string;if err:=DB.QueryRow(`SELECT status FROM memory_chunks WHERE id=?`,id).Scan(&status);err!=nil||status!="superseded"{t.Fatalf("legacy active status %s %v",status,err)}
 if _,err:=DB.Exec(`DELETE FROM memory_chunks WHERE id=?`,id);err!=nil{t.Fatal(err)}
 if err:=DB.QueryRow(`SELECT COUNT(*) FROM memory_chunks_fts WHERE memory_chunks_fts MATCH '"玻璃杯"'`).Scan(&count);err!=nil||count!=0{t.Fatalf("deleted index %d %v",count,err)}
}
