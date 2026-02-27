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
	"github.com/florian/kodama/internal/tui"
	"github.com/florian/kodama/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tui" {
		runTUI()
		return
	}
	runDaemon()
}

func runDaemon() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	// Open database.
	database, err := db.Open(cfg.DataDir)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create WebSocket hub.
	hub := web.NewHub()

	// Create daemon.
	d := daemon.New(cfg, database, hub)

	// Set up Telegram bot if configured.
	if cfg.Telegram.Token != "" && cfg.Telegram.UserID != 0 {
		bot, err := telegram.New(cfg.Telegram.Token, cfg.Telegram.UserID)
		if err != nil {
			slog.Warn("telegram bot init failed", "err", err)
		} else {
			d.SetNotifier(bot)
			d.SetQuestionAnswerer(bot)
			go bot.Start(context.Background())
			slog.Info("telegram bot started")
		}
	}

	// Create and start web server.
	srv, err := web.New(cfg, database, hub, d)
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

func runTUI() {
	// Determine daemon URL.
	port := 8080
	cfg, err := config.Load()
	if err == nil {
		port = cfg.Port
	}
	baseURL := fmt.Sprintf("http://localhost:%d", port)

	if err := tui.Run(baseURL); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
