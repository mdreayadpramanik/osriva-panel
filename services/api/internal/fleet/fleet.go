// Package fleet manages servers and their agents: the internal CA, one-time
// enrolment, mutual-TLS agent authentication, reports and renewal.
package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/compat"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/pki"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/ratelimit"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/reqmeta"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const EnrollmentTTL = time.Hour

var (
	// ErrAgentNotConfigured means OSRIVA_AGENT_ADDR / OSRIVA_AGENT_PUBLIC_URL / OSRIVA_PUBLIC_URL are unset.
	ErrAgentNotConfigured = errors.New("the agent endpoint is not configured: set OSRIVA_PUBLIC_URL, OSRIVA_AGENT_ADDR and OSRIVA_AGENT_PUBLIC_URL")
	ErrInvalidEnrollment  = errors.New("the enrolment token is invalid, expired or already used")
	ErrAgentForbidden     = errors.New("agent certificate is not valid for an active server")
)

// caKeyAAD binds the encrypted CA key to its purpose.
var caKeyAAD = []byte("osriva/agent-ca-key")

type Options struct {
	Store    store.Store
	Identity *identity.Service
	Keys     *secrets.Keyring
	Log      *slog.Logger
	// PanelURL is where agents reach POST /api/v1/agent/enroll (OSRIVA_PUBLIC_URL).
	PanelURL string
	// AgentURL is the public https:// URL of the mTLS agent listener (OSRIVA_AGENT_PUBLIC_URL).
	AgentURL string
}

type Service struct {
	store    store.Store
	id       *identity.Service
	log      *slog.Logger
	ca       *pki.CA
	panelURL string
	agentURL string
	limiter  *ratelimit.KeyedLimiter

	notify notifier

	certMu     sync.Mutex
	serverCert *tls.Certificate
}

// New loads the internal CA, creating it on first start.
func New(ctx context.Context, o Options) (*Service, error) {
	s := &Service{store: o.Store, id: o.Identity, log: o.Log, panelURL: strings.TrimRight(o.PanelURL, "/"),
		agentURL: strings.TrimRight(o.AgentURL, "/"), limiter: ratelimit.NewKeyed(0.2, 10)}
	if s.log == nil {
		s.log = slog.Default()
	}
	ca, err := loadOrCreateCA(ctx, o.Store, o.Keys, s.id.Now(), s.log)
	if err != nil {
		return nil, fmt.Errorf("agent CA: %w", err)
	}
	s.ca = ca
	return s, nil
}

func loadOrCreateCA(ctx context.Context, st store.Store, keys *secrets.Keyring, now time.Time, log *slog.Logger) (*pki.CA, error) {
	for attempt := 0; attempt < 2; attempt++ {
		certPEM, keyEnc, err := st.GetAgentCA(ctx)
		if err == nil {
			keyDER, err := keys.Decrypt(keyEnc, caKeyAAD)
			if err != nil {
				return nil, errors.New("cannot decrypt the agent CA key: OSRIVA_SECRET_KEY differs from the one that created it")
			}
			return pki.LoadCA([]byte(certPEM), keyDER)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		ca, err := pki.NewCA(now)
		if err != nil {
			return nil, err
		}
		keyDER, err := ca.MarshalKey()
		if err != nil {
			return nil, err
		}
		enc, err := keys.Encrypt(keyDER, caKeyAAD)
		if err != nil {
			return nil, err
		}
		err = st.InsertAgentCA(ctx, string(ca.CertPEM), enc)
		if err == nil {
			log.Info("created agent CA", "fingerprint", ca.Fingerprint())
			return ca, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		// Another instance created it concurrently; load theirs.
	}
	return nil, errors.New("could not load or create the agent CA")
}

func (s *Service) CA() *pki.CA           { return s.ca }
func (s *Service) Configured() bool      { return s.panelURL != "" && s.agentURL != "" }
func (s *Service) CAFingerprint() string { return s.ca.Fingerprint() }
func (s *Service) AgentEndpoint() string { return s.agentURL }

// AgentTLSConfig is the TLS configuration of the mutual-TLS agent listener.
func (s *Service) AgentTLSConfig() (*tls.Config, error) {
	u, err := url.Parse(s.agentURL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid OSRIVA_AGENT_PUBLIC_URL %q", s.agentURL)
	}
	host := u.Hostname()
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  s.ca.Pool(),
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return s.listenerCertificate(host)
		},
	}, nil
}

// listenerCertificate reissues the listener certificate when a third of its lifetime remains.
func (s *Service) listenerCertificate(host string) (*tls.Certificate, error) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	now := s.id.Now()
	if c := s.serverCert; c != nil && c.Leaf.NotAfter.Sub(now) > c.Leaf.NotAfter.Sub(c.Leaf.NotBefore)/3 {
		return c, nil
	}
	c, err := s.ca.ServerCertificate([]string{host}, now)
	if err != nil {
		return nil, err
	}
	s.serverCert = &c
	return &c, nil
}

// ---- operator-facing operations ----

type Connection string

const (
	ConnPending Connection = "pending"
	ConnOnline  Connection = "online"
	ConnOffline Connection = "offline"
	ConnRevoked Connection = "revoked"
)

// ServerView is a server with its derived connection state.
type ServerView struct {
	Server     store.Server
	Connection Connection
}

// Enrollment is shown to the operator exactly once.
type Enrollment struct {
	Token         string
	ExpiresAt     time.Time
	PanelURL      string
	CAFingerprint string
	Command       string
}

func (s *Service) view(x store.Server) ServerView {
	v := ServerView{Server: x}
	switch {
	case x.Status == store.ServerRevoked:
		v.Connection = ConnRevoked
	case x.Status == store.ServerPending:
		v.Connection = ConnPending
	case x.LastSeenAt != nil && s.id.Now().Sub(*x.LastSeenAt) <= agentproto.OfflineAfter:
		v.Connection = ConnOnline
	default:
		v.Connection = ConnOffline
	}
	return v
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func (s *Service) newEnrollment(create func(hash []byte, exp time.Time) error) (*Enrollment, error) {
	if !s.Configured() {
		return nil, &identity.PolicyError{Message: ErrAgentNotConfigured.Error()}
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return nil, err
	}
	exp := s.id.Now().Add(EnrollmentTTL)
	if err := create(hash, exp); err != nil {
		return nil, err
	}
	fp := s.ca.Fingerprint()
	return &Enrollment{
		Token: token, ExpiresAt: exp, PanelURL: s.panelURL, CAFingerprint: fp,
		Command: fmt.Sprintf("osriva-agent enroll --panel-url %s --token %s --ca-fingerprint %s", s.panelURL, token, fp),
	}, nil
}

func (s *Service) CreateServer(ctx context.Context, actor *identity.Principal, name string) (*ServerView, *Enrollment, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersWrite); err != nil {
		return nil, nil, err
	}
	name = strings.TrimSpace(name)
	if !validName.MatchString(name) {
		return nil, nil, &identity.ValidationError{Field: "name", Message: "must be 1–100 characters: letters, digits, dot, dash or underscore, starting with a letter or digit"}
	}
	x := &store.Server{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: name, CreatedBy: &actor.User.ID}
	enr, err := s.newEnrollment(func(hash []byte, exp time.Time) error {
		x.EnrollmentTokenHash, x.EnrollmentExpiresAt = hash, &exp
		return s.store.CreateServer(ctx, x)
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, nil, &identity.ValidationError{Field: "name", Message: "is already used by another server"}
	}
	if err != nil {
		return nil, nil, err
	}
	s.id.Audit(ctx, actor.User, "servers.create", "server", x.ID, store.OutcomeSuccess, map[string]any{"name": name})
	v := s.view(*x)
	return &v, enr, nil
}

func (s *Service) getInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.Server, error) {
	x, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, err
	}
	if x.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return x, nil
}

// RegenerateEnrollment issues a new token for a server that has not enrolled yet.
func (s *Service) RegenerateEnrollment(ctx context.Context, actor *identity.Principal, id string) (*Enrollment, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersWrite); err != nil {
		return nil, err
	}
	x, err := s.getInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if x.Status != store.ServerPending {
		return nil, &identity.PolicyError{Message: "this server's agent is already enrolled or revoked"}
	}
	enr, err := s.newEnrollment(func(hash []byte, exp time.Time) error {
		return s.store.SetEnrollmentToken(ctx, x.ID, hash, exp)
	})
	if err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "servers.enrollment_token", "server", x.ID, store.OutcomeSuccess, nil)
	return enr, nil
}

func (s *Service) List(ctx context.Context, actor *identity.Principal) ([]ServerView, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	list, err := s.store.ListServers(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]ServerView, len(list))
	for i, x := range list {
		out[i] = s.view(x)
	}
	return out, nil
}

// Detail is a server with its latest inventory and capabilities (nil before the first report).
type Detail struct {
	ServerView
	Inventory    *inventory.Inventory
	Capabilities []compat.Capability
	ReceivedAt   *time.Time
}

func (s *Service) Get(ctx context.Context, actor *identity.Principal, id string) (*Detail, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	x, err := s.getInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	d := &Detail{ServerView: s.view(*x)}
	snap, err := s.store.GetServerInventory(ctx, x.ID)
	if errors.Is(err, store.ErrNotFound) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	var inv inventory.Inventory
	if err := json.Unmarshal(snap.Data, &inv); err != nil {
		return nil, fmt.Errorf("decode stored inventory: %w", err)
	}
	_ = json.Unmarshal(snap.Capabilities, &d.Capabilities)
	d.Inventory, d.ReceivedAt = &inv, &snap.ReceivedAt
	return d, nil
}

// Revoke permanently revokes a server's agent credentials (dangerous: re-auth required).
func (s *Service) Revoke(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.id.Require(ctx, actor, rbac.ServersWrite); err != nil {
		return err
	}
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return err
	}
	x, err := s.getInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if x.Status == store.ServerRevoked {
		return &identity.PolicyError{Message: "this server is already revoked"}
	}
	if err := s.store.RevokeServer(ctx, x.ID, s.id.Now()); err != nil {
		return err
	}
	s.id.Audit(ctx, actor.User, "servers.revoke", "server", x.ID, store.OutcomeSuccess, map[string]any{"name": x.Name})
	return nil
}

// ---- agent-facing operations ----

// Enroll handles POST /api/v1/agent/enroll (unauthenticated; the token is the credential).
func (s *Service) Enroll(ctx context.Context, req agentproto.EnrollRequest) (*agentproto.EnrollResponse, error) {
	meta := reqmeta.From(ctx)
	if !s.limiter.Allow(meta.IP) {
		return nil, identity.ErrRateLimited
	}
	if !s.Configured() {
		return nil, ErrAgentNotConfigured
	}
	fail := func(reason string, err error) (*agentproto.EnrollResponse, error) {
		s.id.Audit(ctx, nil, "agent.enroll", "server", "", store.OutcomeFailure, map[string]any{"reason": reason})
		return nil, err
	}
	if req.Token == "" || len(req.Token) > 128 {
		return fail("missing_token", ErrInvalidEnrollment)
	}
	now := s.id.Now()
	hash := auth.HashToken(req.Token)
	x, err := s.store.FindPendingEnrollment(ctx, hash, now)
	if errors.Is(err, store.ErrNotFound) {
		return fail("invalid_token", ErrInvalidEnrollment)
	}
	if err != nil {
		return nil, err
	}
	cert, certPEM, err := s.ca.SignClientCSR([]byte(req.CSRPEM), x.ID, now)
	if err != nil {
		return fail("invalid_csr", &identity.ValidationError{Field: "csr_pem", Message: err.Error()})
	}
	rec := &store.AgentCertificate{Serial: pki.SerialHex(cert), ServerID: x.ID, Fingerprint: pki.Fingerprint(cert), NotAfter: cert.NotAfter}
	if err := s.store.CompleteEnrollment(ctx, x.ID, hash, now, rec); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fail("token_race", ErrInvalidEnrollment)
		}
		return nil, err
	}
	s.id.Audit(ctx, nil, "agent.enroll", "server", x.ID, store.OutcomeSuccess,
		map[string]any{"name": x.Name, "agent_version": truncate(req.AgentVersion, 64), "cert_serial": rec.Serial})
	return &agentproto.EnrollResponse{ServerID: x.ID, ServerName: x.Name, CertificatePEM: string(certPEM),
		CAPEM: string(s.ca.CertPEM), AgentEndpoint: s.agentURL}, nil
}

// AuthenticateAgent maps a verified client certificate to its active server.
// The TLS layer has already checked the chain; this checks revocation and status.
func (s *Service) AuthenticateAgent(ctx context.Context, cert *x509.Certificate) (*store.Server, error) {
	rec, err := s.store.GetAgentCertificate(ctx, pki.SerialHex(cert))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrAgentForbidden
	}
	if err != nil {
		return nil, err
	}
	if rec.RevokedAt != nil || rec.Fingerprint != pki.Fingerprint(cert) || rec.ServerID != cert.Subject.CommonName || !s.id.Now().Before(rec.NotAfter) {
		return nil, ErrAgentForbidden
	}
	x, err := s.store.GetServer(ctx, rec.ServerID)
	if err != nil {
		return nil, err
	}
	if x.Status != store.ServerActive {
		return nil, ErrAgentForbidden
	}
	return x, nil
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// Report stores an agent's inventory and recomputes its capabilities.
func (s *Service) Report(ctx context.Context, x *store.Server, req agentproto.ReportRequest) (*agentproto.ReportResponse, error) {
	inv := req.Inventory
	if inv == nil {
		return nil, &identity.ValidationError{Field: "inventory", Message: "is required"}
	}
	if inv.SchemaVersion != inventory.SchemaVersion {
		return nil, &identity.ValidationError{Field: "inventory.schema_version", Message: fmt.Sprintf("unsupported (control plane expects %d)", inventory.SchemaVersion)}
	}
	now := s.id.Now()
	// Never trust the agent clock for anything beyond display.
	if inv.CollectedAt.IsZero() || inv.CollectedAt.After(now.Add(5*time.Minute)) {
		inv.CollectedAt = now
	}
	data, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}
	caps, err := json.Marshal(compat.Evaluate(inv))
	if err != nil {
		return nil, err
	}
	facts := store.ServerFacts{
		Hostname: truncate(inv.Hostname, 255), OSFamily: truncate(inv.OS.Family, 32), OSName: truncate(inv.OS.Name, 200),
		OSVersion: truncate(inv.OS.Version, 64), Kernel: truncate(inv.OS.Kernel, 128), Arch: truncate(inv.OS.Arch, 32),
		AgentVersion: truncate(req.AgentVersion, 64),
	}
	snap := &store.ServerInventory{CollectedAt: inv.CollectedAt, Data: data, Capabilities: caps}
	if err := s.store.RecordReport(ctx, x.ID, facts, reqmeta.From(ctx).IP, now, snap); err != nil {
		return nil, err
	}
	return &agentproto.ReportResponse{NextReportSeconds: int(agentproto.DefaultReportInterval.Seconds())}, nil
}

// Renew issues a fresh certificate to an authenticated agent.
func (s *Service) Renew(ctx context.Context, x *store.Server, req agentproto.RenewRequest) (*agentproto.RenewResponse, error) {
	now := s.id.Now()
	cert, certPEM, err := s.ca.SignClientCSR([]byte(req.CSRPEM), x.ID, now)
	if err != nil {
		return nil, &identity.ValidationError{Field: "csr_pem", Message: err.Error()}
	}
	rec := &store.AgentCertificate{Serial: pki.SerialHex(cert), ServerID: x.ID, Fingerprint: pki.Fingerprint(cert), NotAfter: cert.NotAfter, IssuedAt: now}
	if err := s.store.AddAgentCertificate(ctx, rec); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, nil, "agent.renew", "server", x.ID, store.OutcomeSuccess, map[string]any{"cert_serial": rec.Serial})
	return &agentproto.RenewResponse{CertificatePEM: string(certPEM)}, nil
}
