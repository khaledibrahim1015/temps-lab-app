# temps-lab-app

Test app for the Temps feature lab (`Docs/temps/02-features-lab.md`).

- `GET /` returns JSON: `version` (build arg `APP_VERSION`), `greeting` (env `GREETING`), `hostname`, `started`, `host`.
- `GET /healthz` returns `ok`.
- `GET /db` writes one row to the linked Postgres (`POSTGRES_URL`).
- `GET /log?level=debug|info|warn|error&msg=...` writes one JSON log line at that level.
- `GET /boom?tag=...` sends one error to `SENTRY_DSN` and returns `500`.
- Every request except `/healthz` writes one JSON log line (`slog`) and, when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, one trace.
- Listens on `$PORT`, default `8080`.
push test 00:06:01
push test 02:11:30
