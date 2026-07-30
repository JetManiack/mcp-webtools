package storage

import "time"

// ActorKind distinguishes an AI agent from a human user. Both share the
// Actor table so every other table (ToolCall, and anything added later)
// needs only a single foreign key, regardless of which kind of actor it
// points to.
type ActorKind string

const (
	ActorKindAgent ActorKind = "agent"
	ActorKindHuman ActorKind = "human"
)

type Actor struct {
	ID          string    `gorm:"type:char(36);primaryKey" json:"id"`
	DisplayName string    `gorm:"not null;uniqueIndex" json:"display_name"`
	Kind        ActorKind `gorm:"type:varchar(10);not null;index" json:"kind"`
	CreatedAt   time.Time `json:"created_at"`
}

type AgentCredential struct {
	ID         string     `gorm:"type:char(36);primaryKey" json:"id"`
	ActorID    string     `gorm:"type:char(36);not null;index" json:"actor_id"`
	TokenHash  string     `gorm:"type:char(64);not null;uniqueIndex" json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// UserIdentity links a human Actor to the OIDC subject that authenticated
// it, and carries the role that subject's group membership maps to.
type UserIdentity struct {
	ActorID         string `gorm:"type:char(36);primaryKey"`
	KeycloakSubject string `gorm:"not null;uniqueIndex"`
	Role            string `gorm:"type:varchar(10);not null"`
}

// Session is a server-side OIDC login session, keyed by an opaque ID
// stored in the browser's session cookie. ExpiresAt is NOT the session's
// final expiry — it's a short checkpoint after which
// humanauth.OIDCProvider re-validates the session against Keycloak using
// RefreshToken (re-reading claims, so a role/group change propagates);
// the session's true maximum lifetime is bounded by how long Keycloak's
// own refresh token stays valid, which isn't tracked separately here.
type Session struct {
	ID           string `gorm:"type:char(64);primaryKey"` // a SHA-256 hex digest (humanauth.hashSessionID), not a UUID — 64 chars, not 36
	Subject      string `gorm:"not null;index"`
	DisplayName  string `gorm:"not null"`
	Role         string `gorm:"type:varchar(10);not null"`
	RefreshToken string `gorm:"not null"`
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// ToolCallStatus is whether a recorded tool call succeeded or returned an
// error to the agent.
type ToolCallStatus string

const (
	ToolCallStatusOK    ToolCallStatus = "ok"
	ToolCallStatusError ToolCallStatus = "error"
)

// ToolCall is one recorded MCP tool invocation.
//
// Args holds the full JSON-encoded input — inputs here are small (a URL or
// a query string), so there's nothing to gain from truncating them.
// ResponsePreview holds at most --history-preview-bytes of the JSON-encoded
// output, with Truncated set and ResponseBytes carrying the true size:
// `fetch` can return megabytes of HTML per call, so storing responses whole
// would let this one table outgrow everything else in the database.
type ToolCall struct {
	ID      string `gorm:"type:char(36);primaryKey" json:"id"`
	ActorID string `gorm:"type:char(36);not null;index:idx_tool_calls_actor_created,priority:1" json:"actor_id"`
	Tool    string `gorm:"type:varchar(64);not null;index:idx_tool_calls_tool_created,priority:1" json:"tool"`
	Args    string `gorm:"type:text;not null" json:"args"`

	Status       ToolCallStatus `gorm:"type:varchar(10);not null;index" json:"status"`
	ErrorMessage string         `gorm:"type:text" json:"error_message,omitempty"`

	DurationMS      int64  `json:"duration_ms"`
	ResponseBytes   int64  `json:"response_bytes"`
	ResponsePreview string `gorm:"type:text" json:"response_preview,omitempty"`
	Truncated       bool   `json:"truncated"`

	// Indexed both on its own (the unfiltered newest-first listing) and as
	// the second column of each composite index, so filtering by actor or
	// tool still reads the rows in the order the UI wants them.
	CreatedAt time.Time `gorm:"index;index:idx_tool_calls_actor_created,priority:2;index:idx_tool_calls_tool_created,priority:2" json:"created_at"`
}
