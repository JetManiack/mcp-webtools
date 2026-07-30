# go-ai-webtools: parity with go-ai-rendezvous-point

Date: 2026-07-30

## Goal

Bring `go-ai-webtools` to the same architecture, quality bar and packaging as
`go-ai-rendezvous-point`, keeping its own tools (`fetch`, `search`) and adding
a tool-call history surfaced through a REST API and web UI.

## Starting state

The repository does not compile:

- `go.mod` declares `go 1.24.5`; the vendored `modelcontextprotocol/go-sdk
  v1.7.0` requires >= 1.25.
- `go.mod` never requires the go-sdk it imports, and pins a nonexistent
  `golang.org/x/time v0.0.0` (vendor has v0.15.0).
- `cmd/webtools/main.go` ends with `return nil` inside `func main()`.
- `internal/mcpserver/sdk_stub.go` declares a second `NewServer` in the package
  and returns an undefined `*MCPSver`.
- `internal/mcp_fake_sdk/` is a hand-written fake SDK whose `HandleRequest`
  returns `nil, nil`.
- `internal/mcpserver/server.go` calls `mcp.Request` / `server.HandleRequest`,
  which are not the real SDK's API.

Additional defects: `Dockerfile` sets `SEARXNG_URL` to `http://searxng:808`
with an inline comment baked into the value, builds on `golang:1.21-alpine`,
runs as root and ships no `ca-certificates`; the `Makefile` `help` target is
broken awk; the README documents a nonexistent `make docker-build` and a stdio
transport the binary does not serve; `search` hard-codes a 1 s client timeout
and its test asserts that a 2 s response must fail. There is no `.gitignore`,
`.dockerignore`, CI, or git repository.

## Package layout

```
cmd/webtools/          CLI entrypoint, flag/env parsing, server wiring
internal/mcpserver/    server.go, auth.go, recorder.go, tool_fetch.go, tool_search.go
internal/tools/fetch/  HTTP fetcher
internal/tools/search/ SearXNG client
internal/storage/      GORM models, SQLite/Postgres backends, repos
internal/restapi/      REST API for humans, mounted under /api
internal/humanauth/    OIDC provider, session handling, crypto, stub auth
internal/health/       /livez + /readyz
internal/frontend/     go:embed of the built SPA
web/src/               React SPA (hash routing, esbuild bundle)
```

`internal/engine`, `internal/mcp_fake_sdk` and `internal/mcpserver/sdk_stub.go`
are deleted. `engine`'s `map[string]string` dispatcher is the reason the tools
advertise no input schema; tool handlers instead use the SDK's typed
`mcp.ToolHandlerFor[In, Out]`, which infers schemas from structs.

## Data model

Agents and humans are rows in one `Actor` table (`kind: agent | human`), so
history needs one foreign key regardless of who called.

| Model | Fields of note |
|---|---|
| `Actor` | `kind`, `name` |
| `AgentCredential` | hashed token, name, `last_used_at`, `revoked_at` |
| `UserIdentity` | issuer, subject, email, role; links to an `Actor` |
| `Session` | hashed ID, AES-256-GCM-encrypted refresh token, expiry |
| `ToolCall` | `actor_id`, `tool`, `args` (JSON), `status`, `error_message`, `duration_ms`, `response_bytes`, `response_preview`, `truncated`, `created_at` |

`ToolCall` indexes: `created_at desc`, `(actor_id, created_at)`,
`(tool, created_at)`, `status` — the four ways the UI filters.

### History recording

A generic `recorded[In, Out](db, name, handler)` wrapper applied at tool
registration. One place to test, records failures as well as successes, and a
failing insert logs and returns the tool result anyway: observability must not
break the tool it observes.

`--history-preview-bytes` (default 64 KiB) bounds the stored response preview;
rows carry a `truncated` flag and the true `response_bytes`.
`--history-retention` prunes rows older than a duration, defaulting to off —
shipped now because retrofitting a pruner onto an already-large table is worse.

## Auth

- **Agents → `/mcp`**: `RequireAgentToken` hashes the bearer token, loads the
  `Actor` into the request context, and tool handlers read it via
  `ActorFromContext`. Per-agent and revocable; no shared secret.
- **Humans → `/api`, `/`**: Keycloak/OIDC authorization-code flow, encrypted
  DB-backed sessions, `groups`-claim gating into `admin` vs `viewer`.
  `--auth-stub` provides a fixed always-admin identity for local development
  only; the server refuses to start on incomplete OIDC config rather than
  silently falling back to the stub.

Role split: `viewer` reads history; `admin` also creates agents and issues or
revokes tokens.

## HTTP surfaces

| Path | Protocol | Auth |
|---|---|---|
| `/mcp` | MCP (Streamable HTTP) | Bearer token (per-agent) |
| `/api/*` | REST + JSON | OIDC session (human) |
| `/auth/*` | OIDC login / callback / logout | — |
| `/livez`, `/readyz` | plain HTTP | none |
| `/` | Web UI (React SPA) | OIDC session (human) |

```
GET    /api/history?actor=&tool=&status=&limit=&cursor=
GET    /api/history/{id}
GET    /api/agents                            admin
POST   /api/agents                            admin
POST   /api/agents/{id}/tokens                admin, token returned once
DELETE /api/agents/{id}/tokens/{tokenID}      admin
GET    /api/me
```

If `storage.Open` fails at boot, the server serves `/livez` healthy and
`/readyz` not-ready while retrying with capped exponential backoff, then swaps
the real routes onto the same listener. A crash loop cannot fix a database
outage.

## Web UI

React SPA with hash routing, bundled by esbuild through `go generate` and
embedded with `go:embed`. React and react-dom plus the three fonts are
downloaded and SHA-256-verified at build time and served from our own origin.

Screens: **History** (list, filters, pagination), **Call detail** (arguments,
timing, response size, preview with truncation marker), **Agents** (create
agents, issue/revoke tokens; admin-only and hidden from `viewer`).

## Testing

- `tools/fetch`, `tools/search` — `httptest` servers. The `search` timeout
  becomes configurable (default 10 s) and its test drives the timeout
  explicitly instead of asserting that 2 s must fail.
- `storage` — SQLite by default; every test also runs against Postgres when
  `TEST_POSTGRES_DSN` is set, via a shared `openTestDB` helper and
  `docker-compose.yml`.
- `mcpserver` — real in-memory MCP client sessions, covering that a recorded
  failure still returns the tool error and that a failing recorder does not
  fail the call.
- `restapi` — `httptest`, including role gating (`viewer` gets 403 on agent
  management).
- `humanauth` — crypto round-trip, middleware, OIDC handlers.
- `health`, `frontend`, `cmd/webtools` — as in the reference.

## Packaging

`Makefile` with `help`, `build`, `run`, `generate`, `test`, `lint`, `gosec`,
`govulncheck`, `staticcheck`, `security`, `clean`, plus checksum-verified
vendoring targets for JS and fonts. Multi-stage `Dockerfile`
(`golang:1.26-alpine` → `alpine:3.22`), non-root user, `ca-certificates` for
`fetch`, and an asset-presence guard before the Go build. `.gitignore`,
`.dockerignore`, `docker-compose.yml`, GitHub Actions `ci.yml` and `docker.yml`
(the latter with an image smoke test). `README.md` and `docs/ROADMAP.md`
rewritten. `go.mod` moves to `go 1.26`; `go mod tidy && go mod vendor` repairs
the missing go-sdk requirement and the bogus `x/time` pin.

## Deliberate deviations from the reference

Agreed before implementation:

1. The module path stays canonical (`github.com/JetManiack/go-ai-webtools`);
   the reference's bare module name is not worth mirroring.
2. No `marked` / `DOMPurify`: nothing here is user-authored markdown, so a
   sanitizer would have nothing to sanitize.
3. No full-text search. History filters are structured (actor, tool, status,
   time); FTS exists in the reference for prose that has no analogue here.

Found while implementing, and fixed here rather than copied:

4. **`make lint` / `make staticcheck` were broken in the reference's Makefile
   pattern**, in two independent ways. It pins the tool build to
   `GOTOOLCHAIN=$(go env GOVERSION)` — the *locally installed* Go, routinely
   older than go.mod's target — so the go command upgrades only as far as the
   tool's own minimum and produces a binary that cannot load the project's
   packages ("package requires newer Go version"). And it invokes the tool by
   bare name right after `go install`, which fails outright on any machine that
   hasn't put GOPATH/bin on PATH. This Makefile derives the pin from go.mod and
   resolves each tool through `command -v` with a GOPATH/bin fallback.
5. **GORM's default logger prints `record not found` at ERROR level.** Both
   `GetOrCreateHumanActor` and `AuthenticateAgentToken` probe before writing, so
   an untouched config logs a red error on every new user's first request and
   every rejected token — training operators to ignore the level real failures
   arrive at. `internal/storage/logger.go` sets
   `IgnoreRecordNotFoundError: true`.
6. **Graceful shutdown.** The reference selects on `ctx.Done()` but runs the CLI
   with `context.Background()`, which never cancels, so SIGTERM kills in-flight
   work. `main` here uses `signal.NotifyContext`.
7. **`.golangci.yml` added.** The reference has no lint config, so its own
   `make lint` would flag `defer conn.Close()` and similar. The config excludes
   errcheck on close-only defers and disables one stylistic staticcheck rule,
   with the reasoning written in the file.
8. **Dependency and toolchain versions are current, not mirrored.**
   `govulncheck` reported 15 reachable vulnerabilities against the reference's
   pins (`golang.org/x/text` v0.21.0, `jackc/pgx` v5.6.0, plus stdlib issues
   because a bare `go 1.26` resolves to 1.26.0). go.mod pins `go 1.26.5` and the
   upgraded modules; the scan is now clean.

## Phasing

Each phase ends with a compiling, tested binary.

1. Core repair: `go.mod`, dead code removal, typed tools over Streamable HTTP,
   `internal/health`.
2. Storage: models, SQLite/Postgres backends, actor and credential repos.
3. History: recorder wrapper, history repo, REST API.
4. Humans: OIDC auth, session handling, REST role gating, React SPA.
5. Packaging: Makefile, Dockerfile, CI, compose, README, ROADMAP.
