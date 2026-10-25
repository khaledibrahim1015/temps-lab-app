// Test app for the Temps feature lab (Docs/temps/02-features-lab.md, groups 1-2).
// Each field on / tests one deploy feature:
//
//	version  -> build variable (Docker build arg), changes per commit
//	greeting -> runtime environment variable
//	hostname -> container ID, changes on every new container
//	started  -> restart vs redeploy
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

// Set at build time: docker build --build-arg APP_VERSION=...
var version = "dev"

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
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	log.Printf("listening on :%s, version %s", port, version)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
