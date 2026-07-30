package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JetManiack/go-ai-webtools/internal/tools/fetch"
)

type FetchInput struct {
	URL string `json:"url" jsonschema:"the http(s) URL to retrieve"`
}

type FetchOutput struct {
	URL         string `json:"url" jsonschema:"the URL actually fetched, with any userinfo redacted"`
	StatusCode  int    `json:"status_code" jsonschema:"HTTP status code of the response"`
	ContentType string `json:"content_type,omitempty" jsonschema:"Content-Type header of the response"`
	Content     string `json:"content" jsonschema:"the response body"`
	Bytes       int64  `json:"bytes" jsonschema:"size of the returned content in bytes"`
	Truncated   bool   `json:"truncated" jsonschema:"true when the body was longer than the server's per-fetch limit and was cut"`
}

func fetchHandler(fetcher *fetch.Fetcher) mcp.ToolHandlerFor[FetchInput, FetchOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in FetchInput) (*mcp.CallToolResult, FetchOutput, error) {
		result, err := fetcher.Fetch(ctx, in.URL)
		if err != nil {
			return nil, FetchOutput{}, err
		}
		return nil, FetchOutput{
			URL:         result.URL,
			StatusCode:  result.StatusCode,
			ContentType: result.ContentType,
			Content:     result.Content,
			Bytes:       result.Bytes,
			Truncated:   result.Truncated,
		}, nil
	}
}
