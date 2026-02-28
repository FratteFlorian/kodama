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
	"github.com/florian/kodama/internal/telegram"
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
		"telegram_configured", cfg.Telegram.Token != "",
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

	// Create WebSocket hubs (one for tasks, one for environment logs).
	hub := web.NewHub()
	envHub := web.NewHub()

	// Create daemon.
	d := daemon.New(cfg, database, hub, envHub)

	// Set up Telegram bot if configured.
	if cfg.Telegram.Token != "" && cfg.Telegram.UserID != 0 {
		bot, err := telegram.New(cfg.Telegram.Token, cfg.Telegram.UserID)
		if err != nil {
			slog.Warn("telegram bot init failed", "err", err)
		} else {
			d.SetNotifier(bot)
			d.SetQuestionAnswerer(bot)
			go bot.Start(context.Background())
			slog.Info("telegram bot started", "user_id", cfg.Telegram.UserID)
		}
	} else {
		slog.Info("telegram not configured — notifications will be logged only")
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
