package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReadDoesNotWaitBehindOpenWriteTransaction(t *testing.T) {
	Init(filepath.Join(t.TempDir(), "concurrent.db"))
	t.Cleanup(func() { _ = DB.Close() })
	tx, err := DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO preferences(key,value) VALUES('held-write','1')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var count int
	if err := DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM preferences`).Scan(&count); err != nil {
		t.Fatalf("read waited behind write transaction: %v", err)
	}
}

func TestMemoryChunkIndexesAndForeignKeys(t *testing.T) {
	Init(filepath.Join(t.TempDir(), "schema.db"))
	t.Cleanup(func() { _ = DB.Close() })

	indexes := map[string]bool{}
	rows, err := DB.Query(`PRAGMA index_list(memory_chunks)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	rows.Close()
	for _, name := range []string{"idx_memory_chunks_scope_conversation", "idx_memory_chunks_source", "idx_memory_chunks_accessed", "idx_memory_chunks_topic_correction"} {
		if !indexes[name] {
			t.Fatalf("missing index %s", name)
		}
	}

	parents := map[string]bool{}
	rows, err = DB.Query(`PRAGMA foreign_key_list(memory_chunks)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		parents[from+":"+table] = true
	}
	rows.Close()
	if !parents["conversation_id:conversations"] || !parents["source_doc_id:knowledge_docs"] {
		t.Fatalf("missing memory chunk foreign keys: %#v", parents)
	}
}

func TestAllMessagesViewAndAttachmentForeignKeys(t *testing.T) {
	Init(filepath.Join(t.TempDir(), "messages.db"))
	t.Cleanup(func() { _ = DB.Close() })
	if _, err := DB.Exec(`INSERT INTO messages(id,conversation_id,role,content) VALUES(10,1,'user','live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO messages_archive(id,conversation_id,role,content) VALUES(11,1,'assistant','archived')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM all_messages WHERE id IN (10,11)`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("all_messages count=%d err=%v", count, err)
	}
	rows, err := DB.Query(`PRAGMA foreign_key_list(message_attachments)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if from == "message_id" {
			t.Fatalf("message_id must not point to a single physical message table: %s", table)
		}
	}
}
