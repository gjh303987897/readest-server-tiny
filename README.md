# Readest Tiny Backend

A self-hosted backend compatible with the current `readest-main` client. It is built with Go, Gin, GORM, SQLite, and local object storage.

Implemented client contracts:

- `GET /runtime-config.js` for Readest's custom server endpoint form
- Supabase Auth-compatible email/password signup, login, refresh, user update, logout, and password recovery under `/auth/v1`
- `GET/POST /api/sync` for books and reading configs, including server cursors, paged pulls, whole-row LWW, and field-level cover/reading-status/metadata merges
- `/api/storage/upload`, `/download`, `/list`, and `/delete`, backed by short-lived signed local upload/download URLs

## Quick Start

Go 1.25 and a C compiler are required because the official GORM SQLite driver uses CGO. Older Go installations with automatic toolchain selection enabled can download the required toolchain through `GOPROXY`.

PowerShell:

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
go mod download
go run ./cmd/readest-server
```

The server starts at `http://127.0.0.1:8080`. In Readest's login page, enter that address in **Server endpoint**, connect, then create an account.

The first start creates `data/readest.db`, `data/objects`, and a private random `data/jwt-secret`. Back up the whole `data` directory. Losing `jwt-secret` signs out all clients; losing the database or object directory loses synchronized metadata or books.

Docker:

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
docker compose up --build
```

Compose persists state in the `readest-data` named volume so the non-root container keeps correct ownership. Include that volume in backups; `docker compose down` preserves it, while `docker compose down -v` deliberately deletes it.

## Configuration

Copy values from `.env.example` into your process environment or a local `.env` consumed by your process manager. The binary intentionally does not parse `.env` files itself.

Important production settings:

- `APP_ENV=production` requires an HTTPS `PUBLIC_URL`.
- Put the service behind a TLS reverse proxy and forward to its private HTTP port.
- Persist and back up `DATA_DIR`; do not expose it through the web server.
- Set `CORS_ALLOWED_ORIGINS` and `ALLOWED_REDIRECT_URLS` to the exact origins/redirects you use.
- Configure SMTP before setting `AUTO_CONFIRM_EMAIL=false`. Password recovery also requires SMTP.
- Keep `JWT_SECRET`, SMTP credentials, the SQLite file, and stored objects out of source control.

`PUBLIC_ANON_KEY` is deliberately public because Readest receives it from runtime config. It is only an API identifier; authorization always comes from signed, revocable Bearer sessions.

## Security Model

- Passwords use bcrypt; access tokens are short-lived HS256 JWTs and are checked against revocable server sessions.
- Refresh tokens are random, stored only as SHA-256 hashes, rotated atomically, and rejected on replay.
- Every GORM query for user data includes the authenticated user ID. Client-supplied `user_id` values are ignored.
- Sync payloads, timestamps, JSON size, record counts, and text lengths are bounded.
- HTTP body I/O and SMTP operations have deadlines; panic recovery never logs request headers or signed query strings.
- Storage keys must exactly match `Readest/Books/<32-hex-hash>/<supported-file>` in raw and decoded form.
- Disk paths use server-generated UUID object names, never client paths, and every operation is confined beneath an `os.Root` storage handle.
- Upload signatures bind method purpose, account, record, object key, nonce, exact byte length, and expiry; reservations are claimed atomically so a signed PUT cannot be replayed concurrently.
- Per-account byte quotas and file-count limits prevent unbounded reservation/database growth.
- JSON/API rate limits, request body limits, strict proxy handling, security headers, and CORS handling are enabled by default.

The built-in fixed-window limiter is suitable for a single tiny-backend process. For a multi-instance deployment, apply a shared rate limit at the reverse proxy as well.

## Verification

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
go test ./...
go test -race ./...
go vet ./...
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.22.7 -quiet ./...
```

Tests cover the Supabase-compatible session lifecycle, token replay and revocation, dangerous configuration rejection, tenant isolation, book/config merge and cursor behavior, progress piggybacking, path traversal rejection, signed upload size/replay enforcement, and cross-account storage access.
