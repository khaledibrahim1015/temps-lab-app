# temps-lab-app

Test app for the Temps feature lab (`Docs/temps/02-features-lab.md`).

- `GET /` returns JSON: `version` (build arg `APP_VERSION`), `greeting` (env `GREETING`), `hostname`, `started`, `host`.
- `GET /healthz` returns `ok`.
- Listens on `$PORT`, default `8080`.
