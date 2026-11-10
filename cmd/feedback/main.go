package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NerdsWhoFish/feedback/internal/api"
	"github.com/NerdsWhoFish/feedback/internal/auth"
	"github.com/NerdsWhoFish/feedback/internal/config"
	"github.com/NerdsWhoFish/feedback/internal/store"
	"github.com/NerdsWhoFish/feedback/internal/telemetry"
	"github.com/NerdsWhoFish/feedback/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

var version = "dev"
var commit = "unknown"

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("feedback %s (%s)\n", version, commit)
			return nil
		case "verify-build":
			return verifyBuild(os.Getenv("VERSION"), os.Getenv("COMMIT"))
		default:
			return errors.New("usage: feedback [version|verify-build]")
		}
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	if c.Environment != "local" && c.FaroURL == "" {
		return errors.New("FARO_COLLECTOR_URL is required outside local development")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger, shutdown, err := telemetry.Setup(ctx, c.Environment, version)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = shutdown(shutdownCtx)
	}()
	pc, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	pc.ConnConfig.Tracer = telemetry.PGTracer{}
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	pc.ConnConfig.RuntimeParams["lock_timeout"] = "5000"
	pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	pc.MaxConns = 20
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return errors.New("database initialization failed")
	}
	defer db.Close()
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = store.Migrate(startupCtx, db); err != nil {
		return errors.New("database migration failed")
	}
	authentication, err := auth.New(startupCtx, db, c)
	if err != nil {
		return err
	}
	application := api.New(db, logger)
	if err = application.ConfigureOutgoingWebhookWithHeaders(c.WebhookURL, c.WebhookSecret, c.BaseURL, c.WebhookHeaders); err != nil {
		return err
	}
	if err = application.ConfigureEmail(c.ResendAPIKey, c.EmailFrom, c.BaseURL, c.AdminEmails); err != nil {
		return err
	}
	metrics := telemetry.NewMetrics(application.RequestCounts)
	application.SetMetrics(metrics)
	if len(c.WebhookKey) > 0 {
		if err = application.SetWebhookEncryptionKey(c.WebhookKey); err != nil {
			return errors.New("invalid webhook encryption configuration")
		}
	}
	mux := http.NewServeMux()
	authentication.Register(mux)
	application.Register(mux, authentication.Require)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if db.Ping(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"telemetry_enabled": c.FaroURL != "", "version": version, "environment": c.Environment})
	})
	mux.Handle("POST /api/v1/telemetry", telemetry.BrowserRelay(c.BaseURL, c.FaroURL, c.Environment, version, logger))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "API route not found"}})
	})
	mux.Handle("/", web.Handler())
	server := &http.Server{Addr: c.Address, Handler: headers(telemetry.HTTPWithMetrics(logger, mux, metrics), c.CookieSecure), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	var metricsServer *http.Server
	var metricsListener net.Listener
	if c.MetricsAddress != "" {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("GET /metrics", metrics.Handler())
		metricsServer = &http.Server{Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
		metricsListener, err = net.Listen("tcp", c.MetricsAddress)
		if err != nil {
			return errors.New("metrics listener failed")
		}
		defer func() { _ = metricsServer.Close() }()
	}
	var workers sync.WaitGroup
	workers.Add(4)
	go func() { defer workers.Done(); authentication.Cleanup(ctx) }()
	go func() { defer workers.Done(); application.RunWebhooks(ctx) }()
	go func() { defer workers.Done(); application.RunOutgoingWebhooks(ctx) }()
	go func() { defer workers.Done(); application.RunEmail(ctx) }()
	defer func() { stop(); workers.Wait() }()
	done := make(chan error, 2)
	go func() { done <- server.ListenAndServe() }()
	if metricsServer != nil {
		go func() { done <- metricsServer.Serve(metricsListener) }()
	}
	defer func() { _ = server.Close() }()
	logger.Info("service started", "version", version)
	select {
	case <-ctx.Done():
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server failed")
		}
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if server.Shutdown(shutdownCtx) != nil {
		return errors.New("HTTP shutdown failed")
	}
	if metricsServer != nil && metricsServer.Shutdown(shutdownCtx) != nil {
		return errors.New("metrics shutdown failed")
	}
	return nil
}

func verifyBuild(expectedVersion, expectedCommit string) error {
	if expectedVersion == "" || expectedCommit == "" {
		return errors.New("VERSION and COMMIT are required to verify the build")
	}
	if version != strings.TrimPrefix(expectedVersion, "v") || commit != expectedCommit {
		return errors.New("embedded build version or commit does not match the image")
	}
	return nil
}

func headers(next http.Handler, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.URL.Path != "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
