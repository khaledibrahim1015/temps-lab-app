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
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Set at build time: docker build --build-arg APP_VERSION=...
var version = "dev"

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

	fail := func(code int, err error) {
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

	json.NewEncoder(w).Encode(map[string]any{
		"database": database,
		"user":     user,
		"visit_id": id,
		"visits":   count,
		"host":     r.Host,
		"version":  version,
	})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	started := time.Now().UTC().Format(time.RFC3339)
	hostname, _ := os.Hostname()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version":  version,
			"greeting": os.Getenv("GREETING"),
			"hostname": hostname,
			"started":  started,
			"host":     r.Host,
		})
	})
	http.HandleFunc("/db", dbHandler)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	log.Printf("listening on :%s, version %s", port, version)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
