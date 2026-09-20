package fetch

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/recorder"
)

// Registrar registers the fetch tool onto an MCP server.
type Registrar struct{ fetcher *Fetcher }

// NewRegistrar returns a Registrar wrapping fetcher.
func NewRegistrar(f *Fetcher) *Registrar { return &Registrar{fetcher: f} }

// Register adds the fetch tool to srv, wrapped with audit recording.
func (r *Registrar) Register(srv *mcp.Server, db *gorm.DB) {
	rec := recorder.Recorder{DB: db}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "fetch",
		Description: "Retrieve the content of an http(s) URL. Bodies longer than the server's page size come back in pages: when truncated is true, call fetch again with the result's next_offset as offset to read the next page.",
	}, recorder.Recorded(rec, "fetch", Handler(r.fetcher)))
}
