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
	args := os.Args[1:]
	var adminEmail string
	if len(args) > 0 {
		switch {
		case len(args) == 3 && args[0] == "admin" && args[1] == "add":
			var err error
			adminEmail, err = inbox.NormalizeAdminEmail(args[2])
			if err != nil {
				return err
			}
		case len(args) == 2 && args[0] == "admin" && args[1] == "list":
		default:
			return errors.New("usage: sms-receiver [admin add <email> | admin list]")
		}
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	forwarderToken := os.Getenv("FORWARDER_TOKEN")
	var access *inbox.CloudflareAccess
	if len(args) == 0 {
		var err error
		access, err = inbox.NewCloudflareAccess(ctx, os.Getenv("CLOUDFLARE_ACCESS_TEAM_URL"), os.Getenv("CLOUDFLARE_ACCESS_AUD"))
		if err != nil {
			return err
		}
		if access != nil && forwarderToken == "" {
			return errors.New("FORWARDER_TOKEN is required with Cloudflare Access")
		}
	}
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
	defer cancel()
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if adminEmail != "" {
		if err := store.AddAdmin(startup, adminEmail); err != nil {
			return fmt.Errorf("add administrator: %w", err)
		}
		fmt.Println(adminEmail)
		return nil
	}
	if len(args) > 0 {
		emails, err := store.AdminEmails(startup)
		if err != nil {
			return fmt.Errorf("list administrators: %w", err)
		}
		for _, email := range emails {
			fmt.Println(email)
		}
		return nil
	}
	cancel()
	events := inbox.NewEvents()
	go events.Listen(ctx, databaseURL, logger)
	settings := inbox.Config{
		ViewerPassword: os.Getenv("VIEWER_PASSWORD"),
		ForwarderToken: forwarderToken,
		CookieSecure:   os.Getenv("COOKIE_SECURE") == "true",
		StaticDir:      env("STATIC_DIR", "../frontend/out"),
	}
	if access == nil && settings.ViewerPassword == "" {
		logger.Warn("viewer password is unset; inbox access is public")
	}
	if settings.ForwarderToken == "" {
		logger.Warn("forwarder token is unset; webhook accepts unauthenticated messages")
	}
	app := &inbox.Server{Store: store, Admins: store, Access: access, Events: events, Config: settings, Logger: logger, Context: ctx}
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
