package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/JetManiack/go-ai-webtools/internal/storage"
)

// DefaultPreviewBytes bounds how much of a tool's output is kept in history
// when the caller doesn't configure a limit.
const DefaultPreviewBytes = 64 << 10 // 64 KiB

// Recorder writes a storage.ToolCall row per MCP tool invocation.
//
// A zero Recorder (nil DB) records nothing and is the intended way to run
// the MCP server without history, e.g. in tests that only care about tool
// behavior.
type Recorder struct {
	DB           *gorm.DB
	PreviewBytes int
}

func (rec Recorder) previewLimit() int {
	if rec.PreviewBytes <= 0 {
		return DefaultPreviewBytes
	}
	return rec.PreviewBytes
}

// recorded wraps a tool handler so every call — successful or not — is
// persisted to history, then returns the handler's own result untouched.
//
// Recording deliberately cannot fail the call: a database problem is
// reported through slog and the agent still gets its answer. Observability
// that breaks the thing it observes is worse than no observability.
func recorded[In, Out any](rec Recorder, tool string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		result, out, err := h(ctx, req, in)
		rec.record(ctx, tool, in, out, err, time.Since(start))
		return result, out, err
	}
}

// record persists one call. It reads the actor from ctx but intentionally
// does NOT pass ctx to the database: by the time a slow tool returns, the
// client may already have disconnected and cancelled it, and a cancelled
// context would drop exactly the calls most worth having in history.
func (rec Recorder) record(ctx context.Context, tool string, in, out any, callErr error, elapsed time.Duration) {
	if rec.DB == nil {
		return
	}

	actor, ok := ActorFromContext(ctx)
	if !ok {
		// Unreachable through /mcp, which is wrapped in RequireAgentToken —
		// so this means a new, unauthenticated entry point was added and
		// history silently stopped covering it. Say so rather than writing a
		// row with no owner.
		slog.Warn("tool call not recorded: no authenticated actor in context", "tool", tool)
		return
	}

	call := &storage.ToolCall{
		ActorID:    actor.ID,
		Tool:       tool,
		Args:       marshalForHistory(in),
		Status:     storage.ToolCallStatusOK,
		DurationMS: elapsed.Milliseconds(),
	}

	if callErr != nil {
		call.Status = storage.ToolCallStatusError
		call.ErrorMessage = callErr.Error()
	} else {
		encoded := marshalForHistory(out)
		call.ResponseBytes = int64(len(encoded))
		call.ResponsePreview, call.Truncated = truncateUTF8(encoded, rec.previewLimit())
	}

	if err := storage.RecordToolCall(rec.DB, call); err != nil {
		slog.Error("failed to record tool call", "tool", tool, "actor_id", actor.ID, "error", err)
	}
}

// marshalForHistory renders v as JSON for storage. encoding/json replaces
// invalid UTF-8 with U+FFFD and escapes a NUL byte as a six-character
// backslash-u escape, so the result is always storable in a Postgres text
// column — which rejects both raw NULs and invalid byte sequences, and
// would otherwise fail on the first agent that fetched a binary file.
func marshalForHistory(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("{%q:%q}", "history_encoding_error", err.Error())
	}
	return string(encoded)
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune, and
// reports whether anything was dropped. Cutting mid-rune would leave an
// invalid UTF-8 tail, which Postgres rejects outright.
func truncateUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := s[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut, true
}
