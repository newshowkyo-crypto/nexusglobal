package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nexusglobal/mamr/internal/platform/config"
	"github.com/nexusglobal/mamr/internal/platform/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	log.Info("worker starting", "concurrency", cfg.OutboxWorkerConcurrency)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down worker...")
	log.Info("worker stopped")
}
