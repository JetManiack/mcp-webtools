package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/storage"
)

// parseArgs runs the root command far enough to parse args and env, then hands
// back the populated *cli.Command without starting a server.
func parseArgs(t *testing.T, args ...string) *cli.Command {
	t.Helper()

	root := newRootCommand()
	var captured *cli.Command
	root.Action = func(_ context.Context, cmd *cli.Command) error {
		captured = cmd
		return nil
	}
	if err := root.Run(context.Background(), append([]string{"webtools"}, args...)); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	if captured == nil {
		t.Fatal("the action never ran")
	}
	return captured
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	return db
}

func TestFlagDefaults(t *testing.T) {
	cmd := parseArgs(t)

	if got := cmd.String("listen-addr"); got != ":8080" {
		t.Errorf("listen-addr = %q, want :8080", got)
	}
	if got := cmd.String("db-dsn"); got != "data/webtools.db" {
		t.Errorf("db-dsn = %q, want data/webtools.db", got)
	}
	if got := cmd.String("admin-group"); got != "admins" {
		t.Errorf("admin-group = %q, want admins", got)
	}
	if cmd.Bool("auth-stub") {
		t.Error("auth-stub defaults to true — stub auth must never be the default")
	}
	// Retention off by default: an unset flag must not mean "delete history".
	if got := cmd.Duration("history-retention"); got != 0 {
		t.Errorf("history-retention = %v, want 0", got)
	}
}

func TestFlagsReadEnvironment(t *testing.T) {
	t.Setenv("SEARXNG_URL", "http://searxng.internal:8888")
	t.Setenv("LISTEN_ADDR", ":9999")
	t.Setenv("HISTORY_RETENTION", "720h")
	t.Setenv("AUTH_STUB", "true")

	cmd := parseArgs(t)

	if got := cmd.String("searxng-url"); got != "http://searxng.internal:8888" {
		t.Errorf("searxng-url = %q, want the value from SEARXNG_URL", got)
	}
	if got := cmd.String("listen-addr"); got != ":9999" {
		t.Errorf("listen-addr = %q, want :9999", got)
	}
	if got := cmd.Duration("history-retention"); got != 720*time.Hour {
		t.Errorf("history-retention = %v, want 720h", got)
	}
	if !cmd.Bool("auth-stub") {
		t.Error("auth-stub = false, want true from AUTH_STUB")
	}
}

// Explicit flags beat the environment; otherwise a stray env var in a shell
// silently overrides what the operator just typed.
func TestFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9999")

	if got := parseArgs(t, "--listen-addr", ":7777").String("listen-addr"); got != ":7777" {
		t.Errorf("listen-addr = %q, want :7777", got)
	}
}

func TestBuildAppHandlerWithStubAuthServesEverySurface(t *testing.T) {
	db := openTestDB(t)
	handler, checker, err := buildAppHandler(context.Background(), parseArgs(t, "--auth-stub"), db)
	if err != nil {
		t.Fatalf("buildAppHandler: %v", err)
	}

	if !checker.MigrationsReady || !checker.OIDCReady {
		t.Error("readiness flags are false after a successful build")
	}
	if err := checker.Ping(context.Background()); err != nil {
		t.Errorf("readiness ping failed: %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	t.Run("frontend at root", func(t *testing.T) {
		resp, err := server.Client().Get(server.URL + "/")
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !strings.Contains(string(body), `id="root"`) {
			t.Error("/ did not serve the SPA shell")
		}
	})

	t.Run("rest api under /api", func(t *testing.T) {
		resp, err := server.Client().Get(server.URL + "/api/me")
		if err != nil {
			t.Fatalf("GET /api/me: %v", err)
		}
		defer resp.Body.Close()
		// Stub auth authenticates everyone, so this must succeed — and it proves
		// the /api prefix is actually stripped before the router sees the path.
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("mcp requires a token", func(t *testing.T) {
		resp, err := server.Client().Post(server.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatalf("POST /mcp: %v", err)
		}
		defer resp.Body.Close()
		// Human stub auth must not leak into the agent surface: /mcp is
		// bearer-token-only regardless of --auth-stub.
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("no oidc routes in stub mode", func(t *testing.T) {
		resp, err := server.Client().Get(server.URL + "/auth/login")
		if err != nil {
			t.Fatalf("GET /auth/login: %v", err)
		}
		defer resp.Body.Close()
		// With no OIDC configured there is nothing to log into; the SPA shell
		// (served by the catch-all) is fine, a working login route is not.
		if resp.StatusCode == http.StatusFound {
			t.Error("/auth/login redirects in stub mode, but no OIDC client is configured")
		}
	})
}

// The single most important safety property of the wiring: without --auth-stub,
// incomplete OIDC configuration must be a hard startup failure. Falling back to
// the stub would silently publish every agent's history.
func TestBuildAppHandlerRefusesIncompleteOIDCConfig(t *testing.T) {
	full := []string{
		"--oidc-issuer", "https://keycloak.example.com/realms/webtools",
		"--oidc-client-id", "webtools",
		"--oidc-client-secret", "secret",
		"--public-url", "https://webtools.example.com",
		"--session-encryption-key", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}

	tests := []struct {
		name    string
		omit    string
		wantErr string
	}{
		{name: "nothing configured", omit: "all", wantErr: "OIDC auth requires"},
		{name: "no issuer", omit: "--oidc-issuer", wantErr: "OIDC auth requires"},
		{name: "no client id", omit: "--oidc-client-id", wantErr: "OIDC auth requires"},
		{name: "no client secret", omit: "--oidc-client-secret", wantErr: "OIDC auth requires"},
		{name: "no public url", omit: "--public-url", wantErr: "OIDC auth requires"},
		{name: "no encryption key", omit: "--session-encryption-key", wantErr: "OIDC auth requires"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)

			var args []string
			if tt.omit != "all" {
				for i := 0; i < len(full); i += 2 {
					if full[i] == tt.omit {
						continue
					}
					args = append(args, full[i], full[i+1])
				}
			}

			_, _, err := buildAppHandler(context.Background(), parseArgs(t, args...), db)
			if err == nil {
				t.Fatal("buildAppHandler succeeded with incomplete OIDC config")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildAppHandlerRejectsBadEncryptionKey(t *testing.T) {
	base := []string{
		"--oidc-issuer", "https://keycloak.example.com/realms/webtools",
		"--oidc-client-id", "webtools",
		"--oidc-client-secret", "secret",
		"--public-url", "https://webtools.example.com",
	}

	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{name: "not base64", key: "!!!not base64!!!", wantErr: "valid base64"},
		{name: "too short", key: "c2hvcnQ=", wantErr: "32 bytes"},
		{name: "too long", key: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYwMTIzNDU2Nzg5YWJjZGVm", wantErr: "32 bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			args := append(append([]string{}, base...), "--session-encryption-key", tt.key)

			_, _, err := buildAppHandler(context.Background(), parseArgs(t, args...), db)
			if err == nil {
				t.Fatal("buildAppHandler accepted an invalid encryption key")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewServerTimeouts(t *testing.T) {
	server := newServer(":8080", http.NotFoundHandler())

	if server.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout is unset, leaving the listener open to slowloris")
	}
	// WriteTimeout has to outlast the slowest tool call the MCP surface can
	// make, or the transport cuts responses the tool was about to deliver.
	if server.WriteTimeout <= 15*time.Second {
		t.Errorf("WriteTimeout = %v, want more than the default fetch timeout", server.WriteTimeout)
	}
	if server.IdleTimeout == 0 {
		t.Error("IdleTimeout is unset")
	}
}

func TestPruneHistoryUntilDoneWithoutRetentionReturnsImmediately(t *testing.T) {
	db := openTestDB(t)

	done := make(chan struct{})
	go func() {
		pruneHistoryUntilDone(context.Background(), db, 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pruneHistoryUntilDone did not return with retention disabled")
	}
}

func TestPruneHistoryUntilDonePrunesThenStopsOnContextCancel(t *testing.T) {
	db := openTestDB(t)
	agent, err := storage.CreateAgent(db, "scraper-1")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	old := &storage.ToolCall{
		ActorID:   agent.ID,
		Tool:      "fetch",
		InputJSON: "{}",
		CalledAt:  time.Now().Add(-48 * time.Hour),
	}
	if err := storage.RecordToolCall(db, old); err != nil {
		t.Fatalf("RecordToolCall: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pruneHistoryUntilDone(ctx, db, time.Hour)
		close(done)
	}()

	// The first sweep runs before the first tick, so the expired row goes
	// promptly rather than an hour after startup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int64
		if err := db.Model(&storage.ToolCall{}).Count(&count).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the expired row was not pruned on the first sweep")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pruneHistoryUntilDone did not stop when its context was cancelled")
	}
}
