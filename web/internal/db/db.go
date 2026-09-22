package db

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

func Init(path string) {
	var err error
	DB, err = sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		log.Fatalf("无法打开数据库: %v", err)
	}
	// WAL serializes writers while allowing reads to proceed during background
	// maintenance. A one-connection pool makes every HTTP read wait behind any
	// long-running transaction.
	DB.SetMaxOpenConns(8)
	DB.SetMaxIdleConns(8)
	migrate()
}

func migrate() {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS roleplay_generation_tasks (
			request_id TEXT PRIMARY KEY, fingerprint BLOB NOT NULL, line_id INTEGER NOT NULL,
			chapter_index INTEGER NOT NULL, mode TEXT NOT NULL, source_updated_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('pending','running','done','failed')),
			response_json TEXT, error_text TEXT NOT NULL DEFAULT '', error_code INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_active_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_roleplay_generation_tasks_status_active ON roleplay_generation_tasks(status,last_active_at)`,
		`CREATE TABLE IF NOT EXISTS roleplay_generation_results (
			request_id TEXT PRIMARY KEY,
			fingerprint BLOB NOT NULL,
			response_json TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_roleplay_generation_results_created_at ON roleplay_generation_results(created_at)`,
		`CREATE TABLE IF NOT EXISTS roleplay_generation_deliveries (
			request_id TEXT PRIMARY KEY,
			line_id INTEGER NOT NULL,
			chapter_index INTEGER NOT NULL,
			source_updated_at TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			claimed_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_roleplay_generation_deliveries_unclaimed ON roleplay_generation_deliveries(claimed_at,created_at)`,
		`CREATE TABLE IF NOT EXISTS training_profile (id INTEGER PRIMARY KEY CHECK(id=1), role_name TEXT NOT NULL DEFAULT '小宝', level INTEGER NOT NULL DEFAULT 1, xp INTEGER NOT NULL DEFAULT 0, xp_to_next INTEGER NOT NULL DEFAULT 500, streak_days INTEGER NOT NULL DEFAULT 0, last_active TEXT, total_points INTEGER NOT NULL DEFAULT 0, daily_earned INTEGER NOT NULL DEFAULT 0, daily_spent INTEGER NOT NULL DEFAULT 0, today_status TEXT NOT NULL DEFAULT '待开始')`,
		`CREATE TABLE IF NOT EXISTS training_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT 'daily', category TEXT NOT NULL DEFAULT 'obedience', difficulty INTEGER NOT NULL DEFAULT 1, time_limit INTEGER NOT NULL DEFAULT 0, reward_points INTEGER NOT NULL DEFAULT 0, failure_penalty TEXT NOT NULL DEFAULT '', assigned_date TEXT NOT NULL DEFAULT (date('now')), deadline_at TEXT, status TEXT NOT NULL DEFAULT 'pending', completed_at DATETIME, review TEXT NOT NULL DEFAULT '', score INTEGER, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_training_tasks_date ON training_tasks(assigned_date)`,
		`CREATE TABLE IF NOT EXISTS training_punishments (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER, reason TEXT NOT NULL DEFAULT '', content TEXT NOT NULL, severity INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'pending', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, executed_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS training_reviews (id INTEGER PRIMARY KEY AUTOINCREMENT, review_date TEXT NOT NULL UNIQUE, completion_rate INTEGER NOT NULL DEFAULT 0, comment TEXT NOT NULL, score INTEGER NOT NULL DEFAULT 5, tier TEXT NOT NULL DEFAULT 'C', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS training_badges (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', icon_char TEXT NOT NULL DEFAULT '🏆', unlocked INTEGER NOT NULL DEFAULT 0, unlocked_at TEXT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS training_progress (id INTEGER PRIMARY KEY AUTOINCREMENT, date TEXT NOT NULL UNIQUE, total_tasks INTEGER NOT NULL DEFAULT 0, completed_tasks INTEGER NOT NULL DEFAULT 0, failed_tasks INTEGER NOT NULL DEFAULT 0, streak_days INTEGER NOT NULL DEFAULT 0, total_points INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS cabinet_items (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, category TEXT NOT NULL DEFAULT 'game_reward', source TEXT NOT NULL DEFAULT '', quantity INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS ludo_games (game_id TEXT PRIMARY KEY, player_pos INTEGER NOT NULL DEFAULT 1 CHECK(player_pos BETWEEN 1 AND 51), finished INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS ludo_reward_claims (game_id TEXT NOT NULL, tile_id INTEGER NOT NULL, points INTEGER NOT NULL DEFAULT 0, cabinet_item TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(game_id,tile_id))`,
		`CREATE TABLE IF NOT EXISTS native_reply_jobs (
			job_id TEXT PRIMARY KEY,
			idempotency_key_hash TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','completed','failed')),
			conversation_id INTEGER NOT NULL DEFAULT 0,
			assistant_message_id INTEGER NOT NULL DEFAULT 0,
			user_message_id INTEGER NOT NULL DEFAULT 0,
			user_message_ids_json TEXT NOT NULL DEFAULT '[]',
			assistant TEXT NOT NULL DEFAULT '',
			error_text TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_native_reply_jobs_status ON native_reply_jobs(status,expires_at)`,
		// 对话会话
		`CREATE TABLE IF NOT EXISTS conversations (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			title      TEXT NOT NULL DEFAULT '新对话',
			assistant  TEXT NOT NULL DEFAULT 'rhys',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 对话历史
		`CREATE TABLE IF NOT EXISTS messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL DEFAULT 1,
			role       TEXT    NOT NULL,
			content    TEXT    NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			memory_archived_at DATETIME,
			reply_to_message_id INTEGER,
			quote_role TEXT NOT NULL DEFAULT '',
			quote_text TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(conversation_id) REFERENCES conversations(id)
		)`,
		// 长期记忆块（压缩后的对话摘要）
		`CREATE TABLE IF NOT EXISTS memory_chunks (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL DEFAULT 1,
			scope      TEXT NOT NULL DEFAULT 'conversation',
			source_doc_id INTEGER,
			source_type TEXT NOT NULL DEFAULT 'conversation',
			content    TEXT NOT NULL,
			summary    TEXT,
			keywords   TEXT,
			time_start DATETIME,
			time_end DATETIME,
			emotion TEXT NOT NULL DEFAULT '',
			correction TEXT NOT NULL DEFAULT '',
			topic_label TEXT NOT NULL DEFAULT '',
			is_correction INTEGER NOT NULL DEFAULT 0,
			importance TEXT NOT NULL DEFAULT 'medium',
			active INTEGER NOT NULL DEFAULT 1,
			version INTEGER NOT NULL DEFAULT 1,
			superseded_by INTEGER,
			last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id),
			FOREIGN KEY(source_doc_id) REFERENCES knowledge_docs(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS global_memory_candidates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL,
			user_message TEXT NOT NULL,
			assistant_message TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','done','dead')),
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			next_attempt_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			processed_at DATETIME,
			failed_at DATETIME,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_global_memory_candidates_pending
		 ON global_memory_candidates(conversation_id,status,id)`,
		`CREATE TABLE IF NOT EXISTS model_channel_configs (
			channel TEXT PRIMARY KEY CHECK(channel IN ('reply','grok','memory','memory_rhys','memory_grok','roleplay')),
			base_url TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			assistant_name TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS world_book_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entry_name TEXT NOT NULL,
			keywords TEXT NOT NULL,
			secondary_keywords TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			injection_position TEXT NOT NULL DEFAULT 'before_user_message'
				CHECK(injection_position IN ('after_system_prompt','before_user_message','before_last_message','author_note_depth')),
			depth INTEGER NOT NULL DEFAULT 0 CHECK(depth >= 0),
			priority INTEGER NOT NULL DEFAULT 50,
			token_budget INTEGER NOT NULL DEFAULT 0 CHECK(token_budget >= 0),
			enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS world_book_activation_state (
			conversation_id INTEGER NOT NULL,
			entry_id INTEGER NOT NULL,
			sticky_until_turn INTEGER NOT NULL DEFAULT 0,
			cooldown_until_turn INTEGER NOT NULL DEFAULT 0,
			last_activated_turn INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY(conversation_id,entry_id),
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
			FOREIGN KEY(entry_id) REFERENCES world_book_entries(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_world_book_enabled_priority ON world_book_entries(enabled,priority,id)`,
		`CREATE TABLE IF NOT EXISTS messages_archive (
			id INTEGER PRIMARY KEY,
			conversation_id INTEGER NOT NULL DEFAULT 1,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			memory_archived_at DATETIME
			,reply_to_message_id INTEGER
			,quote_role TEXT NOT NULL DEFAULT ''
			,quote_text TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS virtual_transfers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL UNIQUE,
			sender_role TEXT NOT NULL CHECK(sender_role IN ('user','assistant')),
			recipient_role TEXT NOT NULL CHECK(recipient_role IN ('user','assistant')),
			amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
			note TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','received')),
			idempotency_key TEXT NOT NULL DEFAULT '',
			received_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_virtual_transfers_conversation_message ON virtual_transfers(conversation_id,message_id)`,
		`CREATE TABLE IF NOT EXISTS saved_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id INTEGER NOT NULL UNIQUE,
			conversation_id INTEGER NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_saved_messages_time ON saved_messages(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS message_feedback (
			message_id INTEGER PRIMARY KEY,
			conversation_id INTEGER NOT NULL,
			value INTEGER NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_archive_conv ON messages_archive(conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_archive_created ON messages_archive(created_at)`,
		// 人物设定
		`CREATE TABLE IF NOT EXISTS persona_profiles (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			title       TEXT NOT NULL,
			content     TEXT NOT NULL,
			source_path TEXT,
			active      INTEGER NOT NULL DEFAULT 0,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 全局知识文档
		`CREATE TABLE IF NOT EXISTS knowledge_docs (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			title        TEXT NOT NULL,
			source_path  TEXT,
			content_text TEXT,
			status       TEXT NOT NULL DEFAULT 'pending',
			error        TEXT,
			created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 当前会话图片附件
		`CREATE TABLE IF NOT EXISTS message_attachments (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL DEFAULT 1,
			message_id      INTEGER,
			kind            TEXT NOT NULL DEFAULT 'image',
			original_name   TEXT,
			file_path       TEXT NOT NULL,
			url             TEXT NOT NULL,
			mime_type       TEXT NOT NULL,
			size_bytes      INTEGER NOT NULL DEFAULT 0,
			content_hash    TEXT NOT NULL DEFAULT '',
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id),
			FOREIGN KEY(message_id) REFERENCES messages(id)
		)`,
		// 表情包库
		`CREATE TABLE IF NOT EXISTS stickers (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT NOT NULL,
			file_path   TEXT NOT NULL,
			url         TEXT NOT NULL,
			mime_type   TEXT NOT NULL,
			tags        TEXT,
			description TEXT,
			mood        TEXT,
			enabled     INTEGER NOT NULL DEFAULT 1,
			needs_review INTEGER NOT NULL DEFAULT 0,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 用户偏好设置
		`CREATE TABLE IF NOT EXISTS preferences (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS wake_activity (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			conversation_id INTEGER NOT NULL DEFAULT 1,
			last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			source TEXT NOT NULL DEFAULT 'web',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS wake_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL,
			decision TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			message_id INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wake_events_time ON wake_events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_wake_events_conversation ON wake_events(conversation_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS wake_model_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wake_event_id INTEGER UNIQUE,
			conversation_id INTEGER NOT NULL,
			manual INTEGER NOT NULL DEFAULT 0,
			outcome TEXT NOT NULL DEFAULT 'started',
			completed_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wake_model_attempts_time ON wake_model_attempts(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS wake_secrets (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL UNIQUE,
			keys_json TEXT NOT NULL DEFAULT '{}',
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			user_agent TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS bells (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'mine' CHECK(kind IN ('mine','ai')),
			frequency TEXT NOT NULL DEFAULT 'daily',
			reminder_time TEXT NOT NULL DEFAULT '09:00',
			push_target TEXT NOT NULL DEFAULT 'me' CHECK(push_target IN ('me','ai','both')),
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS bell_checkins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bell_id INTEGER NOT NULL,
			checkin_date TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(bell_id, checkin_date),
			FOREIGN KEY(bell_id) REFERENCES bells(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bell_checkins_date ON bell_checkins(checkin_date)`,
		`CREATE TABLE IF NOT EXISTS bell_deliveries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bell_id INTEGER NOT NULL,
			scheduled_at TEXT NOT NULL,
			delivered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(bell_id, scheduled_at),
			FOREIGN KEY(bell_id) REFERENCES bells(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bell_deliveries_time ON bell_deliveries(scheduled_at)`,
		`CREATE TABLE IF NOT EXISTS mailbox_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sender TEXT NOT NULL,
			content TEXT NOT NULL CHECK(length(content) BETWEEN 1 AND 200),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_messages_time ON mailbox_messages(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS days_matter_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_name TEXT NOT NULL,
			event_date TEXT NOT NULL,
			direction TEXT NOT NULL DEFAULT 'count_up' CHECK(direction IN ('count_up','count_down')),
			image_url TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			is_favorite INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_days_matter_date ON days_matter_events(event_date)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_days_matter_single_favorite ON days_matter_events(is_favorite) WHERE is_favorite=1`,
		`CREATE TABLE IF NOT EXISTS notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL CHECK(length(content) BETWEEN 1 AND 500),
			tags TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','done','archived')),
			conversation_id INTEGER,
			author TEXT NOT NULL DEFAULT 'user' CHECK(author IN ('user','ai')),
			pinned INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS album_photos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uploader TEXT NOT NULL CHECK(uploader IN ('Xane','Rhys')),
			file_path TEXT NOT NULL,
			thumb_path TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			width INTEGER NOT NULL DEFAULT 0,
			height INTEGER NOT NULL DEFAULT 0,
			mime_type TEXT NOT NULL DEFAULT 'image/jpeg',
			caption TEXT NOT NULL DEFAULT '',
			is_favorite INTEGER NOT NULL DEFAULT 0,
			source_type TEXT NOT NULL DEFAULT 'upload',
			source_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_album_photos_created ON album_photos(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_album_photos_uploader ON album_photos(uploader)`,
		`CREATE INDEX IF NOT EXISTS idx_album_photos_favorite ON album_photos(is_favorite) WHERE is_favorite=1`,
		`CREATE INDEX IF NOT EXISTS idx_notes_list ON notes(deleted,status,pinned DESC,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_conversation ON notes(conversation_id)`,
		`CREATE TABLE IF NOT EXISTS ledger_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entry_date TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'expense' CHECK(kind IN ('income','expense')),
			amount_cents INTEGER NOT NULL CHECK(amount_cents >= 0),
			category TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			author TEXT NOT NULL DEFAULT 'user' CHECK(author IN ('user','ai')),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ledger_entries_date ON ledger_entries(entry_date DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS daily_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			log_date TEXT NOT NULL UNIQUE,
			content TEXT NOT NULL,
			mood TEXT NOT NULL DEFAULT '',
			author TEXT NOT NULL DEFAULT 'user' CHECK(author IN ('user','ai')),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS ai_diary_state (
			conversation_id INTEGER PRIMARY KEY,
			window_started_at DATETIME NOT NULL,
			last_generated_at DATETIME,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS ai_diaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL,
			diary_date TEXT NOT NULL,
			content TEXT NOT NULL,
			trigger_type TEXT NOT NULL CHECK(trigger_type IN ('manual','automatic')),
			window_started_at DATETIME NOT NULL,
			window_ended_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_diaries_conversation_date ON ai_diaries(conversation_id,diary_date DESC,window_ended_at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS tool_activity (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL DEFAULT 0,
			call_id TEXT NOT NULL DEFAULT '',
			tool_name TEXT NOT NULL,
			arguments TEXT NOT NULL DEFAULT '',
			result_summary TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'started' CHECK(status IN ('started','completed','failed')),
			started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_activity_started ON tool_activity(started_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_activity_conversation ON tool_activity(conversation_id,started_at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS notebook_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '[]',
			author TEXT NOT NULL DEFAULT 'user' CHECK(author IN ('user','ai')),
			pinned INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notebook_entries_list ON notebook_entries(pinned DESC,updated_at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS galgame_stories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			cover_image TEXT NOT NULL DEFAULT '',
			start_node_key TEXT NOT NULL DEFAULT 'start',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS galgame_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			story_id INTEGER NOT NULL REFERENCES galgame_stories(id) ON DELETE CASCADE,
			node_key TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			options_json TEXT NOT NULL DEFAULT '[]',
			is_ending INTEGER NOT NULL DEFAULT 0,
			ending_name TEXT NOT NULL DEFAULT '',
			UNIQUE(story_id,node_key)
		)`,
		`CREATE TABLE IF NOT EXISTS galgame_saves (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			story_id INTEGER NOT NULL REFERENCES galgame_stories(id) ON DELETE CASCADE,
			node_key TEXT NOT NULL,
			flags_json TEXT NOT NULL DEFAULT '{}',
			is_auto INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_galgame_saves_story_created ON galgame_saves(story_id,created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS roleplay_state (
			id INTEGER PRIMARY KEY CHECK(id=1),
			lines_json TEXT NOT NULL DEFAULT '[]',
			templates_json TEXT NOT NULL DEFAULT '[]',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS roleplay_lines (
			id INTEGER PRIMARY KEY,
			line_json TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			migrated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			log.Fatalf("建表失败: %v\nSQL: %s", err, s)
		}
	}
	if _, err := DB.Exec(`INSERT OR IGNORE INTO wake_model_attempts(wake_event_id,conversation_id,manual,outcome,completed_at,created_at)
		SELECT id,conversation_id,0,decision,created_at,created_at
		FROM wake_events
		WHERE NOT (decision='wait' AND reason LIKE '概率门未通过%')
		  AND NOT (decision='error' AND reason LIKE '%没有可用的 API Key%')`); err != nil {
		log.Fatalf("迁移自主唤醒模型判断记录失败: %v", err)
	}
	migrateRoleplayLines()
	addColumnIfMissing("training_profile", "xp", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("training_profile", "xp_to_next", "INTEGER NOT NULL DEFAULT 500")
	addColumnIfMissing("training_profile", "streak_days", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("training_profile", "last_active", "TEXT")
	addColumnIfMissing("training_profile", "daily_earned", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("training_profile", "daily_spent", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("training_tasks", "time_limit", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("training_tasks", "failure_penalty", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("training_punishments", "severity", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("training_punishments", "updated_at", "DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP")
	addColumnIfMissing("training_reviews", "completion_rate", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("training_reviews", "tier", "TEXT NOT NULL DEFAULT 'C'")
	addColumnIfMissing("training_reviews", "updated_at", "DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP")
	addColumnIfMissing("roleplay_generation_tasks", "source_updated_at", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("roleplay_generation_deliveries", "source_updated_at", "TEXT NOT NULL DEFAULT ''")
	if _, err := DB.Exec(`INSERT OR IGNORE INTO training_profile(id) VALUES(1)`); err != nil {
		log.Fatalf("初始化调教室资料失败: %v", err)
	}
	addColumnIfMissing("messages", "conversation_id", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("conversations", "assistant", "TEXT NOT NULL DEFAULT 'rhys'")
	addColumnIfMissing("messages", "memory_archived_at", "DATETIME")
	addColumnIfMissing("messages", "reply_to_message_id", "INTEGER")
	addColumnIfMissing("messages", "quote_role", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("messages", "quote_text", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("messages_archive", "reply_to_message_id", "INTEGER")
	addColumnIfMissing("messages_archive", "quote_role", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("messages_archive", "quote_text", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("world_book_entries", "entry_type", "TEXT NOT NULL DEFAULT 'keyword'")
	addColumnIfMissing("world_book_entries", "match_whole_words", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("world_book_entries", "secondary_logic", "TEXT NOT NULL DEFAULT 'and_all'")
	addColumnIfMissing("world_book_entries", "sticky_rounds", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("world_book_entries", "cooldown_rounds", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("world_book_entries", "group_name", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("world_book_entries", "group_competition", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("world_book_entries", "exclude_recursion", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("world_book_entries", "prevent_recursion", "INTEGER NOT NULL DEFAULT 1")
	migrateWorldBookPrioritySemantics()
	addColumnIfMissing("virtual_transfers", "idempotency_key", "TEXT NOT NULL DEFAULT ''")
	if _, err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_virtual_transfers_idempotency ON virtual_transfers(conversation_id,sender_role,idempotency_key) WHERE idempotency_key != ''`); err != nil {
		log.Fatalf("创建转账幂等索引失败: %v", err)
	}
	addColumnIfMissing("tool_activity", "assistant_message_id", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("tool_activity", "model_name", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("memory_chunks", "conversation_id", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("memory_chunks", "assistant", "TEXT NOT NULL DEFAULT 'rhys'")
	addColumnIfMissing("memory_chunks", "scope", "TEXT NOT NULL DEFAULT 'conversation'")
	addColumnIfMissing("memory_chunks", "source_doc_id", "INTEGER")
	addColumnIfMissing("memory_chunks", "source_type", "TEXT NOT NULL DEFAULT 'conversation'")
	addColumnIfMissing("memory_chunks", "time_start", "DATETIME")
	addColumnIfMissing("memory_chunks", "time_end", "DATETIME")
	addColumnIfMissing("memory_chunks", "emotion", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("memory_chunks", "correction", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("memory_chunks", "topic_label", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("memory_chunks", "is_correction", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing("memory_chunks", "last_accessed_at", "DATETIME")
	addColumnIfMissing("memory_chunks", "importance", "TEXT NOT NULL DEFAULT 'medium'")
	addColumnIfMissing("memory_chunks", "active", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("memory_chunks", "version", "INTEGER NOT NULL DEFAULT 1")
	addColumnIfMissing("memory_chunks", "superseded_by", "INTEGER")
	ensureGlobalMemoryCandidateSchema()
	ensureModelChannelSchema()
	ensureMailboxSenderSchema()
	addColumnIfMissing("model_channel_configs", "assistant_name", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("bells", "assistant_name", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("push_subscriptions", "user_id", "TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing("push_subscriptions", "keys_json", "TEXT NOT NULL DEFAULT '{}'")
	if _, err := DB.Exec(`UPDATE push_subscriptions SET user_id='legacy-' || id WHERE TRIM(user_id)=''`); err != nil {
		log.Fatalf("迁移推送订阅用户标识失败: %v", err)
	}
	if _, err := DB.Exec(`UPDATE push_subscriptions SET keys_json=json_object('p256dh',p256dh,'auth',auth) WHERE TRIM(keys_json)='' OR keys_json='{}'`); err != nil {
		log.Fatalf("迁移推送订阅密钥字段失败: %v", err)
	}
	if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_push_subscriptions_user ON push_subscriptions(user_id,created_at DESC)`); err != nil {
		log.Fatalf("创建推送订阅用户索引失败: %v", err)
	}
	for _, key := range []string{"user_name", "likes", "dislikes", "boundaries", "memory_policy"} {
		if _, err := DB.Exec(`INSERT OR IGNORE INTO preferences(key,value,updated_at) SELECT ?,value,CURRENT_TIMESTAMP FROM preferences WHERE key=?`, key, key+".rhys"); err != nil {
			log.Printf("迁移共享用户设置 %s 失败: %v", key, err)
		}
	}
	if _, err := DB.Exec(`UPDATE memory_chunks SET last_accessed_at=COALESCE(last_accessed_at, created_at, CURRENT_TIMESTAMP)`); err != nil {
		log.Fatalf("初始化记忆访问时间失败: %v", err)
	}
	ensureMemoryChunkForeignKeys()
	ensureMemoryUpgradeSchema()
	ensureAttachmentMessageViewSchema()
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_scope_conversation ON memory_chunks(scope, conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_source ON memory_chunks(source_type, source_doc_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_accessed ON memory_chunks(last_accessed_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_topic_correction ON memory_chunks(topic_label, is_correction)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_chunks_active_scope ON memory_chunks(active, scope, conversation_id)`,
	} {
		if _, err := DB.Exec(stmt); err != nil {
			log.Fatalf("创建长期记忆索引失败: %v", err)
		}
	}
	addColumnIfMissing("message_attachments", "original_name", "TEXT")
	addColumnIfMissing("message_attachments", "content_hash", "TEXT NOT NULL DEFAULT ''")
	if _, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_attachments_hash ON message_attachments(content_hash)`); err != nil {
		log.Fatalf("创建附件哈希索引失败: %v", err)
	}
	if _, err := DB.Exec(`INSERT OR IGNORE INTO conversations (id, title) VALUES (1, '默认对话')`); err != nil {
		log.Fatalf("初始化默认会话失败: %v", err)
	}
	if _, err := DB.Exec(`UPDATE conversations
		SET updated_at = COALESCE((SELECT MAX(created_at) FROM messages WHERE conversation_id = conversations.id), updated_at)`); err != nil {
		log.Fatalf("更新会话时间失败: %v", err)
	}
	for _, s := range []string{`ALTER TABLE bells ADD COLUMN deadline TEXT NOT NULL DEFAULT ''`, `ALTER TABLE bells ADD COLUMN reminder_interval TEXT NOT NULL DEFAULT ''`} {
		if _, err := DB.Exec(s); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			log.Printf("铃铛字段迁移失败: %v", err)
		}
	}
}

func migrateRoleplayLines() {
	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM roleplay_lines`).Scan(&count); err != nil || count != 0 {
		return
	}
	var linesJSON, stateUpdatedAt string
	if err := DB.QueryRow(`SELECT lines_json,updated_at FROM roleplay_state WHERE id=1`).Scan(&linesJSON, &stateUpdatedAt); err != nil {
		return
	}
	var lines []json.RawMessage
	if err := json.Unmarshal([]byte(linesJSON), &lines); err != nil {
		log.Printf("迁移过家家故事线失败: %v", err)
		return
	}
	tx, err := DB.Begin()
	if err != nil {
		log.Printf("迁移过家家故事线失败: %v", err)
		return
	}
	defer tx.Rollback()
	version := BeijingTimestamp(stateUpdatedAt)
	if version == "" {
		version = time.Now().In(BeijingLocation).Format(time.RFC3339Nano)
	}
	for _, raw := range lines {
		var identity struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil || identity.ID <= 0 {
			log.Printf("跳过无效的过家家故事线")
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO roleplay_lines(id,line_json,updated_at) VALUES(?,?,?)`, identity.ID, string(raw), version); err != nil {
			log.Printf("迁移过家家故事线失败: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("迁移过家家故事线失败: %v", err)
	}
}

func ensureMailboxSenderSchema() {
	var sql string
	if err := DB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='mailbox_messages'`).Scan(&sql); err != nil || !strings.Contains(sql, "sender IN") {
		return
	}
	_, err := DB.Exec(`PRAGMA foreign_keys=OFF;
		CREATE TABLE mailbox_messages_new (id INTEGER PRIMARY KEY AUTOINCREMENT,sender TEXT NOT NULL,content TEXT NOT NULL CHECK(length(content) BETWEEN 1 AND 200),created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO mailbox_messages_new(id,sender,content,created_at) SELECT id,sender,content,created_at FROM mailbox_messages;
		DROP TABLE mailbox_messages;
		ALTER TABLE mailbox_messages_new RENAME TO mailbox_messages;
		CREATE INDEX idx_mailbox_messages_time ON mailbox_messages(created_at DESC);
		PRAGMA foreign_keys=ON;`)
	if err != nil {
		log.Fatalf("迁移留言署名字段失败: %v", err)
	}
}

func migrateWorldBookPrioritySemantics() {
	const key = "world_book_priority_v2_migrated"
	var marker string
	err := DB.QueryRow(`SELECT value FROM preferences WHERE key=?`, key).Scan(&marker)
	if err == nil {
		return
	}
	if err != sql.ErrNoRows {
		log.Fatalf("读取世界书优先级迁移状态失败: %v", err)
	}
	tx, err := DB.Begin()
	if err != nil {
		log.Fatalf("开始世界书优先级迁移失败: %v", err)
	}
	if _, err = tx.Exec(`UPDATE world_book_entries SET priority=100-priority`); err != nil {
		_ = tx.Rollback()
		log.Fatalf("迁移世界书优先级失败: %v", err)
	}
	if _, err = tx.Exec(`UPDATE world_book_entries SET secondary_logic='or_any'`); err != nil {
		_ = tx.Rollback()
		log.Fatalf("迁移世界书次关键词逻辑失败: %v", err)
	}
	if _, err = tx.Exec(`INSERT INTO preferences(key,value,updated_at) VALUES(?,?,CURRENT_TIMESTAMP)`, key, "true"); err != nil {
		_ = tx.Rollback()
		log.Fatalf("记录世界书优先级迁移失败: %v", err)
	}
	if err = tx.Commit(); err != nil {
		log.Fatalf("提交世界书优先级迁移失败: %v", err)
	}
}

func ensureModelChannelSchema() {
	var sql string
	if err := DB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='model_channel_configs'`).Scan(&sql); err != nil || strings.Contains(sql, "'memory_rhys'") {
		return
	}
	tx, err := DB.Begin()
	if err != nil {
		log.Fatalf("开始模型通道表迁移失败: %v", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`CREATE TABLE model_channel_configs_new (
			channel TEXT PRIMARY KEY CHECK(channel IN ('reply','grok','memory','memory_rhys','memory_grok','roleplay')),
			base_url TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
			assistant_name TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '', updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO model_channel_configs_new(channel,base_url,model,api_key,updated_at)
		 SELECT channel,base_url,model,api_key,updated_at FROM model_channel_configs`,
		`DROP TABLE model_channel_configs`,
		`ALTER TABLE model_channel_configs_new RENAME TO model_channel_configs`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			log.Fatalf("迁移模型通道表失败: %v", err)
		}
	}
	if err = tx.Commit(); err != nil {
		log.Fatalf("提交模型通道表迁移失败: %v", err)
	}
	_, _ = DB.Exec(`INSERT OR IGNORE INTO model_channel_configs(channel,base_url,model,assistant_name,api_key,updated_at)
		SELECT 'memory_rhys',base_url,model,'',api_key,updated_at FROM model_channel_configs WHERE channel='memory'`)
	_, _ = DB.Exec(`INSERT OR IGNORE INTO model_channel_configs(channel,base_url,model,assistant_name,api_key,updated_at)
		SELECT 'memory_grok',base_url,model,'',api_key,updated_at FROM model_channel_configs WHERE channel='memory'`)
}

func ensureGlobalMemoryCandidateSchema() {
	var tableSQL string
	if err := DB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='global_memory_candidates'`).Scan(&tableSQL); err != nil {
		log.Fatalf("读取全局记忆候选表结构失败: %v", err)
	}
	if strings.Contains(tableSQL, "'dead'") {
		addColumnIfMissing("global_memory_candidates", "next_attempt_at", "DATETIME")
		addColumnIfMissing("global_memory_candidates", "failed_at", "DATETIME")
		addColumnIfMissing("global_memory_candidates", "model", "TEXT NOT NULL DEFAULT ''")
		return
	}
	tx, err := DB.Begin()
	if err != nil {
		log.Fatalf("开始全局记忆候选表迁移失败: %v", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`CREATE TABLE global_memory_candidates_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL,
			user_message TEXT NOT NULL,
			assistant_message TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','done','dead')),
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			next_attempt_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			processed_at DATETIME,
			failed_at DATETIME,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`INSERT INTO global_memory_candidates_new
			(id,conversation_id,user_message,assistant_message,model,status,attempts,last_error,created_at,processed_at)
		 SELECT id,conversation_id,user_message,assistant_message,'',
			CASE WHEN status='processing' THEN 'pending' ELSE status END,attempts,last_error,created_at,processed_at
		 FROM global_memory_candidates`,
		`DROP TABLE global_memory_candidates`,
		`ALTER TABLE global_memory_candidates_new RENAME TO global_memory_candidates`,
		`CREATE INDEX idx_global_memory_candidates_pending ON global_memory_candidates(conversation_id,status,next_attempt_at,id)`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			log.Fatalf("迁移全局记忆候选表失败: %v", err)
		}
	}
	if err = tx.Commit(); err != nil {
		log.Fatalf("提交全局记忆候选表迁移失败: %v", err)
	}
}

func ensureAttachmentMessageViewSchema() {
	if _, err := DB.Exec(`DROP VIEW IF EXISTS all_messages`); err != nil {
		log.Fatalf("刷新统一消息视图失败: %v", err)
	}
	if _, err := DB.Exec(`CREATE VIEW all_messages AS
		SELECT id,conversation_id,role,content,created_at,memory_archived_at,reply_to_message_id,quote_role,quote_text FROM messages
		UNION ALL
		SELECT id,conversation_id,role,content,created_at,memory_archived_at,reply_to_message_id,quote_role,quote_text FROM messages_archive`); err != nil {
		log.Fatalf("创建统一消息视图失败: %v", err)
	}
	rows, err := DB.Query(`PRAGMA foreign_key_list(message_attachments)`)
	if err != nil {
		log.Fatalf("读取附件外键失败: %v", err)
	}
	hasMessageFK := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			log.Fatalf("解析附件外键失败: %v", err)
		}
		if from == "message_id" && table == "messages" {
			hasMessageFK = true
		}
	}
	rows.Close()
	if !hasMessageFK {
		return
	}
	tx, err := DB.Begin()
	if err != nil {
		log.Fatalf("开始附件表迁移失败: %v", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE message_attachments_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL DEFAULT 1,
			message_id INTEGER,
			kind TEXT NOT NULL DEFAULT 'image',
			original_name TEXT,
			file_path TEXT NOT NULL,
			url TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id)
		)`,
		`INSERT INTO message_attachments_new
			(id,conversation_id,message_id,kind,original_name,file_path,url,mime_type,size_bytes,content_hash,created_at)
		 SELECT id,conversation_id,message_id,kind,original_name,file_path,url,mime_type,size_bytes,content_hash,created_at
		 FROM message_attachments`,
		`DROP TABLE message_attachments`,
		`ALTER TABLE message_attachments_new RENAME TO message_attachments`,
	}
	for _, stmt := range statements {
		if _, err = tx.Exec(stmt); err != nil {
			log.Fatalf("重建附件表失败: %v", err)
		}
	}
	if err = tx.Commit(); err != nil {
		log.Fatalf("提交附件表迁移失败: %v", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_attachments_hash ON message_attachments(content_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_message ON message_attachments(message_id)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_conversation ON message_attachments(conversation_id)`,
	} {
		if _, err := DB.Exec(stmt); err != nil {
			log.Fatalf("重建附件索引失败: %v", err)
		}
	}
}

func ensureMemoryChunkForeignKeys() {
	rows, err := DB.Query(`PRAGMA foreign_key_list(memory_chunks)`)
	if err != nil {
		log.Fatalf("读取长期记忆外键失败: %v", err)
	}
	parents := map[string]bool{}
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			log.Fatalf("解析长期记忆外键失败: %v", err)
		}
		parents[from+":"+table] = true
	}
	rows.Close()
	if parents["conversation_id:conversations"] && parents["source_doc_id:knowledge_docs"] {
		return
	}

	tx, err := DB.Begin()
	if err != nil {
		log.Fatalf("开始长期记忆外键迁移失败: %v", err)
	}
	defer tx.Rollback()
	// Global memories do not depend on their historical conversation id. Keep
	// them intact while repairing references left by deleted conversations.
	if _, err = tx.Exec(`UPDATE memory_chunks SET conversation_id=1
		WHERE scope='global' AND NOT EXISTS (
			SELECT 1 FROM conversations c WHERE c.id=memory_chunks.conversation_id
		)`); err != nil {
		log.Fatalf("修复全局记忆会话引用失败: %v", err)
	}
	var orphaned int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM memory_chunks m
		WHERE NOT EXISTS (SELECT 1 FROM conversations c WHERE c.id=m.conversation_id)
		   OR (m.source_doc_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM knowledge_docs d WHERE d.id=m.source_doc_id))`).Scan(&orphaned); err != nil {
		log.Fatalf("检查长期记忆引用失败: %v", err)
	}
	if orphaned != 0 {
		log.Fatalf("长期记忆存在 %d 条无法安全迁移的孤儿引用", orphaned)
	}
	statements := []string{
		`CREATE TABLE memory_chunks_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id INTEGER NOT NULL DEFAULT 1,
			assistant TEXT NOT NULL DEFAULT 'rhys',
			scope TEXT NOT NULL DEFAULT 'conversation',
			source_doc_id INTEGER,
			source_type TEXT NOT NULL DEFAULT 'conversation',
			content TEXT NOT NULL,
			summary TEXT,
			keywords TEXT,
			time_start DATETIME,
			time_end DATETIME,
			emotion TEXT NOT NULL DEFAULT '',
			correction TEXT NOT NULL DEFAULT '',
			topic_label TEXT NOT NULL DEFAULT '',
			is_correction INTEGER NOT NULL DEFAULT 0,
			importance TEXT NOT NULL DEFAULT 'medium',
			active INTEGER NOT NULL DEFAULT 1,
			version INTEGER NOT NULL DEFAULT 1,
			superseded_by INTEGER,
			last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(conversation_id) REFERENCES conversations(id),
			FOREIGN KEY(source_doc_id) REFERENCES knowledge_docs(id) ON DELETE SET NULL
		)`,
		`INSERT INTO memory_chunks_new
			(id,conversation_id,assistant,scope,source_doc_id,source_type,content,summary,keywords,time_start,time_end,emotion,correction,topic_label,is_correction,importance,active,version,superseded_by,last_accessed_at,created_at)
		 SELECT id,conversation_id,assistant,scope,source_doc_id,source_type,content,summary,keywords,time_start,time_end,emotion,correction,topic_label,is_correction,importance,active,version,superseded_by,COALESCE(last_accessed_at,created_at,CURRENT_TIMESTAMP),created_at
		 FROM memory_chunks`,
		`DROP TABLE memory_chunks`,
		`ALTER TABLE memory_chunks_new RENAME TO memory_chunks`,
	}
	for _, stmt := range statements {
		if _, err = tx.Exec(stmt); err != nil {
			log.Fatalf("重建长期记忆表失败: %v", err)
		}
	}
	if err = tx.Commit(); err != nil {
		log.Fatalf("提交长期记忆外键迁移失败: %v", err)
	}
}

func addColumnIfMissing(table, column, spec string) {
	rows, err := DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		log.Fatalf("读取表结构失败: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			log.Fatalf("解析表结构失败: %v", err)
		}
		if name == column {
			return
		}
	}
	if _, err := DB.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + spec); err != nil {
		log.Fatalf("迁移字段失败: %s.%s: %v", table, column, err)
	}
}
