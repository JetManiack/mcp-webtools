package storage

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrToolCallNotFound = errors.New("tool call not found")
	ErrUnknownCursor    = errors.New("unknown pagination cursor")
)

const (
	DefaultHistoryPageSize = 50
	MaxHistoryPageSize     = 200
)

// HistoryFilter selects and pages a slice of tool-call history. Cursor is
// the ID of the last row of the previous page (see ListToolCalls).
type HistoryFilter struct {
	ActorID string
	Tool    string
	IsError *bool
	Limit   int
	Cursor  string
}

// RecordToolCall persists one tool invocation, filling in ID and CalledAt
// when the caller left them unset.
func RecordToolCall(db *gorm.DB, call *ToolCall) error {
	if call.ID == "" {
		call.ID = uuid.NewString()
	}
	if call.CalledAt.IsZero() {
		call.CalledAt = time.Now()
	}
	return db.Create(call).Error
}

// ListToolCalls returns a page of history newest-first, plus the cursor for
// the next page ("" when this was the last one).
//
// Paging is keyset, not OFFSET: history is append-heavy and read newest-
// first, so an OFFSET page would silently shift as new calls arrive
// mid-pagination. The cursor is the previous page's last row ID, and that
// row's CreatedAt is re-read from the database rather than encoded into the
// cursor — SQLite and Postgres round-trip timestamps at different
// precisions, so a timestamp that made the round trip through a cursor
// string could no longer compare equal to the stored one.
func ListToolCalls(db *gorm.DB, f HistoryFilter) ([]ToolCall, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxHistoryPageSize {
		limit = DefaultHistoryPageSize
	}

	q := db.Model(&ToolCall{})
	if f.ActorID != "" {
		q = q.Where("actor_id = ?", f.ActorID)
	}
	if f.Tool != "" {
		q = q.Where("tool = ?", f.Tool)
	}
	if f.IsError != nil {
		q = q.Where("is_error = ?", *f.IsError)
	}

	if f.Cursor != "" {
		var after ToolCall
		err := db.First(&after, "id = ?", f.Cursor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrUnknownCursor
		}
		if err != nil {
			return nil, "", err
		}
		q = q.Where("(called_at < ?) OR (called_at = ? AND id < ?)", after.CalledAt, after.CalledAt, after.ID)
	}

	var calls []ToolCall
	if err := q.Order("called_at DESC, id DESC").Limit(limit).Find(&calls).Error; err != nil {
		return nil, "", err
	}

	next := ""
	if len(calls) == limit {
		next = calls[len(calls)-1].ID
	}
	return calls, next, nil
}

// GetToolCall returns one recorded call by ID.
func GetToolCall(db *gorm.DB, id string) (*ToolCall, error) {
	var call ToolCall
	err := db.First(&call, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrToolCallNotFound
	}
	if err != nil {
		return nil, err
	}
	return &call, nil
}

// DistinctTools returns the tool names that actually appear in history,
// so the UI's filter offers what's there rather than a hardcoded list.
func DistinctTools(db *gorm.DB) ([]string, error) {
	var tools []string
	if err := db.Model(&ToolCall{}).Distinct("tool").Order("tool").Pluck("tool", &tools).Error; err != nil {
		return nil, err
	}
	return tools, nil
}

// PruneToolCalls deletes history older than retention and reports how many
// rows went. A non-positive retention deletes nothing: that's the default,
// and "prune everything" must never be what an unset flag means.
func PruneToolCalls(db *gorm.DB, retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	result := db.Where("called_at < ?", time.Now().Add(-retention)).Delete(&ToolCall{})
	return result.RowsAffected, result.Error
}
