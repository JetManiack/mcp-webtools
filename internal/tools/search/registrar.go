package search

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/recorder"
)

// Registrar registers the search tool onto an MCP server.
type Registrar struct{ searcher *Searcher }

// NewRegistrar returns a Registrar wrapping searcher.
func NewRegistrar(s *Searcher) *Registrar { return &Registrar{searcher: s} }

// Register adds the search tool to srv, wrapped with audit recording.
func (r *Registrar) Register(srv *mcp.Server, db *gorm.DB) {
	rec := recorder.Recorder{DB: db}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search",
		Description: "Search the web via a SearXNG instance",
	}, recorder.Recorded(rec, "search", Handler(r.searcher)))
}
