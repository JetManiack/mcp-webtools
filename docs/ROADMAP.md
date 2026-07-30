# Roadmap

## Shipped

- **2 MCP tools** — `fetch` and `search`, served over Streamable HTTP at
  `/mcp` with per-agent bearer-token auth and schemas inferred from typed
  Go input/output structs.
- **Tool-call history** — one `ToolCall` row per invocation (arguments,
  outcome, error message, duration, response size, bounded response
  preview), written by a single generic wrapper applied at tool
  registration. A failed insert logs and still returns the agent's result.
- **REST API for humans** — filtered, keyset-paginated history, a single
  call view, the tool names present in history, agent + token management,
  `/api/me` — mounted under `/api`.
- **Web UI** — React SPA (hash routing): history list with tool/outcome/
  agent filters and cursor paging, call detail with arguments and response
  preview, an Agents screen for registering agents and issuing/revoking
  tokens.
- **Human auth** — Keycloak/OIDC as the default and only auth path for real
  deployments: authorization-code flow, encrypted (AES-256-GCM) DB-backed
  sessions with hashed IDs, refresh-token rotation, group-based role gating
  (`admin` vs `viewer`). `StubProvider` remains available via `--auth-stub`
  for local development only, and the server refuses to start with
  incomplete OIDC config rather than silently falling back to it.
- **Dual storage backend** — SQLite (default, pure Go, no CGO) and Postgres,
  both behind the same `storage.Open(dsn)` entrypoint, with a session
  advisory lock serializing concurrent-replica migrations on Postgres.
- **Bounded resource use** — configurable per-tool timeouts, a maximum
  `fetch` body size, a capped `search` result count, a capped history
  preview, and optional `--history-retention` pruning.
- **Packaging** — multi-stage Alpine `Dockerfile` (non-root, no CGO,
  `ca-certificates` for `fetch`), `docker-compose.yml` for a local Postgres
  test instance, `Makefile` with build/test/lint/security targets, GitHub
  Actions CI (vet, test, lint, gosec, govulncheck, Docker build, Postgres
  storage suite) and a publish workflow pushing multi-platform images to
  GHCR on `main` and `v*` tags.
- **Kubernetes health endpoints** — unauthenticated `/livez` (unconditional
  once serving) and `/readyz` (gated on a pooled DB ping; JSON body naming
  the failing dependency on 503), plus a degraded-start path that keeps the
  pod alive and retries the database instead of crash-looping.
- **Clean static analysis** — `go vet`, `golangci-lint`, `staticcheck` and
  `gosec` report nothing, and `govulncheck` reports zero reachable
  vulnerabilities (which required `go 1.26.5` plus current `x/text` and `pgx`;
  see the design doc's deviations section).

## Outstanding before production deployment

- [ ] **Verify OIDC against a real Keycloak instance.** The flow is ported
  from a deployment where it was confirmed working, but has not been
  re-verified end-to-end here.
- [ ] **Decide on a license.**
- [ ] **Tag and publish a first release** (`v0.0.1`) once the above is
  confirmed, or explicitly deferred with the risk accepted.

## Ideas, not yet planned

- **Rate limiting per agent.** Nothing currently stops one agent from
  driving `fetch` in a tight loop; history makes it visible after the fact
  but doesn't slow it down.
- **SSRF controls on `fetch`.** The tool refuses non-http(s) schemes, but
  will happily fetch `http://169.254.169.254/` or a private-range address.
  A deployment reachable by untrusted agents needs a deny-list or an egress
  proxy.
- **A dashboard** — call volume, error rate, slowest tools, most-fetched
  hosts. The data is already in `ToolCall`; only the aggregation queries and
  a screen are missing.
- **Response body storage** for calls worth keeping whole, e.g. opt-in per
  agent or per tool, with object storage rather than a text column.
- **More tools** — the registration surface (`RegisterTools`) and the
  recording wrapper are generic, so a new tool is one file plus one
  `mcp.AddTool` call.
- **Multi-tenancy** (currently a single shared history).
