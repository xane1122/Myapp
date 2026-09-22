package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"myapp/internal/api"
	"myapp/internal/db"
)

func main() {
	_ = godotenv.Load()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		time.Local = location
	}
	dbPath := firstNonEmpty(os.Getenv("DB_PATH"), "data/memories.db")
	db.Init(dbPath)
	summary, err := api.BackfillConversationGlobalMemories(
		firstNonEmpty(os.Getenv("OPENROUTER_API_KEY"), os.Getenv("CHEAP_API_KEY")),
		strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		strings.TrimSpace(os.Getenv("GLOBAL_MEMORY_BACKFILL_MODEL")),
		log.Printf,
	)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[汇总] 窗口 %d 个, 扫描 %d 条, 提取 %d 条进 global", summary.Conversations, summary.Scanned, summary.Inserted)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
