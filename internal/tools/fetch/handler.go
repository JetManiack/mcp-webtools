package fetch

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JetManiack/mcp-webtools/internal/auth"
)

// FetchInput is the MCP tool input schema.
type FetchInput struct {
	URL    string `json:"url" jsonschema:"the http(s) URL to retrieve"`
	Offset int64  `json:"offset,omitempty" jsonschema:"byte offset to start reading from; to read the continuation of a truncated body, pass the next_offset from its result"`
}

// FetchOutput is the MCP tool output schema.
type FetchOutput struct {
	URL         string `json:"url" jsonschema:"the URL actually fetched, with any userinfo redacted"`
	StatusCode  int    `json:"status_code" jsonschema:"HTTP status code of the response"`
	ContentType string `json:"content_type,omitempty" jsonschema:"Content-Type header of the response"`
	Offset      int64  `json:"offset" jsonschema:"byte offset within the document where the content below begins"`
	Content     string `json:"content" jsonschema:"the response body, or the page of it starting at offset"`
	Bytes       int64  `json:"bytes" jsonschema:"size of the content above in bytes"`
	Truncated   bool   `json:"truncated" jsonschema:"true when the document continues beyond the content above"`
	NextOffset  int64  `json:"next_offset" jsonschema:"byte offset of the continuation; call fetch again with this as offset to read the next page. Zero when the whole document was returned"`
}

// Handler returns a tool handler for the fetch tool.
func Handler(fetcher *Fetcher) mcp.ToolHandlerFor[FetchInput, FetchOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in FetchInput) (*mcp.CallToolResult, FetchOutput, error) {
		cacheKey := ""
		if actor, ok := auth.ActorFromContext(ctx); ok {
			cacheKey = actor.ID
		}
		result, err := fetcher.Fetch(ctx, in.URL, in.Offset, cacheKey)
		if err != nil {
			return nil, FetchOutput{}, err
		}
		return nil, FetchOutput{
			URL:         result.URL,
			StatusCode:  result.StatusCode,
			ContentType: result.ContentType,
			Offset:      result.Offset,
			Content:     result.Content,
			Bytes:       result.Bytes,
			Truncated:   result.Truncated,
			NextOffset:  result.NextOffset,
		}, nil
	}
}
