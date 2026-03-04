package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/florian/kodama/internal/config"
	"github.com/florian/kodama/internal/daemon"
	"github.com/florian/kodama/internal/db"
	"github.com/florian/kodama/internal/web"
)

func main() {
	// Default to DEBUG level; set KODAMA_LOG=INFO to reduce noise.
	logLevel := slog.LevelDebug
	if os.Getenv("KODAMA_LOG") == "INFO" {
		logLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	runDaemon()
}

func runDaemon() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	slog.Info("config loaded",
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
		"claude_binary", cfg.Claude.Binary,
		"question_timeout", cfg.QuestionTimeout,
		"docker_socket", cfg.Docker.Socket,
	)

	// Open database.
	database, err := db.Open(cfg.DataDir)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	slog.Info("database opened", "path", cfg.DataDir+"/kodama.db")
	if recovered, err := database.RecoverRunningTasksToPending(); err != nil {
		slog.Warn("recover running tasks", "err", err)
	} else if recovered > 0 {
		slog.Warn("recovered running tasks to pending", "count", recovered)
	}

	// Create WebSocket hubs (one for tasks, one for environment logs).
	hub := web.NewHub()
	envHub := web.NewHub()

	// Create daemon.
	d := daemon.New(cfg, database, hub, envHub)

	// Apply Telegram settings from DB (if set).
	if settings, err := database.GetSettings(); err != nil {
		slog.Warn("load settings", "err", err)
	} else if settings != nil {
		if err := d.UpdateTelegramSettings(settings.TelegramToken, settings.TelegramUserID); err != nil {
			slog.Warn("apply telegram settings failed", "err", err)
		}
	}

	// Create and start web server.
	srv, err := web.New(cfg, database, hub, envHub, d)
	if err != nil {
		slog.Error("create web server", "err", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("starting Kodama", "addr", addr, "data_dir", cfg.DataDir)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	// Graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down...")
		cancel()
		httpServer.Shutdown(ctx)
	}()

	fmt.Printf("Kodama running at http://localhost%s\n", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http server", "err", err)
		os.Exit(1)
	}
}
