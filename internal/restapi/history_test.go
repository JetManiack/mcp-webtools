package restapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/JetManiack/go-ai-webtools/internal/storage"
)

func TestListHistoryEmpty(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("viewer"))

	resp := do(t, server, http.MethodGet, "/api/history", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body historyListResponse
	decode(t, resp, &body)
	// An empty list must serialize as [] — a nil slice becomes JSON null, which
	// every client then has to special-case.
	if body.Calls == nil {
		t.Error("calls = null, want []")
	}
	if len(body.Calls) != 0 {
		t.Errorf("got %d calls, want 0", len(body.Calls))
	}
	if body.NextCursor != "" {
		t.Errorf("next_cursor = %q, want empty", body.NextCursor)
	}
}

func TestListHistoryReturnsCallsWithActorNames(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	recordCall(t, db, agent.ID, "fetch", storage.ToolCallStatusOK, base)
	recordCall(t, db, agent.ID, "search", storage.ToolCallStatusError, base.Add(time.Minute))

	server := newTestAPI(t, db, providerWithRole("viewer"))
	resp := do(t, server, http.MethodGet, "/api/history", "")

	var body historyListResponse
	decode(t, resp, &body)
	if len(body.Calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(body.Calls))
	}
	if body.Calls[0].Tool != "search" {
		t.Errorf("first call = %q, want search (newest first)", body.Calls[0].Tool)
	}
	// The actors map is what lets the UI label rows without one lookup request
	// per distinct agent.
	if body.Actors[agent.ID] != "scraper-1" {
		t.Errorf("actors[%s] = %q, want scraper-1", agent.ID, body.Actors[agent.ID])
	}
}

func TestListHistoryFilters(t *testing.T) {
	db := openTestDB(t)
	one := mustAgent(t, db, "scraper-1")
	two := mustAgent(t, db, "scraper-2")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	recordCall(t, db, one.ID, "fetch", storage.ToolCallStatusOK, base)
	recordCall(t, db, one.ID, "search", storage.ToolCallStatusError, base.Add(time.Minute))
	recordCall(t, db, two.ID, "fetch", storage.ToolCallStatusError, base.Add(2*time.Minute))

	server := newTestAPI(t, db, providerWithRole("viewer"))

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "by tool", query: "?tool=fetch", want: 2},
		{name: "by status", query: "?status=error", want: 2},
		{name: "by actor", query: "?actor=" + one.ID, want: 2},
		{name: "combined", query: "?tool=fetch&status=error", want: 1},
		{name: "limit", query: "?limit=1", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := do(t, server, http.MethodGet, "/api/history"+tt.query, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var body historyListResponse
			decode(t, resp, &body)
			if len(body.Calls) != tt.want {
				t.Errorf("got %d calls, want %d", len(body.Calls), tt.want)
			}
		})
	}
}

func TestListHistoryRejectsBadParameters(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("viewer"))

	tests := []struct {
		name  string
		query string
	}{
		{name: "non-numeric limit", query: "?limit=lots"},
		{name: "unknown status", query: "?status=maybe"},
		{name: "unknown cursor", query: "?cursor=not-a-real-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := do(t, server, http.MethodGet, "/api/history"+tt.query, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
			// A 4xx keeps its specific message; that's the difference between a
			// usable client error and a shrug.
			var body map[string]string
			decode(t, resp, &body)
			if body["error"] == "" {
				t.Error("response body carries no error message")
			}
		})
	}
}

func TestListHistoryPagination(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	for i := range 5 {
		recordCall(t, db, agent.ID, "fetch", storage.ToolCallStatusOK, base.Add(time.Duration(i)*time.Second))
	}

	server := newTestAPI(t, db, providerWithRole("viewer"))

	seen := map[string]bool{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatal("paging did not terminate")
		}
		path := "/api/history?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		resp := do(t, server, http.MethodGet, path, "")
		var body historyListResponse
		decode(t, resp, &body)
		for _, call := range body.Calls {
			if seen[call.ID] {
				t.Fatalf("call %s was returned on more than one page", call.ID)
			}
			seen[call.ID] = true
		}
		if body.NextCursor == "" {
			break
		}
		cursor = body.NextCursor
	}
	if len(seen) != 5 {
		t.Errorf("saw %d calls across all pages, want 5", len(seen))
	}
}

func TestGetHistoryEntry(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	call := recordCall(t, db, agent.ID, "fetch", storage.ToolCallStatusOK, time.Now().UTC())

	server := newTestAPI(t, db, providerWithRole("viewer"))

	resp := do(t, server, http.MethodGet, "/api/history/"+call.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body historyEntryResponse
	decode(t, resp, &body)
	if body.Call.ID != call.ID {
		t.Errorf("call ID = %q, want %q", body.Call.ID, call.ID)
	}
	if body.Actor != "scraper-1" {
		t.Errorf("actor = %q, want scraper-1", body.Actor)
	}
}

func TestGetHistoryEntryNotFound(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("viewer"))

	resp := do(t, server, http.MethodGet, "/api/history/does-not-exist", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestListHistoryTools(t *testing.T) {
	db := openTestDB(t)
	agent := mustAgent(t, db, "scraper-1")
	base := time.Now().Add(-time.Hour).UTC()
	recordCall(t, db, agent.ID, "search", storage.ToolCallStatusOK, base)
	recordCall(t, db, agent.ID, "fetch", storage.ToolCallStatusOK, base.Add(time.Second))
	recordCall(t, db, agent.ID, "fetch", storage.ToolCallStatusOK, base.Add(2*time.Second))

	server := newTestAPI(t, db, providerWithRole("viewer"))

	resp := do(t, server, http.MethodGet, "/api/history/tools", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var tools []string
	decode(t, resp, &tools)
	if len(tools) != 2 || tools[0] != "fetch" || tools[1] != "search" {
		t.Errorf("tools = %v, want [fetch search]", tools)
	}
}

func TestListHistoryToolsEmptyIsArray(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("viewer"))

	resp := do(t, server, http.MethodGet, "/api/history/tools", "")
	var tools []string
	decode(t, resp, &tools)
	if tools == nil {
		t.Error("tools = null, want []")
	}
}
