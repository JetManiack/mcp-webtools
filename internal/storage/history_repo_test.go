package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
)

func mustAgent(t *testing.T, db *gorm.DB, name string) *Actor {
	t.Helper()
	agent, err := CreateAgent(db, name)
	if err != nil {
		t.Fatalf("CreateAgent(%q): %v", name, err)
	}
	return agent
}

// recordCall inserts one history row with an explicit timestamp, so ordering
// and paging can be asserted without depending on how fast the test runs.
func recordCall(t *testing.T, db *gorm.DB, actorID, tool string, status ToolCallStatus, at time.Time) *ToolCall {
	t.Helper()
	call := &ToolCall{
		ActorID:   actorID,
		Tool:      tool,
		Args:      fmt.Sprintf(`{"url":"https://example.com/%d"}`, at.UnixNano()),
		Status:    status,
		CreatedAt: at,
	}
	if status == ToolCallStatusError {
		call.ErrorMessage = "boom"
	} else {
		call.ResponsePreview = `{"content":"hi"}`
		call.ResponseBytes = 16
	}
	if err := RecordToolCall(db, call); err != nil {
		t.Fatalf("RecordToolCall: %v", err)
	}
	return call
}

func TestRecordToolCallFillsIDAndTimestamp(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")

	call := &ToolCall{ActorID: agent.ID, Tool: "fetch", Args: "{}", Status: ToolCallStatusOK}
	if err := RecordToolCall(db, call); err != nil {
		t.Fatalf("RecordToolCall: %v", err)
	}
	if call.ID == "" {
		t.Error("ID was not filled in")
	}
	if call.CreatedAt.IsZero() {
		t.Error("CreatedAt was not filled in")
	}
}

func TestListToolCallsNewestFirst(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	oldest := recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, base)
	middle := recordCall(t, db, agent.ID, "search", ToolCallStatusOK, base.Add(time.Minute))
	newest := recordCall(t, db, agent.ID, "fetch", ToolCallStatusError, base.Add(2*time.Minute))

	calls, next, err := ListToolCalls(db, HistoryFilter{})
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	if next != "" {
		t.Errorf("next cursor = %q, want empty (all rows fit on one page)", next)
	}
	want := []string{newest.ID, middle.ID, oldest.ID}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d", len(calls), len(want))
	}
	for i, id := range want {
		if calls[i].ID != id {
			t.Errorf("calls[%d] = %q, want %q", i, calls[i].ID, id)
		}
	}
}

func TestListToolCallsFilters(t *testing.T) {
	db := openTestDB(t)
	one := mustAgent(t, db, "scraper-1")
	two := mustAgent(t, db, "scraper-2")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	recordCall(t, db, one.ID, "fetch", ToolCallStatusOK, base)
	recordCall(t, db, one.ID, "search", ToolCallStatusError, base.Add(time.Minute))
	recordCall(t, db, two.ID, "fetch", ToolCallStatusError, base.Add(2*time.Minute))

	tests := []struct {
		name   string
		filter HistoryFilter
		want   int
	}{
		{name: "no filter", filter: HistoryFilter{}, want: 3},
		{name: "by actor", filter: HistoryFilter{ActorID: one.ID}, want: 2},
		{name: "by tool", filter: HistoryFilter{Tool: "fetch"}, want: 2},
		{name: "by status", filter: HistoryFilter{Status: string(ToolCallStatusError)}, want: 2},
		{name: "combined", filter: HistoryFilter{Tool: "fetch", Status: string(ToolCallStatusError)}, want: 1},
		{name: "actor and tool", filter: HistoryFilter{ActorID: one.ID, Tool: "search"}, want: 1},
		{name: "no matches", filter: HistoryFilter{Tool: "nonexistent"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, _, err := ListToolCalls(db, tt.filter)
			if err != nil {
				t.Fatalf("ListToolCalls: %v", err)
			}
			if len(calls) != tt.want {
				t.Errorf("got %d calls, want %d", len(calls), tt.want)
			}
		})
	}
}

// Keyset paging has to walk every row exactly once, with no gaps and no
// repeats — the failure mode of OFFSET paging on an append-heavy table.
func TestListToolCallsPagesWithoutGapsOrRepeats(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	const total = 25
	for i := range total {
		recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, base.Add(time.Duration(i)*time.Second))
	}

	seen := make(map[string]int)
	cursor := ""
	pages := 0
	for {
		calls, next, err := ListToolCalls(db, HistoryFilter{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListToolCalls: %v", err)
		}
		for _, call := range calls {
			seen[call.ID]++
		}
		pages++
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Errorf("saw %d distinct rows across %d pages, want %d", len(seen), pages, total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("row %s was returned %d times", id, count)
		}
	}
}

// Rows sharing a timestamp still have to page correctly: the cursor breaks the
// tie on ID, and without that a whole batch of same-instant rows either
// repeats forever or gets skipped.
func TestListToolCallsPagesRowsSharingATimestamp(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	sameInstant := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	const total = 6
	for range total {
		recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, sameInstant)
	}

	seen := make(map[string]bool)
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 6 {
			t.Fatal("paging did not terminate")
		}
		calls, next, err := ListToolCalls(db, HistoryFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListToolCalls: %v", err)
		}
		for _, call := range calls {
			if seen[call.ID] {
				t.Fatalf("row %s was returned twice", call.ID)
			}
			seen[call.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Errorf("saw %d rows, want %d", len(seen), total)
	}
}

func TestListToolCallsLimitBounds(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	for i := range 5 {
		recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, base.Add(time.Duration(i)*time.Second))
	}

	for _, limit := range []int{0, -1, MaxHistoryPageSize + 1} {
		calls, _, err := ListToolCalls(db, HistoryFilter{Limit: limit})
		if err != nil {
			t.Fatalf("limit %d: ListToolCalls: %v", limit, err)
		}
		// All five rows fit under the default page size, so an out-of-range
		// limit must fall back to it rather than returning nothing.
		if len(calls) != 5 {
			t.Errorf("limit %d: got %d calls, want 5", limit, len(calls))
		}
	}
}

func TestListToolCallsUnknownCursor(t *testing.T) {
	db := openTestDB(t)

	_, _, err := ListToolCalls(db, HistoryFilter{Cursor: "not-a-real-id"})
	if !errors.Is(err, ErrUnknownCursor) {
		t.Errorf("error = %v, want ErrUnknownCursor", err)
	}
}

func TestGetToolCall(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	call := recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, time.Now().UTC())

	got, err := GetToolCall(db, call.ID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if got.ID != call.ID || got.Tool != "fetch" {
		t.Errorf("got %+v, want the recorded call", got)
	}

	if _, err := GetToolCall(db, "missing"); !errors.Is(err, ErrToolCallNotFound) {
		t.Errorf("error = %v, want ErrToolCallNotFound", err)
	}
}

func TestDistinctTools(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC()

	recordCall(t, db, agent.ID, "search", ToolCallStatusOK, base)
	recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, base.Add(time.Second))
	recordCall(t, db, agent.ID, "fetch", ToolCallStatusError, base.Add(2*time.Second))

	tools, err := DistinctTools(db)
	if err != nil {
		t.Fatalf("DistinctTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %v, want 2 distinct tools", tools)
	}
	if tools[0] != "fetch" || tools[1] != "search" {
		t.Errorf("got %v, want [fetch search] in name order", tools)
	}
}

func TestPruneToolCalls(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	now := time.Now().UTC()

	old := recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, now.Add(-48*time.Hour))
	recent := recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, now.Add(-time.Minute))

	deleted, err := PruneToolCalls(db, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneToolCalls: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}
	if _, err := GetToolCall(db, old.ID); !errors.Is(err, ErrToolCallNotFound) {
		t.Errorf("the expired row survived: %v", err)
	}
	if _, err := GetToolCall(db, recent.ID); err != nil {
		t.Errorf("a row inside the retention window was deleted: %v", err)
	}
}

// A zero or negative retention is the default "keep everything" setting. It
// must never be reinterpreted as "everything is older than zero, delete it
// all" — that mistake is unrecoverable.
func TestPruneToolCallsWithoutRetentionDeletesNothing(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	recordCall(t, db, agent.ID, "fetch", ToolCallStatusOK, time.Now().Add(-10*365*24*time.Hour).UTC())

	for _, retention := range []time.Duration{0, -time.Hour} {
		deleted, err := PruneToolCalls(db, retention)
		if err != nil {
			t.Fatalf("retention %v: PruneToolCalls: %v", retention, err)
		}
		if deleted != 0 {
			t.Errorf("retention %v: deleted %d rows, want 0", retention, deleted)
		}
	}

	var count int64
	if err := db.Model(&ToolCall{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d rows remain, want 1", count)
	}
}

// Postgres rejects both invalid UTF-8 and raw NUL bytes in a text column. The
// recorder JSON-encodes payloads before they get here, which neutralizes both
// — this asserts the column really does accept what that produces.
func TestRecordToolCallStoresEscapedBinaryPayload(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")

	call := &ToolCall{
		ActorID: agent.ID,
		Tool:    "fetch",
		Args:    `{"url":"https://example.com/binary"}`,
		Status:  ToolCallStatusOK,
		// A NUL escaped the way encoding/json escapes it (six ASCII characters,
		// not a raw byte), plus U+FFFD standing in for an invalid byte.
		ResponsePreview: `{"content":"\u0000 � binary-ish"}`,
		ResponseBytes:   42,
	}
	if err := RecordToolCall(db, call); err != nil {
		t.Fatalf("RecordToolCall: %v", err)
	}

	got, err := GetToolCall(db, call.ID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if got.ResponsePreview != call.ResponsePreview {
		t.Errorf("ResponsePreview = %q, want %q", got.ResponsePreview, call.ResponsePreview)
	}
}
