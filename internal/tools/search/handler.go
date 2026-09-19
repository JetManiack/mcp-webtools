package search

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JetManiack/mcp-webtools/internal/auth"
)

// SearchInput is the MCP tool input schema.
type SearchInput struct {
	Query string `json:"query" jsonschema:"the search query"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results to return (default 10, max 50)"`
}

// SearchOutput is the MCP tool output schema.
type SearchOutput struct {
	Results []Result `json:"results" jsonschema:"the search hits, most relevant first"`
}

// Handler returns a tool handler for the search tool.
func Handler(searcher *Searcher) mcp.ToolHandlerFor[SearchInput, SearchOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		cacheKey := ""
		if actor, ok := auth.ActorFromContext(ctx); ok {
			cacheKey = actor.ID
		}
		results, err := searcher.Search(ctx, in.Query, in.Limit, cacheKey)
		if err != nil {
			return nil, SearchOutput{}, err
		}
		return nil, SearchOutput{Results: results}, nil
	}
}
