// Command slack-bridge is a standalone service that bridges beads lifecycle
// events to Slack notifications and handles Slack interaction webhooks.
//
// It runs three subsystems:
//   - Decisions watcher: NATS subscription for decision beads → Slack notifications
//   - Mail watcher: NATS subscription for mail beads → agent nudges
//   - HTTP server: Slack interaction webhook handler (/slack/interactions)
//
// This service has ZERO K8s dependencies and can run as a lightweight sidecar
// or standalone container alongside the gasboat controller.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/bridge"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cfg := parseConfig()

	logger := setupLogger(cfg.logLevel)
	logger.Info("starting slack-bridge",
		"version", version,
		"commit", commit,
		"beads_http", cfg.beadsHTTPAddr,
		"nats_url", cfg.natsURL,
		"slack_channel", cfg.slackChannel,
		"listen_addr", cfg.listenAddr)

	// Create beads daemon HTTP client.
	daemon, err := beadsapi.New(beadsapi.Config{HTTPAddr: cfg.beadsHTTPAddr})
	if err != nil {
		logger.Error("failed to create beads daemon client", "error", err)
		os.Exit(1)
	}
	defer daemon.Close()

	// Register bead types, views, and context configs with the daemon.
	if err := bridge.EnsureConfigs(context.Background(), daemon, logger); err != nil {
		logger.Warn("failed to ensure beads configs (non-fatal)", "error", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Slack notifier (optional — decisions still tracked even without Slack).
	var notifier bridge.Notifier
	mux := http.NewServeMux()

	// Health endpoints — always available regardless of Slack config.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, version)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	if cfg.slackBotToken != "" {
		slack := bridge.NewSlackNotifier(
			cfg.slackBotToken,
			cfg.slackSigningSecret,
			cfg.slackChannel,
			daemon,
			logger,
		)
		notifier = slack

		// Register Slack interaction handler on the shared mux.
		mux.HandleFunc("/slack/interactions", slack.HandleInteraction)
		logger.Info("Slack notifier enabled", "channel", cfg.slackChannel)
	} else {
		logger.Warn("SLACK_BOAT_TOKEN not set — running without Slack notifications")
	}

	// Start HTTP server (always — serves health endpoints + optional Slack handler).
	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("starting HTTP server", "addr", cfg.listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
		}
	}()

	// Start decisions watcher.
	decisions := bridge.NewDecisions(bridge.DecisionsConfig{
		NatsURL:   cfg.natsURL,
		NatsToken: cfg.natsToken,
		Daemon:    daemon,
		Notifier:  notifier,
		Logger:    logger,
	})
	go func() {
		if err := decisions.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("decisions watcher stopped", "error", err)
		}
	}()

	// Start mail watcher.
	mail := bridge.NewMail(bridge.MailConfig{
		NatsURL:   cfg.natsURL,
		NatsToken: cfg.natsToken,
		Daemon:    daemon,
		Logger:    logger,
	})
	go func() {
		if err := mail.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("mail watcher stopped", "error", err)
		}
	}()

	logger.Info("slack-bridge ready")

	// Block until shutdown signal.
	<-ctx.Done()
	logger.Info("shutting down slack-bridge")

	// Graceful HTTP server shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}
}

// config holds parsed environment configuration for the slack-bridge service.
type config struct {
	beadsHTTPAddr      string
	natsURL            string
	natsToken          string
	slackBotToken      string
	slackSigningSecret string
	slackChannel       string
	listenAddr         string
	logLevel           string
}

func parseConfig() *config {
	return &config{
		beadsHTTPAddr:      envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		natsURL:            envOrDefault("NATS_URL", "nats://localhost:4222"),
		natsToken:          os.Getenv("NATS_TOKEN"),
		slackBotToken:      os.Getenv("SLACK_BOAT_TOKEN"),
		slackSigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
		slackChannel:       os.Getenv("SLACK_CHANNEL"),
		listenAddr:         envOrDefault("SLACK_LISTEN_ADDR", ":8090"),
		logLevel:           envOrDefault("LOG_LEVEL", "info"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
}

func init() {
	if v := os.Getenv("VERSION"); v != "" {
		version = v
	}
}

