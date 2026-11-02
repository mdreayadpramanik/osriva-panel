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
	ServerStore

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

// ---- Servers and agents (Phase 2) ----

type ServerStatus string

const (
	ServerPending ServerStatus = "pending"
	ServerActive  ServerStatus = "active"
	ServerRevoked ServerStatus = "revoked"
)

// ServerFacts are the inventory fields denormalised onto the server row for listing.
type ServerFacts struct {
	Hostname, OSFamily, OSName, OSVersion, Kernel, Arch, AgentVersion string
}

type Server struct {
	ID                  string
	OrganizationID      string
	Name                string
	Status              ServerStatus
	EnrollmentTokenHash []byte
	EnrollmentExpiresAt *time.Time
	Facts               ServerFacts
	LastSeenAt          *time.Time
	LastSeenIP          string
	EnrolledAt          *time.Time
	CreatedBy           *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type AgentCertificate struct {
	Serial      string
	ServerID    string
	Fingerprint string
	NotAfter    time.Time
	IssuedAt    time.Time
	RevokedAt   *time.Time
}

type ServerInventory struct {
	ServerID     string
	CollectedAt  time.Time
	ReceivedAt   time.Time
	Data         json.RawMessage
	Capabilities json.RawMessage
}

// ServerStore persists managed servers, enrolment and agent credentials.
type ServerStore interface {
	GetAgentCA(ctx context.Context) (certPEM string, keyEnc []byte, err error)
	// InsertAgentCA returns ErrConflict when a CA already exists.
	InsertAgentCA(ctx context.Context, certPEM string, keyEnc []byte) error

	CreateServer(ctx context.Context, s *Server) error
	GetServer(ctx context.Context, id string) (*Server, error)
	ListServers(ctx context.Context, organizationID string) ([]Server, error)
	// SetEnrollmentToken replaces the token of a pending server (ErrConflict otherwise).
	SetEnrollmentToken(ctx context.Context, id string, hash []byte, expiresAt time.Time) error
	// FindPendingEnrollment returns the pending server whose unexpired token hashes to hash.
	FindPendingEnrollment(ctx context.Context, hash []byte, now time.Time) (*Server, error)
	// CompleteEnrollment atomically consumes the token, activates the server and
	// stores its first certificate. ErrNotFound if the token is no longer valid.
	CompleteEnrollment(ctx context.Context, serverID string, hash []byte, now time.Time, cert *AgentCertificate) error
	AddAgentCertificate(ctx context.Context, c *AgentCertificate) error
	GetAgentCertificate(ctx context.Context, serial string) (*AgentCertificate, error)
	// RevokeServer marks the server revoked, drops any token and revokes all certificates.
	RevokeServer(ctx context.Context, id string, now time.Time) error
	// RecordReport stores the latest facts, last-seen data and inventory snapshot.
	RecordReport(ctx context.Context, serverID string, facts ServerFacts, ip string, now time.Time, inv *ServerInventory) error
	GetServerInventory(ctx context.Context, serverID string) (*ServerInventory, error)
}
