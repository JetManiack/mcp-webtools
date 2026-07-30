package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvalidToken     = errors.New("invalid or revoked token")
	ErrEmptyDisplayName = errors.New("display name must not be empty")
)

// TokenPrefix marks a raw bearer token as belonging to this service, so a
// leaked token is recognizable in a log or a secret scanner.
const TokenPrefix = "wt_"

func CreateAgent(db *gorm.DB, displayName string) (*Actor, error) {
	if strings.TrimSpace(displayName) == "" {
		return nil, ErrEmptyDisplayName
	}

	actor := &Actor{
		ID:          uuid.NewString(),
		DisplayName: displayName,
		Kind:        ActorKindAgent,
	}
	if err := db.Create(actor).Error; err != nil {
		return nil, err
	}
	return actor, nil
}

// IssueAgentToken generates a new bearer token for actorID and persists
// only its hash. The raw token is returned once and never stored.
func IssueAgentToken(db *gorm.DB, actorID string) (rawToken string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	rawToken = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	cred := &AgentCredential{
		ID:        uuid.NewString(),
		ActorID:   actorID,
		TokenHash: hashToken(rawToken),
	}
	if err := db.Create(cred).Error; err != nil {
		return "", err
	}
	return rawToken, nil
}

func AuthenticateAgentToken(db *gorm.DB, rawToken string) (*Actor, error) {
	var cred AgentCredential
	err := db.Where("token_hash = ? AND revoked_at IS NULL", hashToken(rawToken)).First(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if err := db.Model(&cred).Update("last_used_at", now).Error; err != nil {
		slog.Warn("failed to update agent credential last_used_at", "credential_id", cred.ID, "error", err)
	}

	var actor Actor
	if err := db.First(&actor, "id = ?", cred.ActorID).Error; err != nil {
		return nil, err
	}
	return &actor, nil
}

// ListAgents returns every agent Actor, oldest first.
func ListAgents(db *gorm.DB) ([]Actor, error) {
	var agents []Actor
	if err := db.Where("kind = ?", ActorKindAgent).Order("created_at").Find(&agents).Error; err != nil {
		return nil, err
	}
	return agents, nil
}

// ListAgentCredentials returns actorID's credentials, newest first. Token
// hashes are never exposed (AgentCredential.TokenHash is json:"-"); this is
// what lets the UI show and revoke individual tokens by ID.
func ListAgentCredentials(db *gorm.DB, actorID string) ([]AgentCredential, error) {
	var creds []AgentCredential
	if err := db.Where("actor_id = ?", actorID).Order("created_at DESC").Find(&creds).Error; err != nil {
		return nil, err
	}
	return creds, nil
}

// ActorNamesByID resolves actor IDs to display names in one query. Callers
// get a map so a missing ID (an actor deleted out from under existing
// history) reads as an empty name rather than shifting every other result.
func ActorNamesByID(db *gorm.DB, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}

	var actors []Actor
	if err := db.Where("id IN ?", ids).Find(&actors).Error; err != nil {
		return nil, err
	}
	for _, actor := range actors {
		names[actor.ID] = actor.DisplayName
	}
	return names, nil
}

// ActorsWithActiveToken returns the set of actor IDs holding at least one
// non-revoked credential.
func ActorsWithActiveToken(db *gorm.DB) (map[string]bool, error) {
	var ids []string
	if err := db.Model(&AgentCredential{}).
		Where("revoked_at IS NULL").
		Distinct("actor_id").
		Pluck("actor_id", &ids).Error; err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(ids))
	for _, id := range ids {
		active[id] = true
	}
	return active, nil
}

// RevokeAgentToken revokes a single credential by its ID. Revoking a
// credential that's already revoked, or doesn't exist, is a no-op, not an
// error.
func RevokeAgentToken(db *gorm.DB, credentialID string) error {
	return db.Model(&AgentCredential{}).
		Where("id = ? AND revoked_at IS NULL", credentialID).
		Update("revoked_at", time.Now()).Error
}

// RevokeAllAgentCredentials revokes every non-revoked credential belonging
// to actorID — the bulk form of RevokeAgentToken, used when an agent is
// "deleted" via the REST API. That removes its ability to authenticate
// without deleting its Actor row, so the tool-call history it produced
// stays attributable instead of turning into orphaned rows.
func RevokeAllAgentCredentials(db *gorm.DB, actorID string) error {
	return db.Model(&AgentCredential{}).
		Where("actor_id = ? AND revoked_at IS NULL", actorID).
		Update("revoked_at", time.Now()).Error
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
