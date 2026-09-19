package search

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"
)

// Registrar registers the search tool onto an MCP server.
type Registrar struct{ searcher *Searcher }

// NewRegistrar returns a Registrar wrapping searcher.
func NewRegistrar(s *Searcher) *Registrar { return &Registrar{searcher: s} }

// Register adds the search tool to srv.
func (r *Registrar) Register(srv *mcp.Server, _ *gorm.DB) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search",
		Description: "Search the web via a SearXNG instance",
	}, Handler(r.searcher))
}
