package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/compat"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type serverDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`     // pending | active | revoked
	Connection   string  `json:"connection"` // pending | online | offline | revoked (derived from last report)
	Hostname     string  `json:"hostname"`
	OSFamily     string  `json:"os_family"`
	OSName       string  `json:"os_name"`
	OSVersion    string  `json:"os_version"`
	Kernel       string  `json:"kernel"`
	Arch         string  `json:"arch"`
	AgentVersion string  `json:"agent_version"`
	LastSeenAt   *string `json:"last_seen_at"`
	LastSeenIP   string  `json:"last_seen_ip"`
	EnrolledAt   *string `json:"enrolled_at"`
	CreatedAt    string  `json:"created_at"`
}

func toServerDTO(v fleet.ServerView) serverDTO {
	x, f := v.Server, v.Server.Facts
	return serverDTO{ID: x.ID, Name: x.Name, Status: string(x.Status), Connection: string(v.Connection),
		Hostname: f.Hostname, OSFamily: f.OSFamily, OSName: f.OSName, OSVersion: f.OSVersion, Kernel: f.Kernel, Arch: f.Arch,
		AgentVersion: f.AgentVersion, LastSeenAt: tsPtr(x.LastSeenAt), LastSeenIP: x.LastSeenIP,
		EnrolledAt: tsPtr(x.EnrolledAt), CreatedAt: ts(x.CreatedAt)}
}

type enrollmentDTO struct {
	Token         string `json:"token"`
	ExpiresAt     string `json:"expires_at"`
	PanelURL      string `json:"panel_url"`
	CAFingerprint string `json:"ca_fingerprint"`
	Command       string `json:"command"`
	// One-line installers; empty when this panel has no agent builds for that OS family.
	InstallUnix    string `json:"install_unix"`
	InstallWindows string `json:"install_windows"`
}

func (s *Server) toEnrollmentDTO(e *fleet.Enrollment) enrollmentDTO {
	d := enrollmentDTO{Token: e.Token, ExpiresAt: ts(e.ExpiresAt), PanelURL: e.PanelURL, CAFingerprint: e.CAFingerprint, Command: e.Command}
	if s.dist != nil && s.dist.HasFamily("unix") {
		d.InstallUnix = s.dist.UnixCommand(e.Token, e.CAFingerprint)
	}
	if s.dist != nil && s.dist.HasFamily("windows") {
		d.InstallWindows = s.dist.WindowsCommand(e.Token, e.CAFingerprint)
	}
	return d
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.fleet.List(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]serverDTO, len(list))
	for i, v := range list {
		out[i] = toServerDTO(v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

func (s *Server) handleAgentEndpointInfo(w http.ResponseWriter, r *http.Request) {
	if err := s.id.Require(r.Context(), principalFrom(r.Context()), rbac.ServersRead); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.fleet.Configured(), "agent_endpoint": s.fleet.AgentEndpoint(), "ca_fingerprint": s.fleet.CAFingerprint(),
	})
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	v, enr, err := s.fleet.CreateServer(r.Context(), principalFrom(r.Context()), in.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server": toServerDTO(*v), "enrollment": s.toEnrollmentDTO(enr)})
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	d, err := s.fleet.Get(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	caps := d.Capabilities
	if caps == nil {
		caps = []compat.Capability{}
	}
	writeJSON(w, http.StatusOK, struct {
		Server       serverDTO            `json:"server"`
		Inventory    *inventory.Inventory `json:"inventory"`
		Capabilities []compat.Capability  `json:"capabilities"`
		ReceivedAt   *string              `json:"inventory_received_at"`
	}{toServerDTO(d.ServerView), d.Inventory, caps, tsPtr(d.ReceivedAt)})
}

func (s *Server) handleRegenerateEnrollment(w http.ResponseWriter, r *http.Request) {
	enr, err := s.fleet.RegenerateEnrollment(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enrollment": s.toEnrollmentDTO(enr)})
}

func (s *Server) handleRevokeServer(w http.ResponseWriter, r *http.Request) {
	if err := s.fleet.Revoke(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	var in agentproto.EnrollRequest
	if !s.decode(w, r, &in) {
		return
	}
	resp, err := s.fleet.Enroll(r.Context(), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- mutual-TLS agent listener ----

type agentKey struct{}

// NewAgentHandler serves the agent endpoints. It must be mounted on a TLS
// listener that requires and verifies client certificates (fleet.AgentTLSConfig).
// Agents connect directly (no reverse proxy), so forwarded headers are ignored.
func NewAgentHandler(o Options) http.Handler {
	s := &Server{id: o.Identity, fleet: o.Fleet, log: o.Log}
	if s.log == nil {
		s.log = slog.Default()
	}
	r := chi.NewRouter()
	r.Use(s.withMeta, s.recoverer, s.accessLog, s.requireAgent)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint", "")
	})
	r.Post(agentproto.ReportPath, s.handleAgentReport)
	r.Post(agentproto.RenewPath, s.handleAgentRenew)
	return r
}

func agentFrom(ctx context.Context) *store.Server {
	x, _ := ctx.Value(agentKey{}).(*store.Server)
	return x
}

func (s *Server) requireAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			s.writeError(w, r, http.StatusUnauthorized, "client_certificate_required", "a verified agent client certificate is required", "")
			return
		}
		x, err := s.fleet.AuthenticateAgent(r.Context(), r.TLS.PeerCertificates[0])
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), agentKey{}, x)))
	})
}

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	var in agentproto.ReportRequest
	if !s.decode(w, r, &in) {
		return
	}
	resp, err := s.fleet.Report(r.Context(), agentFrom(r.Context()), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAgentRenew(w http.ResponseWriter, r *http.Request) {
	var in agentproto.RenewRequest
	if !s.decode(w, r, &in) {
		return
	}
	resp, err := s.fleet.Renew(r.Context(), agentFrom(r.Context()), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAgentCA serves the public CA certificate so an agent can verify the
// pinned fingerprint before it reveals its enrolment token.
func (s *Server) handleAgentCA(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, agentproto.CAResponse{CAPEM: string(s.fleet.CA().CertPEM), Fingerprint: s.fleet.CAFingerprint()})
}
