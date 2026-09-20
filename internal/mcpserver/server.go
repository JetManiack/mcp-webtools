// Package mcpserver exposes this service's web tools over MCP, recording
// every call to history.
package mcpserver

import (
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/auth"
)

// ServerName is the MCP implementation name advertised to clients.
const ServerName = "mcp-webtools"

// fallbackVersion is reported when the binary was not stamped at build time.
const fallbackVersion = "dev"

// ToolRegistrar registers one or more tools onto an MCP server.
type ToolRegistrar interface {
	Register(srv *mcp.Server, db *gorm.DB)
}

func clearWriteDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		next.ServeHTTP(w, r)
	})
}

// Handler builds the full /mcp handler: Streamable HTTP transport,
// registered tools via ToolRegistrar, wrapped in bearer-token auth.
func Handler(db *gorm.DB, tools []ToolRegistrar) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: fallbackVersion}, nil)
	for _, t := range tools {
		t.Register(srv, db)
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return clearWriteDeadline(auth.RequireBearer(db, mcpHandler))
}
