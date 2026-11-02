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
	TaskStore
	AppStore
	ProxyStore

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

// ---- Agent tasks and metrics (Phase 3) ----

type TaskStatus string

const (
	TaskQueued    TaskStatus = "queued"
	TaskRunning   TaskStatus = "running"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
	TaskExpired   TaskStatus = "expired"
)

type Task struct {
	ID          string
	ServerID    string
	Type        string
	Params      json.RawMessage
	Status      TaskStatus
	RequestedBy *string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	// RunTimeout bounds how long the task may run once claimed (default 5 minutes).
	RunTimeout time.Duration
	StartedAt  *time.Time
	FinishedAt *time.Time
	Result     json.RawMessage
	Error      string
}

type MetricSample struct {
	ServerID string
	TS       time.Time
	Data     json.RawMessage
}

// TaskStore persists the agent task queue and metric samples.
type TaskStore interface {
	CreateTask(ctx context.Context, t *Task) error
	GetTask(ctx context.Context, id string) (*Task, error)
	ListTasks(ctx context.Context, serverID string, limit int) ([]Task, error)
	// ClaimTasks atomically moves up to limit queued, unexpired tasks of the server to running.
	ClaimTasks(ctx context.Context, serverID string, now time.Time, limit int) ([]Task, error)
	// CompleteTask records the outcome of a running task of serverID (ErrNotFound otherwise).
	CompleteTask(ctx context.Context, serverID, taskID string, status TaskStatus, result json.RawMessage, errMsg string, now time.Time) error
	// ExpireTasks expires queued tasks past their deadline and fails running tasks past their run timeout.
	ExpireTasks(ctx context.Context, now time.Time) (int, error)
	// RedactTaskParams replaces the stored parameters (used to drop secrets once a task has finished).
	RedactTaskParams(ctx context.Context, taskID string, params json.RawMessage) error

	InsertMetrics(ctx context.Context, samples []MetricSample) error
	ListMetrics(ctx context.Context, serverID string, since time.Time, limit int) ([]MetricSample, error)
	PruneMetrics(ctx context.Context, before time.Time) (int, error)
}

// ---- Applications and deployments (Phase 4b) ----

type App struct {
	ID            string
	ServerID      string
	Name          string
	GitURL        string
	GitRef        string
	BuildMode     string
	BuildContext  string
	Dockerfile    string
	ContainerPort int
	HostPort      int
	Public        bool
	EnvEnc        []byte // encrypted JSON object; nil when there are no variables
	CreatedBy     *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Deployment struct {
	ID         string
	AppID      string
	TaskID     *string
	Kind       string // deploy | rollback
	RollbackOf *string
	Image      string
	GitRef     string
	CreatedBy  *string
	CreatedAt  time.Time
}

// AppStore persists applications and their deployment history. Name and host
// port are unique per server (ErrConflict).
type AppStore interface {
	CreateApp(ctx context.Context, a *App) error
	GetApp(ctx context.Context, id string) (*App, error)
	ListApps(ctx context.Context, serverID string) ([]App, error)
	UpdateApp(ctx context.Context, a *App) error
	DeleteApp(ctx context.Context, id string) error

	CreateDeployment(ctx context.Context, d *Deployment) error
	GetDeployment(ctx context.Context, id string) (*Deployment, error)
	// ListDeployments returns an app's deployments, newest first.
	ListDeployments(ctx context.Context, appID string, limit int) ([]Deployment, error)
}

// ---- Reverse proxy and websites (Phase 5) ----

type ProxySettings struct {
	ServerID    string
	Enabled     bool
	HTTPPort    int
	HTTPSPort   int
	ACMEEmail   string
	ACMEStaging bool
	UpdatedAt   time.Time
}

type Site struct {
	ID           string
	ServerID     string
	Domain       string
	UpstreamPort int
	TLSMode      string
	CreatedBy    *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ProxyStore persists per-server proxy settings and websites. Domains are
// unique across all servers (ErrConflict).
type ProxyStore interface {
	// GetProxySettings returns ErrNotFound when the server has none yet.
	GetProxySettings(ctx context.Context, serverID string) (*ProxySettings, error)
	SaveProxySettings(ctx context.Context, p *ProxySettings) error

	CreateSite(ctx context.Context, s *Site) error
	GetSite(ctx context.Context, id string) (*Site, error)
	ListSites(ctx context.Context, serverID string) ([]Site, error)
	UpdateSite(ctx context.Context, s *Site) error
	DeleteSite(ctx context.Context, id string) error
}
