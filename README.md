# Readest Tiny Backend

[English](README.md) | [简体中文](README.zh-CN.md)

A self-hosted backend compatible with the current `readest-main` client. It is built with Go, Gin, GORM, SQLite, local object storage, and an embedded React + TypeScript + Ant Design administrator panel.

Implemented client contracts:

- `GET /runtime-config.js` for Readest's custom server endpoint form
- Supabase Auth-compatible email/password login, refresh, user update, logout, and password recovery under `/auth/v1`; public signup is disabled
- `GET/POST /api/sync` for books, reading configs, bookmarks, and annotations, including deletion tombstones, server cursors, paged pulls, LWW conflict handling, and field-level cover/reading-status/metadata merges
- `GET/PUT /api/privacy-sync` for per-account, end-to-end encrypted privacy settings with atomic last-write-wins conflict handling
- `/api/storage/upload`, `/download`, `/list`, and `/delete`, backed by short-lived signed local upload/download URLs
- `/admin/` for administrator-only account creation, deletion, usage statistics, and per-account storage quotas

Runtime config advertises `objectStorageType: "r2"` for the client's object-key naming strategy; objects are still stored locally. It also advertises disabled public signup, immediate email changes, and sync request limits. Updated clients persist these settings when connecting to this server. After upgrading an existing client, reconnect the server endpoint once to load the additional settings.

Book pull cursors round-trip at millisecond precision and advance monotonically per account, including progress updates. Legacy sub-millisecond rows are returned with rounded-up cursors and completed boundary groups. Clients should send the returned cursor unchanged for the next exclusive pull, and split pushes by both the advertised record count and UTF-8 body-size limits.

## Quick Start

Go 1.25 and a C compiler are required because the official GORM SQLite driver uses CGO. Node.js 24 is required only when rebuilding the administrator panel. Older Go installations with automatic toolchain selection enabled can download the required toolchain through `GOPROXY`.

PowerShell:

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
go mod download
Set-Location web/admin
npm ci --registry=https://registry.npmmirror.com
npm run build
Set-Location ../..
go run ./cmd/readest-server
```

The server starts at `http://127.0.0.1:8080` and redirects the root page to the administrator panel at `http://127.0.0.1:8080/admin/`. In Readest's login page, enter `http://127.0.0.1:8080` as the **Server endpoint**.

On the first start, the server creates `data/readest.db`, `data/objects`, a private random `data/jwt-secret`, and an administrator account. With the default configuration, the administrator email is `admin@readest.local` and a random password is written to `data/initial-admin-credentials.json` with owner-only permissions:

```powershell
Get-Content .\data\initial-admin-credentials.json
```

Log in to the administrator panel and change the generated password. The credentials file is removed after the administrator password is changed. Alternatively, set `INITIAL_ADMIN_EMAIL` and a 12-to-72-byte `INITIAL_ADMIN_PASSWORD` before the first start; an explicitly configured password is never written to disk. Bootstrap settings never reset an existing administrator.

Public account registration is permanently disabled. Create every reader account from the administrator panel, then give that account's credentials to the reader.

Back up the whole `data` directory. Losing `jwt-secret` signs out all clients; losing the database or object directory loses synchronized metadata or books.

Docker:

```powershell
$env:NODE_IMAGE = 'your-trusted-registry/library/node:24-alpine' # optional
$env:GO_IMAGE = 'your-trusted-registry/library/golang:1.25-alpine' # optional
$env:ALPINE_IMAGE = 'your-trusted-registry/library/alpine:3.21' # optional
$env:GOPROXY = 'https://goproxy.cn,direct'
docker compose up --build
docker compose exec readest-backend cat /app/data/initial-admin-credentials.json
```

The base-image overrides are only needed when Docker Hub is unavailable; point them at a registry you trust. Compose also accepts `BIND_HOST`, `PORT`, and `READEST_IMAGE`. It persists state in the `readest-data` named volume so the non-root container keeps correct ownership. Include that volume in backups; `docker compose down` preserves it, while `docker compose down -v` deliberately deletes it.

## Automatic Docker Publishing

Every push to any branch triggers [Publish Docker image](.github/workflows/docker-publish.yml), which builds `linux/amd64` and `linux/arm64` images and pushes them to `ghcr.io/<owner>/<repository>`. It uses the repository's `GITHUB_TOKEN` with `packages: write`; no additional registry secret is needed. The workflow can also be run manually from GitHub Actions.

- `branch-<branch>` tracks that branch; unsupported tag characters such as `/` are replaced with `-`.
- `sha-<full-commit-sha>` identifies the source commit.
- `edge` tracks the default branch. Stable `latest` and version tags remain managed by the release workflow below.

New pushes to the same branch cancel older in-progress image builds. After the first publication, check the package visibility and its repository access in GitHub Packages settings. See [GitHub's container publishing guide](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images) for the authentication and permission model.

To run the default branch image with Compose:

```powershell
$env:READEST_IMAGE = 'ghcr.io/gjh303987897/readest-server-tiny:edge'
docker compose pull
docker compose up -d
```

## Releases

Pushing a semantic-version tag starts one release workflow that publishes a
`linux/amd64` and `linux/arm64` image to GHCR, then creates a GitHub Release containing
Linux amd64/arm64, Windows amd64, and macOS amd64/arm64 binaries plus SHA-256 checksums.
Every binary includes the administrator panel and SQLite support. Tags may optionally
start with `v` and may include a prerelease suffix such as `0.0.1-alpha`.

```powershell
git tag v1.0.0
git push origin v1.0.0
```

Prerelease tags create a GitHub Pre-release and an exact-version image tag without
moving the stable minor or `latest` tags:

```powershell
git tag 0.0.1-alpha
git push origin 0.0.1-alpha
```

Run the published multi-platform image through the existing Compose configuration by
setting `READEST_IMAGE` to the immutable release version. Docker automatically selects
the matching image architecture:

```powershell
$env:READEST_IMAGE = 'ghcr.io/gjh303987897/readest-server-tiny:1.0.0'
docker compose pull
docker compose up -d
```

## Configuration

Copy values from `.env.example` into your process environment or a local `.env` consumed by your process manager. The binary intentionally does not parse `.env` files itself.

Important production settings:

- `APP_ENV=production` requires an HTTPS `PUBLIC_URL`.
- Put the service behind a TLS reverse proxy and forward to its private HTTP port.
- Persist and back up `DATA_DIR`; do not expose it through the web server.
- Set `INITIAL_ADMIN_EMAIL` and preferably inject `INITIAL_ADMIN_PASSWORD` through your deployment secret manager before the first production start. Do not place the password in a committed `.env` file.
- Set `CORS_ALLOWED_ORIGINS` and `ALLOWED_REDIRECT_URLS` to the exact origins/redirects you use.
- Configure `SMTP_HOST` and `SMTP_FROM` to enable password recovery emails.
- Keep `JWT_SECRET`, SMTP credentials, the SQLite file, and stored objects out of source control.

`PUBLIC_ANON_KEY` is deliberately public because Readest receives it from runtime config. It is only an API identifier; authorization always comes from signed, revocable Bearer sessions.

## Security Model

- Passwords use bcrypt; access tokens are short-lived HS256 JWTs and are checked against revocable server sessions.
- The public signup endpoint always returns `403`; only an authenticated database administrator can create accounts.
- Administrator APIs reload the user record on every request, require the database-backed admin flag, and never expose password hashes.
- Account deletion revokes sessions, deletes synchronized rows, and removes the account's confined object directory.
- Refresh tokens are random, stored only as SHA-256 hashes, rotated atomically, and rejected on replay.
- Every GORM query for user data includes the authenticated user ID. Client-supplied `user_id` values are ignored.
- Privacy PINs and hidden book hashes remain inside the client-encrypted envelope; the server validates and stores only the opaque ciphertext record.
- Bookmark and annotation rows use the authenticated user plus `book_hash` and note `id` as their key; deleted records remain as tombstones so stale devices cannot recreate them.
- Sync payloads, timestamps, JSON size, record counts, and text lengths are bounded.
- HTTP body I/O and SMTP operations have deadlines; panic recovery never logs request headers or signed query strings.
- Storage keys must exactly match `Readest/Books/<32-hex-hash>/<supported-file>` in raw and decoded form.
- Disk paths use server-generated UUID object names, never client paths, and every operation is confined beneath an `os.Root` storage handle.
- Upload signatures bind method purpose, account, record, object key, nonce, exact byte length, and expiry; reservations are claimed atomically so a signed PUT cannot be replayed concurrently.
- Per-account byte quotas are enforced transactionally against ready files and active upload reservations; administrators can set a custom limit or inherit the server default.
- The administrator panel is compiled into the Go binary, uses same-origin requests, keeps its session only in `sessionStorage`, and is served with a restrictive Content Security Policy.
- JSON/API rate limits, request body limits, strict proxy handling, security headers, and CORS handling are enabled by default.

The built-in fixed-window limiter is suitable for a single tiny-backend process. For a multi-instance deployment, apply a shared rate limit at the reverse proxy as well.

## Verification

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
npm --prefix web/admin ci --registry=https://registry.npmmirror.com
npm --prefix web/admin run typecheck
npm --prefix web/admin run build
go test ./...
go test -race ./...
go vet ./...
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -quiet ./...
```

Tests cover administrator bootstrap, disabled signup, administrator authorization, account creation/deletion, exact per-account quota enforcement, usage statistics, the Supabase-compatible session lifecycle, token replay and revocation, dangerous configuration rejection, tenant isolation, book/config/note merge and cursor behavior, bookmark tombstones, legacy note-schema migration, path traversal rejection, signed upload size/replay enforcement, and cross-account storage access.
