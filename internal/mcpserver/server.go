// Package mcpserver exposes this service's web tools over MCP, recording
// every call to history.
package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/go-ai-webtools/internal/tools/fetch"
	"github.com/JetManiack/go-ai-webtools/internal/tools/search"
)

// ServerName is the MCP implementation name advertised to clients.
const ServerName = "go-ai-webtools"

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
		Description: "Retrieve the content of an http(s) URL",
	}, recorded(rec, "fetch", fetchHandler(deps.Fetcher)))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Description: "Search the web via a SearXNG instance",
	}, recorded(rec, "search", searchHandler(deps.Searcher)))
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
	return RequireAgentToken(deps.DB, mcpHandler)
}
