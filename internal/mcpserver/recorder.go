package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JetManiack/mcp-webtools/internal/recorder"
)

// Re-export the Recorder type and DefaultPreviewBytes constant so existing
// callers and tests that import mcpserver keep working. The implementation
// lives in internal/recorder to avoid the mcpserver ↔ tools/{fetch,search}
// import cycle.

// DefaultPreviewBytes bounds how much of a tool's output is kept in history
// when the caller doesn't configure a limit.
const DefaultPreviewBytes = recorder.DefaultPreviewBytes

// Recorder writes a storage.ToolCall row per MCP tool invocation. A zero
// Recorder (nil DB) records nothing.
type Recorder = recorder.Recorder

// recorded is a package-level wrapper so tests in package mcpserver can call
// it without importing the recorder package directly.
func recorded[In, Out any](rec Recorder, tool string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return recorder.Recorded(rec, tool, h)
}

// truncateUTF8 and marshalForHistory are package-level wrappers so that
// recorder_test.go (package mcpserver) can test the helpers directly.
func truncateUTF8(s string, limit int) (string, bool) { return recorder.TruncateUTF8(s, limit) }
func marshalForHistory(v any) string                  { return recorder.MarshalForHistory(v) }
