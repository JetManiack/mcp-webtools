package mcpserver

// This file provides the legacy Deps-based API used by server_test.go.
// It lives in a _test.go file so that it can import tools/fetch and
// tools/search without creating an import cycle: the production server.go
// no longer imports those packages directly — each registrar carries its
// own audit-wrapping.

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/auth"
	"github.com/JetManiack/mcp-webtools/internal/recorder"
	"github.com/JetManiack/mcp-webtools/internal/tools/fetch"
	"github.com/JetManiack/mcp-webtools/internal/tools/search"
)

// Deps is everything the legacy MCP surface needs. Kept for tests; production
// code uses Handler with ToolRegistrar instead.
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
	rec := recorder.Recorder{DB: deps.DB, PreviewBytes: deps.PreviewBytes}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch",
		Description: "Retrieve the content of an http(s) URL. Bodies longer than the server's page size come back in pages: when truncated is true, call fetch again with the result's next_offset as offset to read the next page.",
	}, recorder.Recorded(rec, "fetch", fetch.Handler(deps.Fetcher)))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Description: "Search the web via a SearXNG instance",
	}, recorder.Recorded(rec, "search", search.Handler(deps.Searcher)))
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
