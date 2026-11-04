// Package provisioning is the signed API that external billing systems use to
// create, suspend, unsuspend and terminate services from technical plans, plus
// the signed webhooks that report the outcome (docs/adr/0014-provisioning-api.md).
// There is deliberately no billing here: plans carry no prices.
package provisioning

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/jobs"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provider"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	ScopeRead  = "provisioning.read"
	ScopeWrite = "provisioning.write"

	// SignatureWindow bounds the clock difference and the life of a signed request.
	SignatureWindow    = 5 * time.Minute
	idempotencyTTL     = 24 * time.Hour
	webhookJob         = "provisioning.webhook"
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusSuspended    = "suspended"
	StatusFailed       = "failed"
	StatusTerminating  = "terminating"
	StatusTerminated   = "terminated"
)

// scopePermissions maps client scopes to the permissions they may use.
var scopePermissions = map[string][]string{
	ScopeRead:  {rbac.ProviderRead, rbac.ServersRead},
	ScopeWrite: {rbac.ProviderRead, rbac.ServersRead, rbac.ProviderWrite, rbac.ServersOperate},
}

var (
	ErrBadSignature = errors.New("the request signature is missing, expired or invalid")
	ErrReplayed     = errors.New("this request was already received (nonce reused)")
	nonceRe         = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	externalRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

type Service struct {
	store    store.Store
	id       *identity.Service
	provider *provider.Service
	keys     *secrets.Keyring
	queue    *jobs.Queue
	lessor   *jobs.Lessor
	log      *slog.Logger
	sender   func(ctx context.Context, url, secret, event string, deliveryID string, body []byte) error
}

func New(st store.Store, id *identity.Service, prov *provider.Service, keys *secrets.Keyring, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{store: st, id: id, provider: prov, keys: keys, log: log}
	s.sender = postWebhook
	return s
}

// UseQueue delivers webhooks through the job queue with retries.
func (s *Service) UseQueue(q *jobs.Queue) {
	s.queue = q
	q.Register(webhookJob, webhookHandler{s})
}

// UseLessor makes Run reconcile only in the process holding the "provisioning" lease.
func (s *Service) UseLessor(l *jobs.Lessor) { s.lessor = l }

func clientAAD(id string) []byte { return []byte("osriva/api-client/" + id) }

func verr(field, msg string) error { return &identity.ValidationError{Field: field, Message: msg} }

// ---- API clients (managed by operators) ----

type ClientInput struct {
	Name         string   `json:"name"`
	Scopes       []string `json:"scopes"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	WebhookURL   string   `json:"webhook_url"`
}

type CreatedClient struct {
	store.APIClient
	Secret string // shown once
}

func (s *Service) requireManage(ctx context.Context, actor *identity.Principal) error {
	if err := s.id.Require(ctx, actor, rbac.SettingsWrite); err != nil {
		return err
	}
	return s.id.RequireRecentReauth(actor)
}

func validWebhook(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && len(raw) <= 500
}

func (s *Service) ListClients(ctx context.Context, actor *identity.Principal) ([]store.APIClient, error) {
	if err := s.id.Require(ctx, actor, rbac.SettingsRead); err != nil {
		return nil, err
	}
	return s.store.ListAPIClients(ctx, actor.User.OrganizationID)
}

func (s *Service) CreateClient(ctx context.Context, actor *identity.Principal, in ClientInput) (*CreatedClient, error) {
	if err := s.requireManage(ctx, actor); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 || strings.ContainsAny(in.Name, "\r\n\t\x00") {
		return nil, verr("name", "must be 1-100 characters on one line")
	}
	scopes := slices.Compact(slices.Sorted(slices.Values(in.Scopes)))
	if len(scopes) == 0 {
		return nil, verr("scopes", "choose provisioning.read and/or provisioning.write")
	}
	for _, sc := range scopes {
		if _, ok := scopePermissions[sc]; !ok {
			return nil, verr("scopes", "unknown scope "+sc)
		}
	}
	cidrs := make([]string, 0, len(in.AllowedCIDRs))
	for _, c := range in.AllowedCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			if a, aerr := netip.ParseAddr(strings.TrimSpace(c)); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				return nil, verr("allowed_cidrs", "invalid network "+c)
			}
		}
		cidrs = append(cidrs, p.Masked().String())
	}
	if len(cidrs) > 20 {
		return nil, verr("allowed_cidrs", "at most 20 networks")
	}
	in.WebhookURL = strings.TrimSpace(in.WebhookURL)
	if !validWebhook(in.WebhookURL) {
		return nil, verr("webhook_url", "must be an http(s) URL without credentials")
	}
	raw := make([]byte, 32)
	kid := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if _, err := rand.Read(kid); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(raw)
	c := &store.APIClient{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: in.Name, KeyID: "osk_" + hex.EncodeToString(kid),
		Scopes: scopes, AllowedCIDRs: cidrs, WebhookURL: in.WebhookURL, Enabled: true, CreatedBy: actor.User.ID}
	enc, err := s.keys.Encrypt([]byte(secret), clientAAD(c.ID))
	if err != nil {
		return nil, err
	}
	c.SecretEnc = enc
	if err := s.store.CreateAPIClient(ctx, c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("name", "is already used by another API client")
		}
		return nil, err
	}
	webhookHost := ""
	if u, err := url.Parse(c.WebhookURL); err == nil {
		webhookHost = u.Host
	}
	s.id.Audit(ctx, actor.User, "provisioning.client_create", "api_client", c.ID, store.OutcomeSuccess,
		map[string]any{"name": c.Name, "key_id": c.KeyID, "scopes": c.Scopes, "allowed_cidrs": c.AllowedCIDRs, "webhook_host": webhookHost})
	return &CreatedClient{APIClient: *c, Secret: secret}, nil
}

func (s *Service) clientInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.APIClient, error) {
	c, err := s.store.GetAPIClient(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return c, nil
}

func (s *Service) SetClientEnabled(ctx context.Context, actor *identity.Principal, id string, enabled bool) (*store.APIClient, error) {
	if err := s.requireManage(ctx, actor); err != nil {
		return nil, err
	}
	c, err := s.clientInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetAPIClientEnabled(ctx, c.ID, enabled); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "provisioning.client_update", "api_client", c.ID, store.OutcomeSuccess, map[string]any{"name": c.Name, "enabled": enabled})
	return s.store.GetAPIClient(ctx, c.ID)
}

func (s *Service) DeleteClient(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireManage(ctx, actor); err != nil {
		return err
	}
	c, err := s.clientInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteAPIClient(ctx, c.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &identity.PolicyError{Message: "this client provisioned services: disable it instead, or terminate its services first"}
		}
		return err
	}
	s.id.Audit(ctx, actor.User, "provisioning.client_delete", "api_client", c.ID, store.OutcomeSuccess, map[string]any{"name": c.Name})
	return nil
}

// ---- request signatures ----

// Canonical is the string a client signs.
func Canonical(method, pathQuery, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	return method + "\n" + pathQuery + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])
}

// SignRequest returns the X-Osriva-Signature value (used by clients and tests).
func SignRequest(secret, method, pathQuery, timestamp, nonce string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(Canonical(method, pathQuery, timestamp, nonce, body)))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// SignedRequest holds what Verify needs from an HTTP request.
type SignedRequest struct {
	Method, PathQuery       string
	KeyID, Timestamp, Nonce string
	Signature               string
	Body                    []byte
	RemoteIP                string
}

// Caller is an authenticated API client.
type Caller struct {
	Client    *store.APIClient
	Principal *identity.Principal
	Secret    string
}

// Verify checks the signature, the time window, the source network and the
// nonce; on success it returns the client and its principal.
func (s *Service) Verify(ctx context.Context, r SignedRequest) (*Caller, error) {
	now := s.id.Now()
	ts, err := strconv.ParseInt(r.Timestamp, 10, 64)
	if err != nil || r.KeyID == "" || !nonceRe.MatchString(r.Nonce) || !strings.HasPrefix(r.Signature, "sha256=") {
		return nil, ErrBadSignature
	}
	at := time.Unix(ts, 0)
	if at.Before(now.Add(-SignatureWindow)) || at.After(now.Add(SignatureWindow)) {
		return nil, ErrBadSignature
	}
	c, err := s.store.GetAPIClientByKey(ctx, r.KeyID)
	if err != nil {
		return nil, ErrBadSignature
	}
	plain, err := s.keys.Decrypt(c.SecretEnc, clientAAD(c.ID))
	if err != nil {
		return nil, ErrBadSignature
	}
	want := SignRequest(string(plain), r.Method, r.PathQuery, r.Timestamp, r.Nonce, r.Body)
	fail := func(reason string, err error) (*Caller, error) {
		s.id.Audit(ctx, nil, "provisioning.auth", "api_client", c.ID, store.OutcomeFailure, map[string]any{"reason": reason, "ip": r.RemoteIP})
		return nil, err
	}
	if !hmac.Equal([]byte(want), []byte(r.Signature)) {
		return fail("bad_signature", ErrBadSignature)
	}
	if !c.Enabled {
		return fail("disabled", ErrBadSignature)
	}
	if len(c.AllowedCIDRs) > 0 {
		ip, err := netip.ParseAddr(r.RemoteIP)
		allowed := false
		for _, cidr := range c.AllowedCIDRs {
			if p, perr := netip.ParsePrefix(cidr); err == nil && perr == nil && p.Contains(ip.Unmap()) {
				allowed = true
			}
		}
		if !allowed {
			return fail("source_not_allowed", ErrBadSignature)
		}
	}
	if err := s.store.UseNonce(ctx, c.ID, r.Nonce, at.Add(SignatureWindow+time.Minute), now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fail("replayed", ErrReplayed)
		}
		return nil, err
	}
	var granted []string
	for _, sc := range c.Scopes {
		granted = append(granted, scopePermissions[sc]...)
	}
	p, err := s.id.ClientPrincipal(ctx, c.CreatedBy, c.ID, granted)
	if err != nil {
		return fail("creator_inactive", ErrBadSignature)
	}
	_ = s.store.TouchAPIClient(ctx, c.ID, now)
	return &Caller{Client: c, Principal: p, Secret: string(plain)}, nil
}

// ---- idempotency ----

// IdempotencyHash identifies a request for Idempotency-Key comparisons.
func IdempotencyHash(method, pathQuery string, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", method, pathQuery)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Begin reserves an idempotency key; it returns the stored record when the key
// was used before (StatusCode 0 means the first request is still running).
func (s *Service) Begin(ctx context.Context, clientID, key, hash string) (*store.IdempotencyRecord, error) {
	return s.store.BeginIdempotent(ctx, &store.IdempotencyRecord{ClientID: clientID, Key: key, RequestHash: hash})
}

// Finish stores the response; server errors are forgotten so the client may retry.
func (s *Service) Finish(ctx context.Context, clientID, key string, status int, body []byte) {
	var err error
	if status >= 500 {
		err = s.store.DeleteIdempotent(context.WithoutCancel(ctx), clientID, key)
	} else {
		err = s.store.FinishIdempotent(context.WithoutCancel(ctx), clientID, key, status, body)
	}
	if err != nil {
		s.log.Warn("idempotency record", "err", err)
	}
}

// ---- plans (managed by operators) ----

type PlanInput struct {
	Name       string  `json:"name"`
	TemplateID string  `json:"template_id"`
	PoolID     string  `json:"pool_id"`
	NodeID     *string `json:"node_id"`
	VCPUs      int     `json:"vcpus"`
	MemoryMB   int     `json:"memory_mb"`
	DiskGB     int     `json:"disk_gb"`
}

func (s *Service) requireProviderWrite(ctx context.Context, actor *identity.Principal) error {
	if err := s.id.Require(ctx, actor, rbac.ProviderWrite); err != nil {
		return err
	}
	mode, err := s.id.PanelMode(ctx)
	if err != nil {
		return err
	}
	if mode != identity.ModeProvider {
		return &identity.PolicyError{Message: "Provider Mode is off: switch the panel to Hosting Provider Mode in Settings first"}
	}
	return nil
}

func (s *Service) ListPlans(ctx context.Context, actor *identity.Principal) ([]store.ServicePlan, error) {
	if err := s.id.Require(ctx, actor, rbac.ProviderRead); err != nil {
		return nil, err
	}
	return s.store.ListServicePlans(ctx, actor.User.OrganizationID)
}

func (s *Service) CreatePlan(ctx context.Context, actor *identity.Principal, in PlanInput) (*store.ServicePlan, error) {
	if err := s.requireProviderWrite(ctx, actor); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 || strings.ContainsAny(in.Name, "\r\n\t\x00") {
		return nil, verr("name", "must be 1-100 characters on one line")
	}
	org := actor.User.OrganizationID
	tpl, err := s.store.GetVMTemplate(ctx, in.TemplateID)
	if err != nil || tpl.OrganizationID != org {
		return nil, verr("template_id", "unknown template")
	}
	if p, err := s.store.GetIPPool(ctx, in.PoolID); err != nil || p.OrganizationID != org {
		return nil, verr("pool_id", "unknown IP pool")
	}
	if in.NodeID != nil {
		if n, err := s.store.GetNode(ctx, *in.NodeID); err != nil || n.OrganizationID != org {
			return nil, verr("node_id", "unknown node")
		}
	}
	if in.VCPUs < 1 || in.VCPUs > 64 {
		return nil, verr("vcpus", "must be 1-64")
	}
	if in.MemoryMB < 256 || in.MemoryMB > 262144 {
		return nil, verr("memory_mb", "must be 256-262144")
	}
	if in.DiskGB < tpl.MinDiskGB || in.DiskGB > 4096 {
		return nil, verr("disk_gb", fmt.Sprintf("must be %d-4096 for this template", tpl.MinDiskGB))
	}
	p := &store.ServicePlan{ID: auth.NewID(), OrganizationID: org, Name: in.Name, Kind: "vps", TemplateID: in.TemplateID, PoolID: in.PoolID,
		NodeID: in.NodeID, VCPUs: in.VCPUs, MemoryMB: in.MemoryMB, DiskGB: in.DiskGB, Enabled: true}
	if err := s.store.CreateServicePlan(ctx, p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("name", "is already used by another plan")
		}
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "provisioning.plan_create", "service_plan", p.ID, store.OutcomeSuccess,
		map[string]any{"name": p.Name, "vcpus": p.VCPUs, "memory_mb": p.MemoryMB, "disk_gb": p.DiskGB})
	return p, nil
}

func (s *Service) DeletePlan(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireProviderWrite(ctx, actor); err != nil {
		return err
	}
	p, err := s.store.GetServicePlan(ctx, id)
	if err != nil || p.OrganizationID != actor.User.OrganizationID {
		return store.ErrNotFound
	}
	if err := s.store.DeleteServicePlan(ctx, p.ID); err != nil {
		return err
	}
	s.id.Audit(ctx, actor.User, "provisioning.plan_delete", "service_plan", p.ID, store.OutcomeSuccess, map[string]any{"name": p.Name})
	return nil
}

// ---- services (called by billing systems) ----

type ServiceInput struct {
	PlanID     string   `json:"plan_id"`
	ExternalID string   `json:"external_id"`
	Hostname   string   `json:"hostname"`
	SSHKeys    []string `json:"ssh_keys"`
	// CustomerEmail (optional) gives the VPS to an existing Customer Panel account.
	CustomerEmail string `json:"customer_email"`
}

// ServiceView is a provisioned service with its VPS.
type ServiceView struct {
	Service store.ProvisionedService
	VPS     *store.VPS
}

func (s *Service) view(ctx context.Context, x *store.ProvisionedService) *ServiceView {
	v := &ServiceView{Service: *x}
	if x.VPSID != nil {
		v.VPS, _ = s.store.GetVPS(ctx, *x.VPSID)
	}
	return v
}

// pickNode returns the plan's node, or the enabled node with the most free
// memory that still fits the plan.
func (s *Service) pickNode(ctx context.Context, actor *identity.Principal, plan *store.ServicePlan) (string, error) {
	if plan.NodeID != nil {
		return *plan.NodeID, nil
	}
	nodes, err := s.provider.ListNodes(ctx, actor)
	if err != nil {
		return "", err
	}
	best, bestFree := "", -1
	for _, n := range nodes {
		freeMem, freeCPU := n.MemoryMB-n.UsedMemoryMB, n.VCPUs-n.UsedVCPUs
		if n.Node.Enabled && freeMem >= plan.MemoryMB && freeCPU >= plan.VCPUs && freeMem > bestFree {
			best, bestFree = n.Node.ID, freeMem
		}
	}
	if best == "" {
		return "", &identity.PolicyError{Message: "no node has capacity for this plan"}
	}
	return best, nil
}

// Create provisions a service from a plan. A second call with the same
// external id returns the existing service (created is false).
func (s *Service) Create(ctx context.Context, c *Caller, in ServiceInput) (*ServiceView, bool, error) {
	if err := s.id.Require(ctx, c.Principal, rbac.ProviderWrite); err != nil {
		return nil, false, err
	}
	in.ExternalID = strings.TrimSpace(in.ExternalID)
	if !externalRe.MatchString(in.ExternalID) {
		return nil, false, verr("external_id", "must be 1-128 characters: letters, digits, dot, dash, underscore or colon")
	}
	if old, err := s.store.GetProvisionedServiceByExternal(ctx, c.Client.ID, in.ExternalID); err == nil {
		return s.view(ctx, old), false, nil
	}
	plan, err := s.store.GetServicePlan(ctx, in.PlanID)
	if err != nil || plan.OrganizationID != c.Client.OrganizationID {
		return nil, false, verr("plan_id", "unknown plan")
	}
	if !plan.Enabled {
		return nil, false, &identity.PolicyError{Message: "the plan is disabled"}
	}
	in.Hostname = strings.ToLower(strings.TrimSpace(in.Hostname))
	if !agenttask.ValidVMHostname(in.Hostname) {
		return nil, false, verr("hostname", "must be 1-63 lowercase letters, digits or dashes")
	}
	var owner *store.Customer
	if e := strings.ToLower(strings.TrimSpace(in.CustomerEmail)); e != "" {
		o, err := s.store.GetCustomerByEmail(ctx, e)
		if err != nil || o.OrganizationID != c.Client.OrganizationID {
			return nil, false, verr("customer_email", "no Customer Panel account with this e-mail address")
		}
		owner = o
	}
	nodeID, err := s.pickNode(ctx, c.Principal, plan)
	if err != nil {
		return nil, false, err
	}
	svc := &store.ProvisionedService{ID: auth.NewID(), OrganizationID: c.Client.OrganizationID, ClientID: c.Client.ID, PlanID: &plan.ID,
		ExternalID: in.ExternalID, Status: StatusProvisioning, Pending: "create"}
	if err := s.store.CreateProvisionedService(ctx, svc); err != nil {
		if errors.Is(err, store.ErrConflict) { // a concurrent request won
			old, gerr := s.store.GetProvisionedServiceByExternal(ctx, c.Client.ID, in.ExternalID)
			if gerr != nil {
				return nil, false, err
			}
			return s.view(ctx, old), false, nil
		}
		return nil, false, err
	}
	vps, err := s.provider.CreateVPS(ctx, c.Principal, provider.VPSInput{Name: in.Hostname, NodeID: nodeID, TemplateID: plan.TemplateID,
		PoolID: plan.PoolID, VCPUs: plan.VCPUs, MemoryMB: plan.MemoryMB, DiskGB: plan.DiskGB, SSHKeys: in.SSHKeys})
	if err != nil {
		// Nothing was created on a node: forget the service so the billing system can retry.
		if derr := s.store.DeleteProvisionedService(ctx, svc.ID); derr != nil {
			s.log.Warn("forget failed service", "err", derr)
		}
		return nil, false, err
	}
	if owner != nil {
		if err := s.store.SetVPSCustomer(ctx, vps.ID, &owner.ID); err != nil {
			return nil, false, err
		}
	}
	svc.VPSID = &vps.ID
	if err := s.store.UpdateProvisionedService(ctx, svc); err != nil {
		return nil, false, err
	}
	s.id.Audit(ctx, c.Principal.User, "provisioning.service_create", "provisioned_service", svc.ID, store.OutcomeSuccess,
		map[string]any{"external_id": svc.ExternalID, "plan": plan.Name, "vps_id": vps.ID, "ip": vps.IPAddress})
	return s.view(ctx, svc), true, nil
}

func (s *Service) serviceOf(ctx context.Context, c *Caller, id string) (*store.ProvisionedService, error) {
	if err := s.id.Require(ctx, c.Principal, rbac.ProviderRead); err != nil {
		return nil, err
	}
	x, err := s.store.GetProvisionedService(ctx, id)
	if err != nil {
		return nil, err
	}
	if x.ClientID != c.Client.ID {
		return nil, store.ErrNotFound // a client sees only its own services
	}
	if x.Pending != "" {
		if x, err = s.reconcile(ctx, x); err != nil {
			return nil, err
		}
	}
	return x, nil
}

func (s *Service) Get(ctx context.Context, c *Caller, id string) (*ServiceView, error) {
	x, err := s.serviceOf(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, x), nil
}

func (s *Service) GetByExternal(ctx context.Context, c *Caller, externalID string) (*ServiceView, error) {
	if err := s.id.Require(ctx, c.Principal, rbac.ProviderRead); err != nil {
		return nil, err
	}
	x, err := s.store.GetProvisionedServiceByExternal(ctx, c.Client.ID, externalID)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, c, x.ID)
}

// Action suspends, unsuspends or reboots a service.
func (s *Service) Action(ctx context.Context, c *Caller, id, action string) (*ServiceView, error) {
	if err := s.id.Require(ctx, c.Principal, rbac.ProviderWrite); err != nil {
		return nil, err
	}
	x, err := s.serviceOf(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if x.Pending != "" {
		return nil, &identity.PolicyError{Message: "another operation on this service is still running"}
	}
	if x.VPSID == nil {
		return nil, &identity.PolicyError{Message: "the service has no VPS (status " + x.Status + ")"}
	}
	var vmAction string
	switch {
	case action == "suspend" && x.Status == StatusActive:
		vmAction = agenttask.VMShutdown
	case action == "unsuspend" && x.Status == StatusSuspended:
		vmAction = agenttask.VMStart
	case action == "reboot" && x.Status == StatusActive:
		vmAction = agenttask.VMReboot
	case action == "suspend" || action == "unsuspend" || action == "reboot":
		return nil, &identity.PolicyError{Message: fmt.Sprintf("cannot %s a service that is %s", action, x.Status)}
	default:
		return nil, verr("action", "must be suspend, unsuspend or reboot")
	}
	if _, err := s.provider.Action(ctx, c.Principal, *x.VPSID, vmAction); err != nil {
		return nil, err
	}
	x.Pending = action + ":" + x.Status
	if err := s.store.UpdateProvisionedService(ctx, x); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, c.Principal.User, "provisioning.service_"+action, "provisioned_service", x.ID, store.OutcomeSuccess,
		map[string]any{"external_id": x.ExternalID})
	return s.view(ctx, x), nil
}

// Terminate destroys the VPS and its disk; the service record stays as terminated.
func (s *Service) Terminate(ctx context.Context, c *Caller, id string) (*ServiceView, error) {
	if err := s.id.Require(ctx, c.Principal, rbac.ProviderWrite); err != nil {
		return nil, err
	}
	x, err := s.serviceOf(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if x.Status == StatusTerminated || x.Status == StatusTerminating {
		return s.view(ctx, x), nil
	}
	if x.Pending != "" {
		return nil, &identity.PolicyError{Message: "another operation on this service is still running"}
	}
	prev := x.Status
	if x.VPSID != nil {
		v, err := s.store.GetVPS(ctx, *x.VPSID)
		if err == nil {
			if _, err := s.provider.Delete(ctx, c.Principal, v.ID, v.Name); err != nil {
				return nil, err
			}
			x.Status, x.Pending = StatusTerminating, "terminate:"+prev
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if x.Pending == "" {
		x.Status = StatusTerminated
		s.notify(ctx, x, "service.terminated")
	}
	if err := s.store.UpdateProvisionedService(ctx, x); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, c.Principal.User, "provisioning.service_terminate", "provisioned_service", x.ID, store.OutcomeSuccess,
		map[string]any{"external_id": x.ExternalID})
	return s.view(ctx, x), nil
}

// ListServices is for operators (dashboard).
func (s *Service) ListServices(ctx context.Context, actor *identity.Principal) ([]ServiceView, error) {
	if err := s.id.Require(ctx, actor, rbac.ProviderRead); err != nil {
		return nil, err
	}
	list, err := s.store.ListProvisionedServices(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceView, 0, len(list))
	for i := range list {
		out = append(out, *s.view(ctx, &list[i]))
	}
	return out, nil
}

// ---- reconciliation ----

// reconcile applies the outcome of the pending operation, once the VPS's own
// task has finished, and sends the matching webhook.
func (s *Service) reconcile(ctx context.Context, x *store.ProvisionedService) (*store.ProvisionedService, error) {
	op, prev, _ := strings.Cut(x.Pending, ":")
	var v *store.VPS
	var err error
	if x.VPSID != nil {
		if v, err = s.provider.Refresh(ctx, *x.VPSID); err != nil {
			return x, err
		}
	}
	if v != nil && v.PendingTaskID != nil {
		return x, nil // still running on the node
	}
	event := ""
	switch op {
	case "create":
		switch {
		case v == nil:
			x.Status, x.LastError, event = StatusFailed, "the VPS disappeared", "service.failed"
		case v.State == provider.StateRunning:
			x.Status, x.LastError, event = StatusActive, "", "service.active"
		case v.State == provider.StateError:
			x.Status, x.LastError, event = StatusFailed, v.LastError, "service.failed"
		case v.State == provider.StateCreating:
			return x, nil
		default:
			x.Status, event = StatusActive, "service.active"
		}
	case "suspend":
		if v != nil && v.State == provider.StateRunning {
			// The guest ignored the graceful shutdown: force it off.
			if err := s.forceStop(ctx, x, v); err != nil {
				x.Status, x.LastError = prev, "suspend failed: "+err.Error()
				break
			}
			return x, nil
		}
		x.Status, x.LastError, event = StatusSuspended, "", "service.suspended"
		if v != nil && v.LastError != "" {
			x.Status, x.LastError, event = prev, v.LastError, ""
		}
	case "suspend-force":
		x.Status, event = StatusSuspended, "service.suspended"
		if v != nil && v.State == provider.StateRunning {
			x.Status, x.LastError, event = StatusActive, "suspend failed: the VPS is still running", ""
		}
	case "unsuspend":
		x.Status, event = StatusActive, "service.unsuspended"
		if v == nil || v.State != provider.StateRunning {
			x.Status, event = prev, ""
			if v != nil {
				x.LastError = v.LastError
			}
		}
	case "reboot":
		x.Status = StatusActive
	case "terminate":
		if v == nil {
			x.Status, x.VPSID, event = StatusTerminated, nil, "service.terminated"
		} else {
			x.Status, x.LastError = prev, v.LastError // delete failed on the node
		}
	}
	x.Pending = ""
	if err := s.store.UpdateProvisionedService(ctx, x); err != nil {
		return x, err
	}
	if event != "" {
		s.notify(ctx, x, event)
	}
	return x, nil
}

// forceStop powers off a VPS that ignored a graceful shutdown during suspend.
func (s *Service) forceStop(ctx context.Context, x *store.ProvisionedService, v *store.VPS) error {
	c, err := s.store.GetAPIClient(ctx, x.ClientID)
	if err != nil {
		return err
	}
	var granted []string
	for _, sc := range c.Scopes {
		granted = append(granted, scopePermissions[sc]...)
	}
	p, err := s.id.ClientPrincipal(ctx, c.CreatedBy, c.ID, granted)
	if err != nil {
		return err
	}
	if _, err := s.provider.Action(ctx, p, v.ID, agenttask.VMStop); err != nil {
		return err
	}
	x.Pending = "suspend-force:" + StatusActive
	return s.store.UpdateProvisionedService(ctx, x)
}

// Reconcile checks every service with a pending operation once.
func (s *Service) Reconcile(ctx context.Context) {
	list, err := s.store.ListPendingServices(ctx, 100)
	if err != nil {
		s.log.Warn("pending services", "err", err)
		return
	}
	for i := range list {
		if _, err := s.reconcile(ctx, &list[i]); err != nil {
			s.log.Warn("reconcile service", "id", list[i].ID, "err", err)
		}
	}
}

// Run reconciles every interval and prunes nonces and idempotency records.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastPrune time.Time
	for {
		now := s.id.Now()
		if s.lessor == nil || s.lessor.Hold(ctx, "provisioning", now, 3*every) {
			s.Reconcile(ctx)
			if now.Sub(lastPrune) >= 10*time.Minute {
				_, _ = s.store.PruneNonces(ctx, now)
				_, _ = s.store.PruneIdempotency(ctx, now.Add(-idempotencyTTL))
				lastPrune = now
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- webhooks ----

type webhookPayload struct {
	EventID   string `json:"event_id"`
	ClientID  string `json:"client_id"`
	ServiceID string `json:"service_id"`
	Event     string `json:"event"`
	At        string `json:"at"`
}

// WebhookBody is what a billing system receives.
type WebhookBody struct {
	Event      string `json:"event"`
	EventID    string `json:"event_id"`
	ServiceID  string `json:"service_id"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	IPAddress  string `json:"ip_address,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	Error      string `json:"error,omitempty"`
	At         string `json:"at"`
}

func (s *Service) notify(ctx context.Context, x *store.ProvisionedService, event string) {
	c, err := s.store.GetAPIClient(ctx, x.ClientID)
	if err != nil || c.WebhookURL == "" || s.queue == nil {
		return
	}
	p := webhookPayload{EventID: auth.NewID(), ClientID: c.ID, ServiceID: x.ID, Event: event, At: s.id.Now().UTC().Format(time.RFC3339)}
	if _, err := s.queue.Enqueue(ctx, webhookJob, p, 0); err != nil {
		s.log.Warn("queue webhook", "err", err)
	}
}

type webhookHandler struct{ s *Service }

func (h webhookHandler) Run(ctx context.Context, j jobs.Job) error {
	s := h.s
	var p webhookPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent(err)
	}
	c, err := s.store.GetAPIClient(ctx, p.ClientID)
	if errors.Is(err, store.ErrNotFound) {
		return jobs.Permanent(errors.New("the API client was deleted"))
	}
	if err != nil {
		return err
	}
	if c.WebhookURL == "" || !c.Enabled {
		return jobs.Permanent(errors.New("the client has no webhook or is disabled"))
	}
	x, err := s.store.GetProvisionedService(ctx, p.ServiceID)
	if err != nil {
		return jobs.Permanent(err)
	}
	secret, err := s.keys.Decrypt(c.SecretEnc, clientAAD(c.ID))
	if err != nil {
		return jobs.Permanent(errors.New("cannot decrypt the client secret"))
	}
	b := WebhookBody{Event: p.Event, EventID: p.EventID, ServiceID: x.ID, ExternalID: x.ExternalID, Status: x.Status, Error: x.LastError, At: p.At}
	if x.VPSID != nil {
		if v, err := s.store.GetVPS(ctx, *x.VPSID); err == nil {
			b.IPAddress, b.Hostname = v.IPAddress, v.Name
		}
	}
	body, _ := json.Marshal(b)
	return s.sender(ctx, c.WebhookURL, string(secret), p.Event, p.EventID, body)
}

func (h webhookHandler) GiveUp(_ context.Context, j jobs.Job, err error) {
	h.s.log.Warn("provisioning webhook given up", "job", j.ID, "err", err)
}
