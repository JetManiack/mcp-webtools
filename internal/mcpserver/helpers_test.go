package mcpserver

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/auth"
	"github.com/JetManiack/mcp-webtools/internal/storage"
)

// openTestDB gives each test its own migrated SQLite database. The Postgres
// backend is covered by internal/storage's suite; what matters here is the MCP
// behavior on top of it, so this deliberately uses the zero-setup backend.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	return db
}

func mustAgent(t *testing.T, db *gorm.DB, name string) *storage.Actor {
	t.Helper()
	agent, err := storage.CreateAgent(db, name)
	if err != nil {
		t.Fatalf("CreateAgent(%q): %v", name, err)
	}
	return agent
}

// mustAgentWithToken registers an agent and issues it a usable bearer token.
func mustAgentWithToken(t *testing.T, db *gorm.DB, name string) (*storage.Actor, string) {
	t.Helper()
	agent := mustAgent(t, db, name)
	token, err := auth.Issue(db, agent.ID, "")
	if err != nil {
		t.Fatalf("auth.Issue: %v", err)
	}
	return agent, token
}
