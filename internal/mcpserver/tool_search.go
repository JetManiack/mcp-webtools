package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JetManiack/go-ai-webtools/internal/tools/search"
)

type SearchInput struct {
	Query string `json:"query" jsonschema:"the search query"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results to return (default 10, max 50)"`
}

type SearchOutput struct {
	Results []search.Result `json:"results" jsonschema:"the search hits, most relevant first"`
}

func searchHandler(searcher *search.Searcher) mcp.ToolHandlerFor[SearchInput, SearchOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		results, err := searcher.Search(ctx, in.Query, in.Limit)
		if err != nil {
			return nil, SearchOutput{}, err
		}
		return nil, SearchOutput{Results: results}, nil
	}
}
