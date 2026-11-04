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
	CMSStore
	BackupStore
	MonitorStore
	FleetStore
	JobStore
	ProviderStore
	ProvisioningStore
	CustomerStore

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
	// BatchID links the task to a fleet batch (nil for single-server tasks).
	BatchID    *string
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

// ---- CMS instances (Phase 6) ----

type CMSInstance struct {
	ID            string
	ServerID      string
	Kind          string
	Name          string
	Title         string
	URL           string
	HostPort      int
	AdminUser     string
	AdminEmail    string
	InstallTaskID *string
	SiteID        *string
	CreatedBy     *string
	CreatedAt     time.Time
}

// CMSStore persists CMS instances; name and host port are unique per server (ErrConflict).
type CMSStore interface {
	CreateCMS(ctx context.Context, c *CMSInstance) error
	GetCMS(ctx context.Context, id string) (*CMSInstance, error)
	ListCMS(ctx context.Context, serverID string) ([]CMSInstance, error)
	DeleteCMS(ctx context.Context, id string) error
}

// ---- Encrypted backups (Phase 7) ----

type BackupKey struct {
	ServerID    string
	Recipient   string
	IdentityEnc []byte
}

type BackupPolicy struct {
	ID            string
	ServerID      string
	Kind          string
	Target        string
	IntervalHours int
	Keep          int
	LastRunAt     *time.Time
	CreatedBy     *string
	CreatedAt     time.Time
}

type Backup struct {
	ID          string
	ServerID    string
	Kind        string
	Target      string
	TaskID      *string
	PolicyID    *string
	Size        *int64
	SHA256      *string
	PlainSHA256 *string
	Files       *int
	CreatedBy   *string
	CreatedAt   time.Time
}

type BackupStore interface {
	// EnsureBackupKey stores k unless the server already has a key, and returns the stored key.
	EnsureBackupKey(ctx context.Context, k *BackupKey) (*BackupKey, error)

	CreateBackup(ctx context.Context, b *Backup) error
	GetBackup(ctx context.Context, id string) (*Backup, error)
	// ListBackups returns a server's backups, newest first.
	ListBackups(ctx context.Context, serverID string, limit int) ([]Backup, error)
	// SetBackupResult records size and checksums once the agent has reported them.
	SetBackupResult(ctx context.Context, id string, size int64, sha, plainSHA string, files int) error
	DeleteBackup(ctx context.Context, id string) error

	CreateBackupPolicy(ctx context.Context, p *BackupPolicy) error
	GetBackupPolicy(ctx context.Context, id string) (*BackupPolicy, error)
	// ListBackupPolicies lists one server's policies, or all when serverID is empty.
	ListBackupPolicies(ctx context.Context, serverID string) ([]BackupPolicy, error)
	MarkBackupPolicyRun(ctx context.Context, id string, at time.Time) error
	DeleteBackupPolicy(ctx context.Context, id string) error
}

// ---- Alerts, notifications, long-term metrics (Phase 8) ----

type NotificationChannel struct {
	ID             string
	OrganizationID string
	Name           string
	Kind           string // webhook | email
	Config         json.RawMessage
	SecretEnc      []byte
	Enabled        bool
	CreatedBy      *string
	CreatedAt      time.Time
}

type AlertRule struct {
	ID              string
	OrganizationID  string
	Name            string
	ServerID        *string // nil (and GroupID nil): every active server of the organization
	GroupID         *string // the rule applies to the group's members
	Metric          string
	Threshold       float64
	DurationMinutes int
	ChannelIDs      []string
	Enabled         bool
	CreatedBy       *string
	CreatedAt       time.Time
}

type AlertState struct {
	RuleID    string
	ServerID  string
	Firing    bool
	Since     time.Time
	Value     float64
	UpdatedAt time.Time
}

type AlertEvent struct {
	ID             string
	OrganizationID string
	RuleID         *string
	RuleName       string
	ServerID       *string
	ServerName     string
	Metric         string
	State          string // firing | resolved | test
	Value          float64
	Threshold      float64
	Delivered      int
	Failed         int
	DeliveryError  string
	// Pending counts notifications still queued for delivery.
	Pending   int
	CreatedAt time.Time
}

type MetricRollup struct {
	ServerID string
	Hour     time.Time
	Data     json.RawMessage
}

type MonitorStore interface {
	CreateChannel(ctx context.Context, c *NotificationChannel) error
	GetChannel(ctx context.Context, id string) (*NotificationChannel, error)
	ListChannels(ctx context.Context, orgID string) ([]NotificationChannel, error)
	UpdateChannel(ctx context.Context, c *NotificationChannel) error
	// DeleteChannel also removes the channel from every alert rule.
	DeleteChannel(ctx context.Context, id string) error

	CreateAlertRule(ctx context.Context, r *AlertRule) error
	GetAlertRule(ctx context.Context, id string) (*AlertRule, error)
	// ListAlertRules lists an organization's rules, or all rules when orgID is empty.
	ListAlertRules(ctx context.Context, orgID string) ([]AlertRule, error)
	UpdateAlertRule(ctx context.Context, r *AlertRule) error
	DeleteAlertRule(ctx context.Context, id string) error

	GetAlertState(ctx context.Context, ruleID, serverID string) (*AlertState, error)
	PutAlertState(ctx context.Context, s *AlertState) error

	InsertAlertEvent(ctx context.Context, e *AlertEvent) error
	GetAlertEvent(ctx context.Context, id string) (*AlertEvent, error)
	SetAlertEventDelivery(ctx context.Context, id string, delivered, failed int, errMsg string) error
	// RecordAlertDelivery moves one pending notification to delivered (ok) or failed,
	// appending errMsg to the delivery error (bounded to 1000 bytes).
	RecordAlertDelivery(ctx context.Context, id string, ok bool, errMsg string) error
	ListAlertEvents(ctx context.Context, orgID string, limit int) ([]AlertEvent, error)

	UpsertRollup(ctx context.Context, r *MetricRollup) error
	ListRollups(ctx context.Context, serverID string, since time.Time) ([]MetricRollup, error)
	PruneRollups(ctx context.Context, before time.Time) (int, error)
	// PruneAudit deletes audit entries older than before (retention).
	PruneAudit(ctx context.Context, before time.Time) (int, error)
}

// AppendDeliveryError joins delivery errors with "; ", bounded to 1000 bytes.
func AppendDeliveryError(prev, msg string) string {
	if msg == "" {
		return prev
	}
	if prev != "" {
		msg = prev + "; " + msg
	}
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	return msg
}

// ---- Fleet: server groups and task batches (Phase 9) ----

type ServerGroup struct {
	ID             string
	OrganizationID string
	Name           string
	Description    string
	ServerIDs      []string // sorted
	CreatedBy      *string
	CreatedAt      time.Time
}

type TaskBatch struct {
	ID             string
	OrganizationID string
	Type           string
	Params         json.RawMessage // redacted
	GroupID        *string
	Target         string
	Skipped        json.RawMessage // [{server_id, name, reason}]
	RequestedBy    *string
	CreatedAt      time.Time
}

// FleetStore persists server groups and fleet task batches. Group names are
// unique per organization (ErrConflict).
type FleetStore interface {
	CreateServerGroup(ctx context.Context, g *ServerGroup) error
	GetServerGroup(ctx context.Context, id string) (*ServerGroup, error)
	ListServerGroups(ctx context.Context, orgID string) ([]ServerGroup, error)
	// UpdateServerGroup changes the name and description.
	UpdateServerGroup(ctx context.Context, g *ServerGroup) error
	// DeleteServerGroup returns ErrConflict while an alert rule targets the group.
	DeleteServerGroup(ctx context.Context, id string) error
	// SetServerGroupMembers replaces the members (ErrNotFound for an unknown server).
	SetServerGroupMembers(ctx context.Context, groupID string, serverIDs []string) error

	// CreateTaskBatch stores the batch and its tasks in one transaction.
	CreateTaskBatch(ctx context.Context, b *TaskBatch, tasks []*Task) error
	GetTaskBatch(ctx context.Context, id string) (*TaskBatch, error)
	ListTaskBatches(ctx context.Context, orgID string, limit int) ([]TaskBatch, error)
	ListBatchTasks(ctx context.Context, batchID string) ([]Task, error)
}

// ---- Background jobs and leases (Phase 9) ----

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

type Job struct {
	ID          string
	Kind        string
	Payload     json.RawMessage
	Status      JobStatus
	Attempts    int
	MaxAttempts int
	RunAt       time.Time
	LockedUntil *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type JobStore interface {
	EnqueueJob(ctx context.Context, j *Job) error
	// ClaimJobs moves up to limit due jobs (queued with run_at <= now, or running
	// with an expired lease) to running, increments their attempts and leases them.
	ClaimJobs(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Job, error)
	// FinishJob ends a running job: succeeded, failed, or queued again at retryAt.
	FinishJob(ctx context.Context, id string, status JobStatus, retryAt time.Time, errMsg string, now time.Time) error
	GetJob(ctx context.Context, id string) (*Job, error)
	// ListJobs lists the newest jobs, optionally of one status.
	ListJobs(ctx context.Context, status JobStatus, limit int) ([]Job, error)
	CountJobs(ctx context.Context) (map[JobStatus]int, error)
	// PruneJobs deletes succeeded and failed jobs last updated before before.
	PruneJobs(ctx context.Context, before time.Time) (int, error)

	// AcquireLease takes or renews the named lease for holder until now+ttl.
	// It reports false while another holder's lease is unexpired.
	AcquireLease(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error)
	// ReleaseLeases drops every lease of holder (graceful shutdown), so another
	// process or the restarted one takes over at once.
	ReleaseLeases(ctx context.Context, holder string) error
}

// ---- Provider Mode: infrastructure and VPS (Phase 10) ----

type Datacenter struct {
	ID             string
	OrganizationID string
	Name           string
	Location       string
	CreatedAt      time.Time
}

type Rack struct {
	ID           string
	DatacenterID string
	Name         string
	Units        int
	CreatedAt    time.Time
}

// Node is an enrolled server used as a libvirt/KVM hypervisor.
type Node struct {
	ID               string
	OrganizationID   string
	ServerID         string
	DatacenterID     string
	RackID           *string
	Hypervisor       string
	Network          string
	CPUOvercommit    float64
	MemoryOvercommit float64
	Enabled          bool
	CreatedAt        time.Time
}

type IPPool struct {
	ID             string
	OrganizationID string
	DatacenterID   *string
	Name           string
	CIDR           string // e.g. 192.168.122.0/24
	Gateway        string
	First, Last    string
	DNS            []string
	CreatedAt      time.Time
}

type VMTemplate struct {
	ID             string
	OrganizationID string
	Name           string
	ImageURL       string
	ImageSHA256    string
	MinDiskGB      int
	DefaultUser    string
	CreatedAt      time.Time
}

type VPS struct {
	ID             string
	OrganizationID string
	NodeID         string
	TemplateID     *string
	PoolID         string
	Name           string
	VCPUs          int
	MemoryMB       int
	DiskGB         int
	IPAddress      string
	SSHKeys        []string
	State          string // creating | running | stopped | error | deleting
	PendingTaskID  *string
	PendingOp      string
	LastError      string
	CreatedBy      *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CustomerID     *string // owner in the Customer Panel (nil: operators only)
}

// ProviderStore persists Provider Mode infrastructure. Names are unique per
// organization (racks per data center): ErrConflict. Deleting a record that
// others still reference returns ErrConflict.
type ProviderStore interface {
	CreateDatacenter(ctx context.Context, d *Datacenter) error
	GetDatacenter(ctx context.Context, id string) (*Datacenter, error)
	ListDatacenters(ctx context.Context, orgID string) ([]Datacenter, error)
	DeleteDatacenter(ctx context.Context, id string) error

	CreateRack(ctx context.Context, r *Rack) error
	GetRack(ctx context.Context, id string) (*Rack, error)
	ListRacks(ctx context.Context, datacenterID string) ([]Rack, error)
	DeleteRack(ctx context.Context, id string) error

	CreateNode(ctx context.Context, n *Node) error
	GetNode(ctx context.Context, id string) (*Node, error)
	ListNodes(ctx context.Context, orgID string) ([]Node, error)
	DeleteNode(ctx context.Context, id string) error

	CreateIPPool(ctx context.Context, p *IPPool) error
	GetIPPool(ctx context.Context, id string) (*IPPool, error)
	ListIPPools(ctx context.Context, orgID string) ([]IPPool, error)
	DeleteIPPool(ctx context.Context, id string) error
	// ListPoolAddresses returns the allocated addresses of a pool.
	ListPoolAddresses(ctx context.Context, poolID string) ([]string, error)

	CreateVMTemplate(ctx context.Context, t *VMTemplate) error
	GetVMTemplate(ctx context.Context, id string) (*VMTemplate, error)
	ListVMTemplates(ctx context.Context, orgID string) ([]VMTemplate, error)
	DeleteVMTemplate(ctx context.Context, id string) error

	// CreateVPS stores the VPS and allocates its address in one transaction;
	// ErrConflict when the name or the address is taken.
	CreateVPS(ctx context.Context, v *VPS) error
	GetVPS(ctx context.Context, id string) (*VPS, error)
	ListVPS(ctx context.Context, orgID string) ([]VPS, error)
	ListNodeVPS(ctx context.Context, nodeID string) ([]VPS, error)
	SetVPSState(ctx context.Context, id, state string, pendingTaskID *string, pendingOp, lastError string) error
	// DeleteVPS removes the VPS and releases its address.
	DeleteVPS(ctx context.Context, id string) error
}

// ---- Provisioning API (Phase 11) ----

type APIClient struct {
	ID             string
	OrganizationID string
	Name           string
	KeyID          string
	SecretEnc      []byte
	Scopes         []string
	AllowedCIDRs   []string
	WebhookURL     string
	Enabled        bool
	CreatedBy      string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

type IdempotencyRecord struct {
	ClientID    string
	Key         string
	RequestHash string
	StatusCode  int // 0 while the first request is in progress
	Response    []byte
	CreatedAt   time.Time
}

type ServicePlan struct {
	ID             string
	OrganizationID string
	Name           string
	Kind           string
	TemplateID     string
	PoolID         string
	NodeID         *string
	VCPUs          int
	MemoryMB       int
	DiskGB         int
	Enabled        bool
	CreatedAt      time.Time
}

type ProvisionedService struct {
	ID             string
	OrganizationID string
	ClientID       string
	PlanID         *string
	ExternalID     string
	VPSID          *string
	Status         string // provisioning | active | suspended | failed | terminating | terminated
	Pending        string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ProvisioningStore interface {
	CreateAPIClient(ctx context.Context, c *APIClient) error
	GetAPIClient(ctx context.Context, id string) (*APIClient, error)
	GetAPIClientByKey(ctx context.Context, keyID string) (*APIClient, error)
	ListAPIClients(ctx context.Context, orgID string) ([]APIClient, error)
	SetAPIClientEnabled(ctx context.Context, id string, enabled bool) error
	TouchAPIClient(ctx context.Context, id string, at time.Time) error
	// DeleteAPIClient returns ErrConflict while services provisioned by it exist.
	DeleteAPIClient(ctx context.Context, id string) error

	// UseNonce records a nonce; ErrConflict when it was already used and has not expired.
	UseNonce(ctx context.Context, clientID, nonce string, expiresAt, now time.Time) error
	PruneNonces(ctx context.Context, before time.Time) (int, error)

	// BeginIdempotent stores rec (status 0) unless the key exists; it then returns the existing record.
	BeginIdempotent(ctx context.Context, rec *IdempotencyRecord) (*IdempotencyRecord, error)
	FinishIdempotent(ctx context.Context, clientID, key string, status int, response []byte) error
	DeleteIdempotent(ctx context.Context, clientID, key string) error
	PruneIdempotency(ctx context.Context, before time.Time) (int, error)

	CreateServicePlan(ctx context.Context, p *ServicePlan) error
	GetServicePlan(ctx context.Context, id string) (*ServicePlan, error)
	ListServicePlans(ctx context.Context, orgID string) ([]ServicePlan, error)
	DeleteServicePlan(ctx context.Context, id string) error

	// CreateProvisionedService returns ErrConflict when the client already used the external id.
	CreateProvisionedService(ctx context.Context, s *ProvisionedService) error
	GetProvisionedService(ctx context.Context, id string) (*ProvisionedService, error)
	GetProvisionedServiceByExternal(ctx context.Context, clientID, externalID string) (*ProvisionedService, error)
	ListProvisionedServices(ctx context.Context, orgID string) ([]ProvisionedService, error)
	// ListPendingServices returns services whose operation the reconciler still waits for.
	ListPendingServices(ctx context.Context, limit int) ([]ProvisionedService, error)
	UpdateProvisionedService(ctx context.Context, s *ProvisionedService) error
	GetProvisionedServiceByVPS(ctx context.Context, vpsID string) (*ProvisionedService, error)
	// DeleteProvisionedService forgets a service that never got a VPS (creation refused).
	DeleteProvisionedService(ctx context.Context, id string) error
}

// ---- Customer Panel (Phase 12) ----

type CustomerStatus string

const (
	CustomerActive   CustomerStatus = "active"
	CustomerDisabled CustomerStatus = "disabled"
)

type Customer struct {
	ID             string
	OrganizationID string
	Email          string // lower case
	Name           string
	Status         CustomerStatus
	PasswordHash   string // empty until the setup link was used
	SetupExpiresAt *time.Time
	CreatedBy      *string
	CreatedAt      time.Time
	LastLoginAt    *time.Time
}

type CustomerSession struct {
	ID         string
	CustomerID string
	TokenHash  []byte
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	RevokedAt  *time.Time
}

// CustomerStore persists customers and their sessions. E-mail addresses are
// unique (ErrConflict). Customer-facing reads always filter by customer id.
type CustomerStore interface {
	CreateCustomer(ctx context.Context, c *Customer, setupHash []byte) error
	GetCustomer(ctx context.Context, id string) (*Customer, error)
	GetCustomerByEmail(ctx context.Context, email string) (*Customer, error)
	ListCustomers(ctx context.Context, orgID string) ([]Customer, error)
	SetCustomerStatus(ctx context.Context, id string, status CustomerStatus) error
	// SetCustomerSetupToken replaces the one-time setup token.
	SetCustomerSetupToken(ctx context.Context, id string, hash []byte, expiresAt time.Time) error
	// ConsumeCustomerSetup sets the password for the unexpired token and clears it
	// atomically (ErrNotFound when the token is unknown, used or expired).
	ConsumeCustomerSetup(ctx context.Context, hash []byte, passwordHash string, now time.Time) (*Customer, error)
	SetCustomerPassword(ctx context.Context, id, passwordHash string) error
	TouchCustomerLogin(ctx context.Context, id string, at time.Time) error

	CreateCustomerSession(ctx context.Context, s *CustomerSession) error
	GetCustomerSessionByTokenHash(ctx context.Context, hash []byte) (*CustomerSession, error)
	TouchCustomerSession(ctx context.Context, id string, at time.Time) error
	RevokeCustomerSession(ctx context.Context, id string, at time.Time) error
	// RevokeCustomerSessions revokes every session of the customer except keepID.
	RevokeCustomerSessions(ctx context.Context, customerID, keepID string, at time.Time) error

	SetVPSCustomer(ctx context.Context, vpsID string, customerID *string) error
	ListCustomerVPS(ctx context.Context, customerID string) ([]VPS, error)
	// GetCustomerVPS returns the VPS only when it belongs to the customer (ErrNotFound otherwise).
	GetCustomerVPS(ctx context.Context, customerID, vpsID string) (*VPS, error)
}
