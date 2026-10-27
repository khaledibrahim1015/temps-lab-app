// Test app for the Temps feature lab (Docs/temps/02-features-lab.md, groups 1-2).
// Each field on / tests one deploy feature:
//
//	version  -> build variable (Docker build arg), changes per commit
//	greeting -> runtime environment variable
//	hostname -> container ID, changes on every new container
//	started  -> restart vs redeploy
//
// GET /db tests a linked managed Postgres (group 4): it reads POSTGRES_URL,
// which Temps injects at deploy time, and writes one row per request.
//
// Observability (group 5) uses only what Temps injects at deploy time:
//
//	JSON request log on stdout -> container logs (one line per request)
//	GET /log?level=&msg=       -> one log line at the given level
//	GET /boom                  -> one error to SENTRY_DSN, then 500
//	OTEL_EXPORTER_OTLP_*       -> one trace per request (no-op if unset)
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Set at build time: docker build --build-arg APP_VERSION=...
var version = "dev"

var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

var tracer = otel.Tracer("temps-lab-app")

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	poolErr  error
)

// getPool connects on first use, so the app still starts without a database.
func getPool(ctx context.Context) (*pgxpool.Pool, error) {
	poolOnce.Do(func() {
		url := os.Getenv("POSTGRES_URL")
		if url == "" {
			poolErr = errNoURL
			return
		}
		pool, poolErr = pgxpool.New(ctx, url)
	})
	return pool, poolErr
}

type constErr string

func (e constErr) Error() string { return string(e) }

const errNoURL = constErr("POSTGRES_URL is not set (no Postgres service linked)")

func dbHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ctx, span := tracer.Start(ctx, "db.visit")
	defer span.End()

	fail := func(code int, err error) {
		span.RecordError(err)
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}

	p, err := getPool(ctx)
	if err != nil {
		fail(http.StatusServiceUnavailable, err)
		return
	}
	if _, err := p.Exec(ctx, `CREATE TABLE IF NOT EXISTS visits (
		id bigserial PRIMARY KEY,
		host text NOT NULL,
		app_version text NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}

	var id int64
	if err := p.QueryRow(ctx,
		`INSERT INTO visits (host, app_version) VALUES ($1, $2) RETURNING id`,
		r.Host, version).Scan(&id); err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}

	var database, user string
	var count int64
	if err := p.QueryRow(ctx,
		`SELECT current_database(), current_user, (SELECT count(*) FROM visits)`).
		Scan(&database, &user, &count); err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}
	span.SetAttributes(attribute.Int64("visit.id", id))

	json.NewEncoder(w).Encode(map[string]any{
		"database": database,
		"user":     user,
		"visit_id": id,
		"visits":   count,
		"host":     r.Host,
		"version":  version,
	})
}

// logHandler writes one log line at the level from ?level= (debug, info, warn, error).
func logHandler(w http.ResponseWriter, r *http.Request) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(r.URL.Query().Get("level"))); err != nil {
		level = slog.LevelInfo
	}
	msg := r.URL.Query().Get("msg")
	if msg == "" {
		msg = "test log line"
	}
	logger.Log(r.Context(), level, msg, "source", "log-endpoint", "request_id", requestID(r))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"level": level.String(), "msg": msg})
}

// boomHandler sends one error to Sentry (SENTRY_DSN) and answers 500.
func boomHandler(w http.ResponseWriter, r *http.Request) {
	err := fmt.Errorf("lab boom: test error %q (version %s)", r.URL.Query().Get("tag"), version)
	var eventID string
	if hub := sentry.CurrentHub(); hub.Client() != nil {
		if id := hub.CaptureException(err); id != nil {
			eventID = string(*id)
		}
		sentry.Flush(2 * time.Second)
	}
	logger.Error("boom", "error", err.Error(), "sentry_event_id", eventID, "request_id", requestID(r))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "sentry_event_id": eventID})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

type ctxKey struct{}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(ctxKey{}).(string)
	return id
}

// logRequests writes one JSON line per request: 5xx as error, 4xx as warn.
// /healthz is skipped: the Docker HEALTHCHECK calls it every 10 seconds.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			b := make([]byte, 8)
			rand.Read(b)
			id = hex.EncodeToString(b)
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)

		level := slog.LevelInfo
		switch {
		case sw.status >= 500:
			level = slog.LevelError
		case sw.status >= 400:
			level = slog.LevelWarn
		}
		logger.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", id,
			"host", r.Host,
			"version", version)
	})
}

// setupTracing exports traces over OTLP/HTTP when Temps injected
// OTEL_EXPORTER_OTLP_ENDPOINT. The exporter reads the endpoint and headers
// from the standard OTEL_* env vars.
func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(semconv.ServiceVersion(version)))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	started := time.Now().UTC().Format(time.RFC3339)
	hostname, _ := os.Hostname()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{Dsn: dsn, Release: version}); err != nil {
			logger.Error("sentry init failed", "error", err.Error())
		}
	}
	shutdownTracing, err := setupTracing(ctx)
	if err != nil {
		logger.Error("tracing setup failed", "error", err.Error())
		shutdownTracing = func(context.Context) error { return nil }
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version":  version,
			"greeting": os.Getenv("GREETING"),
			"hostname": hostname,
			"started":  started,
			"host":     r.Host,
		})
	})
	mux.HandleFunc("/db", dbHandler)
	mux.HandleFunc("/log", logHandler)
	mux.HandleFunc("/boom", boomHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	handler := otelhttp.NewHandler(logRequests(mux), "http.request",
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}))
	srv := &http.Server{Addr: ":" + port, Handler: handler}

	logger.Info("listening",
		"port", port,
		"version", version,
		"sentry", os.Getenv("SENTRY_DSN") != "",
		"otel", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "")
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err.Error())
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	shutdownTracing(shutdownCtx)
	sentry.Flush(2 * time.Second)
	logger.Info("stopped")
}
