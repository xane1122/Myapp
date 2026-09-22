package db

import "log"

func ensureMemoryUpgradeSchema() {
 addColumnIfMissing("memory_chunks","status",`TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','superseded','revoked'))`)
 addColumnIfMissing("memory_chunks","source_conversation_id",`INTEGER REFERENCES conversations(id) ON DELETE SET NULL`)
 addColumnIfMissing("memory_chunks","memory_type",`TEXT NOT NULL DEFAULT 'fact' CHECK(memory_type IN ('fact','preference','diary','emotion','summary'))`)
 addColumnIfMissing("memory_chunks","confidence",`REAL NOT NULL DEFAULT 1 CHECK(confidence>=0 AND confidence<=1)`)
 addColumnIfMissing("memory_chunks","updated_at",`TEXT NOT NULL DEFAULT ''`)
 var exists int
 if err:=DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='memory_chunks_fts'`).Scan(&exists); err!=nil { log.Fatal(err) }
 statements:=[]string{
  `UPDATE memory_chunks SET status=CASE WHEN superseded_by IS NOT NULL THEN 'superseded' ELSE 'revoked' END WHERE active=0 AND status='active'`,
  `UPDATE memory_chunks SET source_conversation_id=conversation_id WHERE source_conversation_id IS NULL AND updated_at=''`,
  `UPDATE memory_chunks SET memory_type=CASE WHEN source_type='conversation' THEN 'summary' WHEN source_type='diary' THEN 'diary' WHEN source_type='emotion' THEN 'emotion' ELSE memory_type END,updated_at=COALESCE(created_at,CURRENT_TIMESTAMP) WHERE updated_at=''`,
  `CREATE INDEX IF NOT EXISTS idx_memory_status_source ON memory_chunks(status,assistant,source_conversation_id,memory_type)`,
  `CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks_fts USING fts5(content,summary,keywords,topic_label,content='memory_chunks',content_rowid='id',tokenize='trigram')`,
  `CREATE TRIGGER IF NOT EXISTS memory_initial_metadata AFTER INSERT ON memory_chunks BEGIN UPDATE memory_chunks SET source_conversation_id=COALESCE(new.source_conversation_id,new.conversation_id),updated_at=CASE WHEN new.updated_at='' THEN CURRENT_TIMESTAMP ELSE new.updated_at END WHERE id=new.id; END`,
  `CREATE TRIGGER IF NOT EXISTS memory_fts_insert AFTER INSERT ON memory_chunks BEGIN INSERT INTO memory_chunks_fts(rowid,content,summary,keywords,topic_label) VALUES(new.id,new.content,new.summary,new.keywords,new.topic_label); END`,
  `CREATE TRIGGER IF NOT EXISTS memory_fts_delete AFTER DELETE ON memory_chunks BEGIN INSERT INTO memory_chunks_fts(memory_chunks_fts,rowid,content,summary,keywords,topic_label) VALUES('delete',old.id,old.content,old.summary,old.keywords,old.topic_label); END`,
  `CREATE TRIGGER IF NOT EXISTS memory_fts_update AFTER UPDATE OF content,summary,keywords,topic_label ON memory_chunks BEGIN INSERT INTO memory_chunks_fts(memory_chunks_fts,rowid,content,summary,keywords,topic_label) VALUES('delete',old.id,old.content,old.summary,old.keywords,old.topic_label); INSERT INTO memory_chunks_fts(rowid,content,summary,keywords,topic_label) VALUES(new.id,new.content,new.summary,new.keywords,new.topic_label); END`,
  `CREATE TRIGGER IF NOT EXISTS memory_status_active AFTER UPDATE OF status ON memory_chunks WHEN new.status<>old.status BEGIN UPDATE memory_chunks SET active=CASE WHEN new.status='active' THEN 1 ELSE 0 END,updated_at=CURRENT_TIMESTAMP WHERE id=new.id; END`,
  `CREATE TRIGGER IF NOT EXISTS memory_active_status AFTER UPDATE OF active,superseded_by ON memory_chunks WHEN (new.active<>old.active OR new.superseded_by IS NOT old.superseded_by) AND new.status=old.status BEGIN UPDATE memory_chunks SET status=CASE WHEN new.active=1 THEN 'active' WHEN new.superseded_by IS NOT NULL THEN 'superseded' ELSE 'revoked' END,updated_at=CURRENT_TIMESTAMP WHERE id=new.id; END`,
  `CREATE TRIGGER IF NOT EXISTS memory_updated_at AFTER UPDATE OF content,summary,keywords,memory_type,confidence ON memory_chunks BEGIN UPDATE memory_chunks SET updated_at=CURRENT_TIMESTAMP WHERE id=new.id; END`,
  `CREATE TABLE IF NOT EXISTS request_stats (id INTEGER PRIMARY KEY AUTOINCREMENT,requested_at TEXT NOT NULL,conversation_id INTEGER,flow_id TEXT NOT NULL DEFAULT '',record_kind TEXT NOT NULL CHECK(record_kind IN ('model','chat')),input_tokens INTEGER NOT NULL DEFAULT 0,output_tokens INTEGER NOT NULL DEFAULT 0,cache_hit_tokens INTEGER,usage_reported INTEGER NOT NULL DEFAULT 0,cache_complete INTEGER NOT NULL DEFAULT 0,retrieval_ms INTEGER NOT NULL DEFAULT 0,total_latency_ms INTEGER NOT NULL DEFAULT 0,model TEXT NOT NULL,request_count INTEGER NOT NULL DEFAULT 1,failed INTEGER NOT NULL DEFAULT 0)`,
  `CREATE INDEX IF NOT EXISTS idx_request_stats_flow ON request_stats(flow_id,record_kind,requested_at)`,
 }
 for _,sql:=range statements { if _,err:=DB.Exec(sql); err!=nil { log.Fatalf("memory/stats migration failed (build with -tags sqlite_fts5): %v",err) } }
 if exists==0 { if _,err:=DB.Exec(`INSERT INTO memory_chunks_fts(memory_chunks_fts) VALUES('rebuild')`); err!=nil { log.Fatal(err) } }
}
