package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"myapp/internal/api"
	"myapp/internal/db"
	"myapp/internal/observability"
)

func main() {
	beijing, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		beijing = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	time.Local = beijing

	// 加载 .env
	if err := godotenv.Load(); err != nil {
		log.Println("未找到 .env 文件，使用系统环境变量")
	}
	logDir := os.Getenv("LOG_DIR")
	if logDir == "" {
		logDir = "logs"
	}
	if err := observability.Init(logDir); err != nil {
		log.Fatalf("初始化日志失败: %v", err)
	}
	defer observability.Close()

	// 初始化数据库
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "data/memories.db"
	}
	db.Init(dbPath)
	log.Printf("数据库已初始化: %s", dbPath)
	if err := api.RestoreRoleplayGenerateTasks(); err != nil {
		log.Fatalf("恢复过家家生成任务失败: %v", err)
	}
	if err := api.EnsureKnowledgeDocIndexes(); err != nil {
		log.Printf("历史文档索引修复失败: %v", err)
	}
	api.MaintainImageAttachments()
	api.StartWakeMonitor()
	api.StartBellMonitor()
	api.StartGlobalMemoryRescan()
	api.StartDiaryMonitor()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			api.MaintainImageAttachments()
		}
	}()

	// 启动服务
	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	addr := host + ":" + port
	log.Printf("服务启动: http://%s", addr)

	if err := http.ListenAndServe(addr, api.Handler()); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
