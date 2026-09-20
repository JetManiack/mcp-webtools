# go-ai-webtools

An MCP server that gives AI agents two web tools — `fetch` and `search` — and
gives humans a record of how they were used. Every tool call an agent makes is
recorded: what it asked for, whether it worked, how long it took, and what
came back. Humans read that history through a REST API and a web UI, gated
behind Keycloak/OIDC login.

## Why

Handing an agent the open web is easy; knowing what it then did with it is
not. Without a record, a misbehaving agent looks exactly like a working one:
you can't tell a tight retry loop from normal traffic, or find the request
that returned the page that led the agent astray. This service is the tool
layer and the audit trail in one binary — an agent gets `fetch` and `search`,
and you get the log.

## Features

- **2 MCP tools** for agents: `fetch` (retrieve an http(s) URL; bodies longer
  than the page size come back paginated via `offset`/`next_offset`, and
  continuation pages are served from a bounded snapshot cache so the origin
  is fetched once per document, not once per page) and `search` (query a
  [SearXNG](https://docs.searxng.org) instance), served over Streamable HTTP
  at `/mcp` with typed input/output schemas. Completed `fetch` pages and
  `search` results are cached for `--cache-ttl` (default 5m), so an identical
  repeat within that window is served from memory without re-hitting the
  origin or the SearXNG instance — each cache is per-agent and never stores
  failures.
- **Per-agent bearer tokens** — each agent is an `Actor` with its own
  revocable credential; no shared secret.
- **Tool-call history** — one row per invocation with arguments, outcome,
  duration, response size and a bounded response preview. Recording can never
  fail the call it records.
- **Bounded by construction** — per-request timeouts, a page size on `fetch`
  (a longer body is truncated to a page; the agent retrieves the rest with
  the `offset` field), byte-budgeted LRU caches with TTLs (document
  snapshots, `fetch` pages, `search` results), a capped result count on
  `search`, a capped history preview, and optional history retention.
- **REST API + React web UI** for humans: browse and filter history, inspect a
  single call, and manage agent credentials.
- **Keycloak/OIDC login** for humans, with group-based role gating (`admin` vs
  read-only `viewer`) and encrypted, DB-backed sessions.
- **Dual storage backend** — SQLite (default, zero external dependencies) or
  Postgres, selected automatically from the DSN scheme.
- **Kubernetes health endpoints** — `/livez` and `/readyz`, and a server that
  starts and stays up while waiting for an unavailable database rather than
  crash-looping.

## Architecture

```
cmd/webtools/          CLI entrypoint, flag/env parsing, server wiring
internal/mcpserver/    MCP tools + bearer-token auth + history recording (/mcp)
internal/tools/fetch/  HTTP fetcher
internal/tools/search/ SearXNG client
internal/cache/        Shared byte-budgeted LRU cache with TTL (document snapshots, fetch pages, search results)
internal/restapi/      REST API for humans, mounted under /api
internal/humanauth/    OIDC provider, session handling, stub auth (dev only)
internal/storage/      GORM models + SQLite/Postgres backends
internal/health/       /livez and /readyz
internal/frontend/     Embeds the built web UI, served at /
web/                   React SPA (hash routing, esbuild bundle)
```

A single Go binary serves every surface on one port:

| Path                | Protocol              | Auth                          |
|---------------------|-----------------------|-------------------------------|
| `/mcp`              | MCP (Streamable HTTP) | Bearer token (per-agent)      |
| `/api/*`            | REST + JSON           | Keycloak/OIDC session (human) |
| `/auth/*`           | OIDC login/callback   | —                             |
| `/livez`, `/readyz` | plain HTTP            | none                          |
| `/`                 | Web UI (React SPA)    | Keycloak/OIDC session (human) |

### Data model

Both AI agents and humans are rows in a single `Actor` table (`kind`: `agent`
or `human`), so a history row needs one foreign key regardless of who caused
it. Agents authenticate with a revocable bearer token (`AgentCredential`);
humans authenticate via an OIDC session (`UserIdentity` + `Session`).

`ToolCall` holds the history: full arguments (a URL plus page offset, or a
query — small enough to keep whole), plus outcome, `duration_ms`,
`response_bytes`, and a `response_preview` capped at
`--history-preview-bytes`. A fetched document can be megabytes, so `fetch`
returns it in pages — each page its own call and its own history row, with
the preview cap and `truncated` flag still applied per row.

## Getting started

### Requirements

- Go 1.26+
- `esbuild` (for building the web UI bundle — installed automatically in
  Docker; install locally via your package manager for `make generate`)
- A SearXNG instance with the JSON format enabled, for the `search` tool

### Run locally (stub auth)

For local development without a Keycloak instance, use `--auth-stub` (or
`AUTH_STUB=true`), which authenticates every human request as a fixed
always-admin identity. **Never use this in a real deployment** — history
contains every URL and query your agents have sent.

```sh
make run   # builds the frontend bundle and starts the server on :8080
```

Equivalent manually:

```sh
go generate ./internal/frontend/...
go run ./cmd/webtools --auth-stub --searxng-url http://localhost:8888
```

### Run with real Keycloak/OIDC

OIDC is the default and required auth mode outside `--auth-stub`. You need:

1. A Keycloak realm with a dedicated OIDC client for this app, redirect URI
   `<PUBLIC_URL>/auth/callback`.
2. A group-membership mapper so the `groups` claim appears in the ID token, and
   a group (`admins` by default) whose members should get the `admin` role.
   Everyone else gets `viewer` (read-only: history, no agent management).

```sh
go run ./cmd/webtools \
  --searxng-url https://searxng.internal \
  --oidc-issuer https://keycloak.example.com/realms/webtools \
  --oidc-client-id webtools \
  --oidc-client-secret "$OIDC_CLIENT_SECRET" \
  --public-url https://webtools.example.com \
  --admin-group admins \
  --session-encryption-key "$(openssl rand -base64 32)"
```

### Configuration

Every flag has an environment-variable equivalent (see `--help`):

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `--listen-addr` | `LISTEN_ADDR` | `:8080` | Listen address |
| `--db-dsn` | `DB_DSN` | `data/webtools.db` | SQLite path or `postgres://…` |
| `--searxng-url` | `SEARXNG_URL` | `http://localhost:8080` | SearXNG base URL |
| `--fetch-timeout` | `FETCH_TIMEOUT` | `15s` | Per-request timeout for `fetch` |
| `--fetch-max-bytes` | `FETCH_MAX_BYTES` | `65536` | Page size for the body `fetch` returns; longer bodies are paginated via the `offset` field |
| `--fetch-cache-max-bytes` | `FETCH_CACHE_MAX_BYTES` | `33554432` | Byte budget of the `fetch` snapshot and results caches (LRU eviction) |
| `--cache-ttl` | `CACHE_TTL` | `5m` | TTL of cached fetch pages, document snapshots, and search results; a non-positive value uses the default |
| `--search-timeout` | `SEARCH_TIMEOUT` | `10s` | Per-request timeout for `search` |
| `--history-preview-bytes` | `HISTORY_PREVIEW_BYTES` | `65536` | Stored response preview cap |
| `--history-retention` | `HISTORY_RETENTION` | `0` (keep all) | Prune history older than this |
| `--auth-stub` | `AUTH_STUB` | off | Fixed always-admin identity (dev only) |
| `--oidc-issuer` | `OIDC_ISSUER` | — | Keycloak realm issuer URL |
| `--oidc-client-id` | `OIDC_CLIENT_ID` | — | OIDC client ID |
| `--oidc-client-secret` | `OIDC_CLIENT_SECRET` | — | OIDC client secret |
| `--public-url` | `PUBLIC_URL` | — | Externally-reachable base URL |
| `--admin-group` | `ADMIN_GROUP` | `admins` | Group mapped to role `admin` |
| `--session-encryption-key` | `SESSION_ENCRYPTION_KEY` | — | base64 32-byte session key |

### Storage backend

`--db-dsn` selects the backend by scheme:

- `postgres://…` / `postgresql://…` → Postgres. Concurrent replicas serialize
  their migrations through a session advisory lock.
- Anything else (a file path, `:memory:`) → SQLite, via a pure-Go driver (no
  CGO), with WAL and a busy timeout so concurrent writers don't fail fast.

History is append-only and grows with agent traffic. On SQLite that's fine for
a single modest deployment; past that, move to Postgres and/or set
`--history-retention`.

### Docker

```sh
docker build -t webtools .
docker run -p 8080:8080 -v webtools-data:/app/data webtools --auth-stub
```

Published images: `ghcr.io/jetmaniack/go-ai-webtools` (built for `linux/amd64`
and `linux/arm64` on every push to `main` and every `v*` tag).

## Connecting an agent

1. As an admin, register an agent and issue it a token in the web UI (Agents
   tab) or via `POST /api/agents` + `POST /api/agents/{id}/tokens`. The raw
   token is shown once and only its hash is stored.
2. Point your MCP client at `<PUBLIC_URL>/mcp` with
   `Authorization: Bearer <token>`.
3. The agent can now call `fetch` and `search` — and every call shows up under
   History.

## REST API

```
GET    /api/history?actor=&tool=&status=&limit=&cursor=   newest first, keyset-paginated
GET    /api/history/tools                                 tool names present in history
GET    /api/history/{id}                                  one call
GET    /api/agents                                        admin
POST   /api/agents                                        admin — register an agent
DELETE /api/agents/{id}                                   admin — revoke all its tokens
GET    /api/agents/{id}/tokens                            admin
POST   /api/agents/{id}/tokens                            admin — issue a token
DELETE /api/agents/{id}/tokens/{tokenID}                  admin — revoke one token
GET    /api/me                                            current identity + role
```

Deleting an agent revokes its credentials instead of removing the row, so the
history it produced keeps a named owner.

## Development

```sh
make test      # go test ./...
make lint      # golangci-lint
make security  # gosec + govulncheck + staticcheck
make build     # regenerate frontend bundle, build bin/webtools
```

To exercise the Postgres backend locally:

```sh
docker compose up -d
TEST_POSTGRES_DSN=postgres://webtools:webtools@localhost:5432/webtools_test \
  go test ./internal/storage/... -v
docker compose down
```

See [`docs/ROADMAP.md`](docs/ROADMAP.md) for what's shipped and what's still
outstanding before a production deployment.

## License

Not yet decided.
