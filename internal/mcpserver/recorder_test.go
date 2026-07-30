package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JetManiack/go-ai-webtools/internal/storage"
)

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		limit         int
		want          string
		wantTruncated bool
	}{
		{name: "under the limit", input: "hello", limit: 10, want: "hello"},
		{name: "exactly the limit", input: "hello", limit: 5, want: "hello"},
		{name: "over the limit", input: "hello world", limit: 5, want: "hello", wantTruncated: true},
		{name: "zero limit", input: "hello", limit: 0, want: "", wantTruncated: true},
		{name: "empty input", input: "", limit: 5, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := truncateUTF8(tt.input, tt.limit)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTruncated)
			}
		})
	}
}

// Cutting at a byte offset inside a multi-byte rune leaves an invalid UTF-8
// tail, which Postgres rejects outright — so the whole history row would fail
// to insert on a response that merely happened to contain a non-ASCII
// character near the cut.
func TestTruncateUTF8NeverSplitsARune(t *testing.T) {
	// Four 3-byte runes: any limit from 1..11 lands mid-rune unless handled.
	input := strings.Repeat("日", 4)

	for limit := range len(input) + 1 {
		got, _ := truncateUTF8(input, limit)
		if !utf8.ValidString(got) {
			t.Errorf("limit %d produced invalid UTF-8: %q", limit, got)
		}
		if len(got) > limit {
			t.Errorf("limit %d produced %d bytes", limit, len(got))
		}
	}
}

// The recorder stores JSON, and encoding/json is what neutralizes bytes a text
// column can't hold: invalid UTF-8 becomes U+FFFD, a NUL becomes a
// backslash-u escape. If that ever stopped being true, recording a fetched
// binary file would start failing on Postgres.
func TestMarshalForHistoryProducesStorableText(t *testing.T) {
	payload := struct {
		Content string `json:"content"`
	}{Content: "ok\x00then\xff\xfebinary"}

	encoded := marshalForHistory(payload)

	if !utf8.ValidString(encoded) {
		t.Errorf("encoded payload is not valid UTF-8: %q", encoded)
	}
	if strings.ContainsRune(encoded, 0) {
		t.Errorf("encoded payload contains a raw NUL byte: %q", encoded)
	}
	var round map[string]string
	if err := json.Unmarshal([]byte(encoded), &round); err != nil {
		t.Errorf("encoded payload is not valid JSON: %v", err)
	}
}

func TestMarshalForHistoryUnmarshalableValue(t *testing.T) {
	// A channel can't be marshaled; the recorder must still produce something
	// storable rather than an empty column or a panic.
	encoded := marshalForHistory(struct {
		Ch chan int `json:"ch"`
	}{Ch: make(chan int)})

	if encoded == "" {
		t.Fatal("got an empty string, want a placeholder describing the failure")
	}
	if !strings.Contains(encoded, "history_encoding_error") {
		t.Errorf("got %q, want it to name the encoding failure", encoded)
	}
}

type recordedOut struct {
	Value string `json:"value"`
}

func callRecorded(t *testing.T, rec Recorder, ctx context.Context, out recordedOut, handlerErr error) error {
	t.Helper()
	handler := recorded(rec, "probe", func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, recordedOut, error) {
		return nil, out, handlerErr
	})
	_, _, err := handler(ctx, nil, struct{}{})
	return err
}

func TestRecordedWritesSuccess(t *testing.T) {
	db := openTestDB(t)
	actor := mustAgent(t, db, "scraper-1")
	ctx := withActor(context.Background(), actor)

	if err := callRecorded(t, Recorder{DB: db}, ctx, recordedOut{Value: "hi"}, nil); err != nil {
		t.Fatalf("handler returned %v", err)
	}

	calls, _, err := storage.ListToolCalls(db, storage.HistoryFilter{})
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d history rows, want 1", len(calls))
	}
	call := calls[0]
	if call.Tool != "probe" {
		t.Errorf("Tool = %q, want probe", call.Tool)
	}
	if call.ActorID != actor.ID {
		t.Errorf("ActorID = %q, want %q", call.ActorID, actor.ID)
	}
	if call.Status != storage.ToolCallStatusOK {
		t.Errorf("Status = %q, want ok", call.Status)
	}
	if !strings.Contains(call.ResponsePreview, "hi") {
		t.Errorf("ResponsePreview = %q, want it to contain the output", call.ResponsePreview)
	}
	if call.ResponseBytes == 0 {
		t.Error("ResponseBytes = 0, want the encoded output size")
	}
	if call.Truncated {
		t.Error("Truncated = true for a tiny payload")
	}
}

// A failing call is the one most worth having in history, so the error path
// must record too — and must record the message, not just the fact of failure.
func TestRecordedWritesFailure(t *testing.T) {
	db := openTestDB(t)
	actor := mustAgent(t, db, "scraper-1")
	ctx := withActor(context.Background(), actor)

	wantErr := errors.New("fetch: connection refused")
	if err := callRecorded(t, Recorder{DB: db}, ctx, recordedOut{}, wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v, want it passed through unchanged", err)
	}

	calls, _, err := storage.ListToolCalls(db, storage.HistoryFilter{})
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d history rows, want 1", len(calls))
	}
	if calls[0].Status != storage.ToolCallStatusError {
		t.Errorf("Status = %q, want error", calls[0].Status)
	}
	if calls[0].ErrorMessage != wantErr.Error() {
		t.Errorf("ErrorMessage = %q, want %q", calls[0].ErrorMessage, wantErr.Error())
	}
	if calls[0].ResponsePreview != "" {
		t.Errorf("ResponsePreview = %q, want empty for a failed call", calls[0].ResponsePreview)
	}
}

func TestRecordedTruncatesLargePreview(t *testing.T) {
	db := openTestDB(t)
	actor := mustAgent(t, db, "scraper-1")
	ctx := withActor(context.Background(), actor)

	big := strings.Repeat("x", 5000)
	if err := callRecorded(t, Recorder{DB: db, PreviewBytes: 100}, ctx, recordedOut{Value: big}, nil); err != nil {
		t.Fatalf("handler returned %v", err)
	}

	calls, _, err := storage.ListToolCalls(db, storage.HistoryFilter{})
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	call := calls[0]
	if len(call.ResponsePreview) != 100 {
		t.Errorf("len(ResponsePreview) = %d, want 100", len(call.ResponsePreview))
	}
	if !call.Truncated {
		t.Error("Truncated = false, want true")
	}
	// The row must still report the real size, or the UI would claim the agent
	// received 100 bytes when it received thousands.
	if call.ResponseBytes <= 100 {
		t.Errorf("ResponseBytes = %d, want the full encoded size", call.ResponseBytes)
	}
}

// Recording is observability; it must never be able to fail the call it
// observes. A nil DB stands in for "history unavailable".
func TestRecordedWithoutDatabaseStillReturnsResult(t *testing.T) {
	handler := recorded(Recorder{}, "probe", func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, recordedOut, error) {
		return nil, recordedOut{Value: "hi"}, nil
	})

	_, out, err := handler(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("handler returned %v", err)
	}
	if out.Value != "hi" {
		t.Errorf("out.Value = %q, want hi", out.Value)
	}
}

// Same requirement when the actor is missing from the context: skip the row,
// return the result.
func TestRecordedWithoutActorStillReturnsResult(t *testing.T) {
	db := openTestDB(t)

	if err := callRecorded(t, Recorder{DB: db}, context.Background(), recordedOut{Value: "hi"}, nil); err != nil {
		t.Fatalf("handler returned %v", err)
	}

	calls, _, err := storage.ListToolCalls(db, storage.HistoryFilter{})
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("got %d history rows, want 0 (a row with no owner is worse than no row)", len(calls))
	}
}

// A cancelled context is exactly what a slow-but-completed call looks like
// once the client hangs up — the row still has to be written, or the calls
// most worth investigating are the ones that never get recorded.
func TestRecordedWritesEvenWhenContextIsCancelled(t *testing.T) {
	db := openTestDB(t)
	actor := mustAgent(t, db, "scraper-1")

	ctx, cancel := context.WithCancel(withActor(context.Background(), actor))
	cancel()

	if err := callRecorded(t, Recorder{DB: db}, ctx, recordedOut{Value: "hi"}, nil); err != nil {
		t.Fatalf("handler returned %v", err)
	}

	calls, _, err := storage.ListToolCalls(db, storage.HistoryFilter{})
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d history rows, want 1", len(calls))
	}
}

func TestRecorderPreviewLimitDefault(t *testing.T) {
	for _, configured := range []int{0, -1} {
		if got := (Recorder{PreviewBytes: configured}).previewLimit(); got != DefaultPreviewBytes {
			t.Errorf("PreviewBytes %d: previewLimit() = %d, want %d", configured, got, DefaultPreviewBytes)
		}
	}
	if got := (Recorder{PreviewBytes: 42}).previewLimit(); got != 42 {
		t.Errorf("previewLimit() = %d, want 42", got)
	}
}
