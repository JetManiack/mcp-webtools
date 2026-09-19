// Package mcpserver exposes this service's web tools over MCP, recording
// every call to history.
package mcpserver

import (
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/auth"
	"github.com/JetManiack/mcp-webtools/internal/tools/fetch"
	"github.com/JetManiack/mcp-webtools/internal/tools/search"
)

// ServerName is the MCP implementation name advertised to clients.
const ServerName = "mcp-webtools"

// fallbackVersion is reported when Deps.Version is empty (an unstamped
// `go build`, as opposed to a release built through the Makefile).
const fallbackVersion = "dev"

// Deps is everything the MCP surface needs: the database history is written
// to, and the configured tool clients.
type Deps struct {
	DB       *gorm.DB
	Fetcher  *fetch.Fetcher
	Searcher *search.Searcher

	// PreviewBytes bounds the stored response preview per call; zero means
	// DefaultPreviewBytes.
	PreviewBytes int

	// Version is advertised to MCP clients as the server version.
	Version string
}

// RegisterTools adds every MCP tool this server exposes to server, each
// wrapped so its calls land in history.
func RegisterTools(server *mcp.Server, deps Deps) {
	rec := Recorder{DB: deps.DB, PreviewBytes: deps.PreviewBytes}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch",
		Description: "Retrieve the content of an http(s) URL. Bodies longer than the server's page size come back in pages: when truncated is true, call fetch again with the result's next_offset as offset to read the next page.",
	}, recorded(rec, "fetch", fetch.Handler(deps.Fetcher)))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Description: "Search the web via a SearXNG instance",
	}, recorded(rec, "search", search.Handler(deps.Searcher)))
}

// NewServer builds the MCP server with every tool registered.
func NewServer(deps Deps) *mcp.Server {
	version := deps.Version
	if version == "" {
		version = fallbackVersion
	}
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version}, nil)
	RegisterTools(server, deps)
	return server
}

// NewHTTPHandler builds the full /mcp handler: Streamable HTTP transport,
// every registered tool, wrapped in bearer-token authentication.
func NewHTTPHandler(deps Deps) http.Handler {
	server := NewServer(deps)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	return auth.RequireBearer(deps.DB, mcpHandler)
}

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
	version := fallbackVersion
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version}, nil)
	for _, t := range tools {
		t.Register(srv, db)
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return clearWriteDeadline(auth.RequireBearer(db, mcpHandler))
}
