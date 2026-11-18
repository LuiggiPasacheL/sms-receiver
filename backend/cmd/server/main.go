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

	"github.com/jackc/pgx/v5/pgxpool"
	"sms-receiver/internal/inbox"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database configuration: %w", err)
	}
	config.MaxConns = 10
	config.ConnConfig.ConnectTimeout = 10 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()
	store := inbox.Store{Pool: pool}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = store.Migrate(startup)
	cancel()
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	events := inbox.NewEvents()
	go events.Listen(ctx, databaseURL, logger)
	settings := inbox.Config{
		ViewerPassword: os.Getenv("VIEWER_PASSWORD"),
		ForwarderToken: os.Getenv("FORWARDER_TOKEN"),
		CookieSecure:   os.Getenv("COOKIE_SECURE") == "true",
		StaticDir:      env("STATIC_DIR", "../frontend/out"),
	}
	if settings.ViewerPassword == "" {
		logger.Warn("viewer password is unset; inbox access is public")
	}
	if settings.ForwarderToken == "" {
		logger.Warn("forwarder token is unset; webhook accepts unauthenticated messages")
	}
	app := &inbox.Server{Store: store, Events: events, Config: settings, Logger: logger, Context: ctx}
	server := &http.Server{
		Addr: env("HTTP_ADDR", ":8080"), Handler: app.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		// SSE controls deadlines per write; a global WriteTimeout would end streams.
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024,
	}
	failures := make(chan error, 1)
	go func() { failures <- server.ListenAndServe() }()
	logger.Info("SMS inbox listening", "address", server.Addr)
	select {
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	return server.Shutdown(shutdown)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
