package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
	"gorm.io/gorm"

	"github.com/JetManiack/go-ai-webtools/internal/frontend"
	"github.com/JetManiack/go-ai-webtools/internal/health"
	"github.com/JetManiack/go-ai-webtools/internal/humanauth"
	"github.com/JetManiack/go-ai-webtools/internal/mcpserver"
	"github.com/JetManiack/go-ai-webtools/internal/restapi"
	"github.com/JetManiack/go-ai-webtools/internal/storage"
	"github.com/JetManiack/go-ai-webtools/internal/tools/fetch"
	"github.com/JetManiack/go-ai-webtools/internal/tools/search"
)

// version is stamped at build time by the Makefile (-X main.version=...).
var version = "dev"

// historyPruneInterval is how often expired history is swept when
// --history-retention is set. Retention is a coarse policy — an hourly sweep
// keeps the delete batches small without pretending to be precise.
const historyPruneInterval = time.Hour

func newRootCommand() *cli.Command {
	return &cli.Command{
		Name:    "webtools",
		Usage:   "MCP server exposing web tools to AI agents, with a history UI for humans",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "listen-addr",
				Value:   ":8080",
				Sources: cli.EnvVars("LISTEN_ADDR"),
			},
			&cli.StringFlag{
				Name:    "db-dsn",
				Value:   "data/webtools.db",
				Usage:   "SQLite file path, or a postgres:// connection string",
				Sources: cli.EnvVars("DB_DSN"),
			},
			&cli.StringFlag{
				Name:    "searxng-url",
				Value:   "http://localhost:8080",
				Usage:   "base URL of the SearXNG instance backing the search tool",
				Sources: cli.EnvVars("SEARXNG_URL"),
			},
			&cli.DurationFlag{
				Name:    "fetch-timeout",
				Value:   fetch.DefaultTimeout,
				Usage:   "per-request timeout for the fetch tool",
				Sources: cli.EnvVars("FETCH_TIMEOUT"),
			},
			&cli.IntFlag{
				Name:    "fetch-max-bytes",
				Value:   fetch.DefaultMaxBytes,
				Usage:   "maximum response body the fetch tool returns; longer bodies are truncated",
				Sources: cli.EnvVars("FETCH_MAX_BYTES"),
			},
			&cli.DurationFlag{
				Name:    "search-timeout",
				Value:   search.DefaultTimeout,
				Usage:   "per-request timeout for the search tool",
				Sources: cli.EnvVars("SEARCH_TIMEOUT"),
			},
			&cli.IntFlag{
				Name:    "history-preview-bytes",
				Value:   mcpserver.DefaultPreviewBytes,
				Usage:   "how much of each tool response is kept in history",
				Sources: cli.EnvVars("HISTORY_PREVIEW_BYTES"),
			},
			&cli.DurationFlag{
				Name:    "history-retention",
				Usage:   "delete history older than this (e.g. 720h); zero, the default, keeps everything",
				Sources: cli.EnvVars("HISTORY_RETENTION"),
			},
			&cli.BoolFlag{
				Name:    "auth-stub",
				Usage:   "use a fixed, always-admin test identity instead of real Keycloak/OIDC auth — local development only, never set this in a real deployment",
				Sources: cli.EnvVars("AUTH_STUB"),
			},
			&cli.StringFlag{
				Name:    "oidc-issuer",
				Usage:   "Keycloak realm issuer URL, e.g. https://keycloak.internal/realms/webtools",
				Sources: cli.EnvVars("OIDC_ISSUER"),
			},
			&cli.StringFlag{
				Name:    "oidc-client-id",
				Usage:   "client ID of the dedicated OIDC client configured in Keycloak for this app",
				Sources: cli.EnvVars("OIDC_CLIENT_ID"),
			},
			&cli.StringFlag{
				Name:    "oidc-client-secret",
				Usage:   "client secret of the dedicated OIDC client configured in Keycloak for this app",
				Sources: cli.EnvVars("OIDC_CLIENT_SECRET"),
			},
			&cli.StringFlag{
				Name:    "public-url",
				Usage:   "this server's externally-reachable base URL (used to build the OIDC redirect URI <public-url>/auth/callback — must match what's configured on the Keycloak client)",
				Sources: cli.EnvVars("PUBLIC_URL"),
			},
			&cli.StringFlag{
				Name:    "admin-group",
				Value:   "admins",
				Usage:   "Keycloak group (from the ID token's groups claim) whose members get role admin; everyone else gets viewer",
				Sources: cli.EnvVars("ADMIN_GROUP"),
			},
			&cli.StringFlag{
				Name:    "session-encryption-key",
				Usage:   "base64-encoded 32-byte key used to encrypt session refresh tokens at rest (generate one with: openssl rand -base64 32)",
				Sources: cli.EnvVars("SESSION_ENCRYPTION_KEY"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			dsn := cmd.String("db-dsn")
			db, err := storage.Open(dsn)
			if err != nil {
				slog.Error("storage.Open failed at boot; serving /livez and /readyz (not ready) while retrying in the background instead of exiting", "error", err)
				return serveDegradedUntilReady(ctx, cmd, dsn, err)
			}
			return serveReady(ctx, cmd, db)
		},
	}
}

// buildAppHandler builds every route this app serves once db is available —
// MCP, the REST API, the static frontend, and (if OIDC is enabled) the
// login/callback/logout routes — plus the ReadyChecker that reflects db and
// OIDC actually being usable. It's shared between the immediate-ready
// startup path (serveReady) and the recovers-after-retry path
// (serveDegradedUntilReady), so both build identical routes.
func buildAppHandler(ctx context.Context, cmd *cli.Command, db *gorm.DB) (http.Handler, health.ReadyChecker, error) {
	var authProvider humanauth.Provider
	var oidcHandlers humanauth.OIDCHandlers
	useOIDC := !cmd.Bool("auth-stub")

	if useOIDC {
		if cmd.String("oidc-issuer") == "" || cmd.String("oidc-client-id") == "" || cmd.String("oidc-client-secret") == "" || cmd.String("public-url") == "" || cmd.String("session-encryption-key") == "" {
			return nil, health.ReadyChecker{}, errors.New("OIDC auth requires --oidc-issuer, --oidc-client-id, --oidc-client-secret, --public-url, and --session-encryption-key (or pass --auth-stub for local development)")
		}
		encryptionKey, err := base64.StdEncoding.DecodeString(cmd.String("session-encryption-key"))
		if err != nil {
			return nil, health.ReadyChecker{}, fmt.Errorf("--session-encryption-key must be valid base64: %w", err)
		}
		if len(encryptionKey) != 32 {
			return nil, health.ReadyChecker{}, fmt.Errorf("--session-encryption-key must decode to exactly 32 bytes, got %d (generate one with: openssl rand -base64 32)", len(encryptionKey))
		}
		cfg := humanauth.OIDCConfig{
			Issuer:        cmd.String("oidc-issuer"),
			ClientID:      cmd.String("oidc-client-id"),
			ClientSecret:  cmd.String("oidc-client-secret"),
			PublicURL:     cmd.String("public-url"),
			AdminGroup:    cmd.String("admin-group"),
			EncryptionKey: encryptionKey,
		}
		provider, handlers, err := humanauth.NewOIDCHandlers(ctx, db, cfg)
		if err != nil {
			return nil, health.ReadyChecker{}, fmt.Errorf("configure OIDC: %w", err)
		}
		authProvider = provider
		oidcHandlers = handlers
	} else {
		// ⚠️ StubProvider authenticates every request as a fixed always-admin
		// identity with no credential check at all. Only reachable via
		// --auth-stub, which must never be set outside local development:
		// history holds every URL and query the agents have sent.
		authProvider = humanauth.StubProvider{}
		slog.Warn("human auth running in STUB mode — do not deploy to production")
	}

	frontendFS, err := frontend.FS(false)
	if err != nil {
		return nil, health.ReadyChecker{}, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, health.ReadyChecker{}, fmt.Errorf("get pooled db handle: %w", err)
	}
	readyChecker := health.ReadyChecker{
		Ping:            sqlDB.PingContext,
		MigrationsReady: true,
		OIDCReady:       true,
	}

	mcpDeps := mcpserver.Deps{
		DB:           db,
		Fetcher:      fetch.New(cmd.Duration("fetch-timeout"), int64(cmd.Int("fetch-max-bytes"))),
		Searcher:     search.New(cmd.String("searxng-url"), cmd.Duration("search-timeout")),
		PreviewBytes: cmd.Int("history-preview-bytes"),
		Version:      version,
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewHTTPHandler(mcpDeps))
	mux.Handle("/api/", http.StripPrefix("/api", restapi.NewHandler(db, authProvider)))
	if useOIDC {
		mux.HandleFunc("/auth/login", oidcHandlers.Login)
		mux.HandleFunc("/auth/callback", oidcHandlers.Callback)
		mux.HandleFunc("/auth/logout", oidcHandlers.Logout)
	}
	mux.Handle("/", http.FileServer(frontendFS))

	return mux, readyChecker, nil
}

// newServer builds the *http.Server this app always serves with, regardless
// of which startup path constructed handler.
func newServer(addr string, handler http.Handler) *http.Server {
	// WriteTimeout has to accommodate the slowest tool call a client can make
	// through /mcp, which is bounded by --fetch-timeout and --search-timeout
	// (15s and 10s by default) plus response encoding — hence 60s rather than
	// the usual 30s. Raising either tool timeout past that means raising this
	// too, or the transport cuts the response the tool was about to deliver.
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// serveReady is the happy-path startup: db is already open, so every route
// builds immediately and the server starts serving fully-ready from the very
// first accepted connection.
func serveReady(ctx context.Context, cmd *cli.Command, db *gorm.DB) error {
	appHandler, readyChecker, err := buildAppHandler(ctx, cmd, db)
	if err != nil {
		return err
	}

	go pruneHistoryUntilDone(ctx, db, cmd.Duration("history-retention"))

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", health.Livez)
	mux.HandleFunc("/readyz", readyChecker.Readyz)
	mux.Handle("/", appHandler)

	server := newServer(cmd.String("listen-addr"), mux)
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("starting server", "addr", server.Addr, "version", version)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		return shutdown(server)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// serveDegradedUntilReady is reached only when storage.Open fails at boot.
// Kubernetes needs a live pod to probe — a crash loop only delays recovery
// and never fixes an outage the app itself can't fix — so this starts
// listening immediately with /livez healthy and /readyz not-ready, and
// retries storage.Open with capped exponential backoff in the background.
// Once it succeeds, the real routes are swapped in atomically on the same
// listener (no restart) and control folds into the same serve/shutdown loop
// serveReady uses.
func serveDegradedUntilReady(ctx context.Context, cmd *cli.Command, dsn string, firstErr error) error {
	var mu sync.RWMutex
	checker := health.ReadyChecker{
		Ping:            func(context.Context) error { return firstErr },
		MigrationsReady: false,
		OIDCReady:       false,
	}
	var appHandler atomic.Pointer[http.Handler]

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", health.Livez)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		c := checker
		mu.RUnlock()
		c.Readyz(w, r)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := appHandler.Load(); h != nil {
			(*h).ServeHTTP(w, r)
			return
		}
		http.Error(w, "starting up: database not yet available", http.StatusServiceUnavailable)
	}))

	server := newServer(cmd.String("listen-addr"), mux)
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("starting server (degraded: database not yet available)", "addr", server.Addr, "version", version)
		serveErr <- server.ListenAndServe()
	}()

	dbReady := make(chan *gorm.DB, 1)
	go retryOpenUntilReady(ctx, dsn, &mu, &checker, dbReady)

	for {
		select {
		case <-ctx.Done():
			return shutdown(server)
		case err := <-serveErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case db := <-dbReady:
			handler, readyChecker, err := buildAppHandler(ctx, cmd, db)
			if err != nil {
				if shutdownErr := shutdown(server); shutdownErr != nil {
					slog.Error("shutdown after failed route build", "error", shutdownErr)
				}
				return err
			}
			h := handler
			appHandler.Store(&h)
			mu.Lock()
			checker = readyChecker
			mu.Unlock()
			go pruneHistoryUntilDone(ctx, db, cmd.Duration("history-retention"))
			slog.Info("database became available; now serving normally")
		}
	}
}

// retryOpenUntilReady retries storage.Open(dsn) with capped exponential
// backoff until it succeeds or ctx is done, updating checker's Ping (via mu)
// with the latest failure after every attempt so /readyz always names the
// current reason rather than the boot-time one. Sends the opened *gorm.DB on
// ready and returns; it never retries again after a success, matching the
// "captured once at startup" contract the rest of the readiness design relies
// on.
func retryOpenUntilReady(ctx context.Context, dsn string, mu *sync.RWMutex, checker *health.ReadyChecker, ready chan<- *gorm.DB) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		db, err := storage.Open(dsn)
		if err == nil {
			ready <- db
			return
		}

		slog.Error("storage.Open retry failed", "error", err)
		mu.Lock()
		checker.Ping = func(context.Context) error { return err }
		mu.Unlock()

		backoff = min(backoff*2, maxBackoff)
	}
}

// pruneHistoryUntilDone sweeps expired tool-call history on an interval. A
// non-positive retention disables pruning entirely and returns immediately —
// keeping everything is the default, and must never be reinterpreted as
// "delete everything".
func pruneHistoryUntilDone(ctx context.Context, db *gorm.DB, retention time.Duration) {
	if retention <= 0 {
		return
	}
	slog.Info("history pruning enabled", "retention", retention, "interval", historyPruneInterval)

	ticker := time.NewTicker(historyPruneInterval)
	defer ticker.Stop()
	for {
		deleted, err := storage.PruneToolCalls(db, retention)
		if err != nil {
			slog.Error("history pruning failed", "error", err)
		} else if deleted > 0 {
			slog.Info("pruned expired history", "rows", deleted)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func shutdown(server *http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func main() {
	// SIGTERM is how a container runtime asks for a graceful stop; without
	// this, in-flight tool calls are cut mid-response on every rollout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCommand().Run(ctx, os.Args); err != nil {
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}
