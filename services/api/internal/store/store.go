// Package store defines the persistence interface for the Core API. Every
// implementation must pass the contract suite in package storetest.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// DefaultOrganizationID is the operator organization seeded by migration 0001.
const DefaultOrganizationID = "00000000-0000-4000-8000-000000000001"

type UserStatus string

const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

type User struct {
	ID                string
	OrganizationID    string
	Email             string
	DisplayName       string
	PasswordHash      string
	Status            UserStatus
	TOTPSecretEnc     []byte
	TOTPEnabled       bool
	TOTPLastStep      int64
	PasswordChangedAt time.Time
	LastLoginAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// UserUpdate changes only the non-nil fields. A non-nil TOTPSecretEnc pointing
// at an empty slice clears the stored secret.
type UserUpdate struct {
	DisplayName   *string
	Status        *UserStatus
	PasswordHash  *string
	TOTPSecretEnc *[]byte
	TOTPEnabled   *bool
	TOTPLastStep  *int64
	LastLoginAt   *time.Time
}

type UserFilter struct {
	OrganizationID string
	Query          string // case-insensitive substring of email or display name
	Limit, Offset  int
}

type Role struct {
	ID             string
	OrganizationID *string // nil for built-in roles
	Key            string
	Name           string
	Description    string
	BuiltIn        bool
	Permissions    []string // sorted
}

type Session struct {
	ID                string
	UserID            string
	TokenHash         []byte
	CreatedAt         time.Time
	ExpiresAt         time.Time
	LastSeenAt        time.Time
	ReauthenticatedAt *time.Time
	RevokedAt         *time.Time
	IP                string
	UserAgent         string
}

type AuditOutcome string

const (
	OutcomeSuccess AuditOutcome = "success"
	OutcomeFailure AuditOutcome = "failure"
	OutcomeDenied  AuditOutcome = "denied"
)

type AuditEntry struct {
	ID             int64
	OrganizationID *string
	ActorUserID    *string
	ActorLabel     string
	Action         string
	TargetType     string
	TargetID       string
	Outcome        AuditOutcome
	IP             string
	UserAgent      string
	RequestID      string
	Metadata       map[string]any
	CreatedAt      time.Time
}

type AuditFilter struct {
	OrganizationID string
	ActorUserID    string
	ActionPrefix   string
	Outcome        AuditOutcome
	BeforeID       int64 // keyset pagination: entries with ID < BeforeID
	Limit          int
}

type Store interface {
	Ping(ctx context.Context) error
	Close()

	// RBAC catalog
	SyncPermissions(ctx context.Context, catalog map[string]string) error
	UpsertBuiltinRole(ctx context.Context, key, name, description string, permissions []string) error
	ListRoles(ctx context.Context, organizationID string) ([]Role, error)
	GetRolesByKeys(ctx context.Context, organizationID string, keys []string) ([]Role, error)

	// Users
	CreateUser(ctx context.Context, u *User, roleIDs []string, grantedBy *string) error
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	ListUsers(ctx context.Context, f UserFilter) ([]User, int, error)
	UpdateUser(ctx context.Context, id string, upd UserUpdate) error
	SetUserRoles(ctx context.Context, userID string, roleIDs []string, grantedBy *string) error
	GetUserRoles(ctx context.Context, userID string) ([]Role, error)
	CountActiveUsersWithRole(ctx context.Context, organizationID, roleKey string) (int, error)

	// Sessions
	CreateSession(ctx context.Context, s *Session) error
	GetSessionByTokenHash(ctx context.Context, hash []byte) (*Session, error)
	TouchSession(ctx context.Context, id string, at time.Time) error
	MarkSessionReauthenticated(ctx context.Context, id string, at time.Time) error
	RevokeSession(ctx context.Context, id string, at time.Time) error
	RevokeUserSessions(ctx context.Context, userID, exceptSessionID string, at time.Time) (int, error)
	ListActiveSessions(ctx context.Context, userID string, now time.Time) ([]Session, error)

	// Audit
	InsertAudit(ctx context.Context, e *AuditEntry) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error)

	// Settings
	GetSetting(ctx context.Context, key string) (json.RawMessage, error)
	PutSetting(ctx context.Context, key string, value json.RawMessage, updatedBy *string) error
}
