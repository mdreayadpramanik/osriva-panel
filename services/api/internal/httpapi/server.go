// Package httpapi exposes the Core API over HTTP (REST, /api/v1).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/agentdist"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/ratelimit"
	"github.com/mdreayadpramanik/osriva-panel/services/api/openapi"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type Options struct {
	Identity       *identity.Service
	Fleet          *fleet.Service
	AgentDist      *agentdist.Dist
	DB             Pinger
	Log            *slog.Logger
	Version        string
	CookieSecure   bool
	SessionTTL     time.Duration
	TrustedProxies []netip.Prefix
	// APILimiter limits all /api requests per client IP; a default is used when nil.
	APILimiter *ratelimit.KeyedLimiter
}

type Server struct {
	id             *identity.Service
	fleet          *fleet.Service
	dist           *agentdist.Dist
	db             Pinger
	log            *slog.Logger
	version        string
	cookieSecure   bool
	sessionTTL     time.Duration
	trustedProxies []netip.Prefix
	apiLimiter     *ratelimit.KeyedLimiter
}

func New(o Options) http.Handler {
	s := &Server{
		id: o.Identity, fleet: o.Fleet, dist: o.AgentDist, db: o.DB, log: o.Log, version: o.Version, cookieSecure: o.CookieSecure,
		sessionTTL: o.SessionTTL, trustedProxies: o.TrustedProxies, apiLimiter: o.APILimiter,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.apiLimiter == nil {
		s.apiLimiter = ratelimit.NewKeyed(20, 60)
	}
	if s.dist == nil {
		s.dist, _ = agentdist.Load("", "") // no builds: downloads answer 404
	}
	if s.sessionTTL == 0 {
		s.sessionTTL = 12 * time.Hour
	}
	return s.routes()
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.withMeta, s.recoverer, s.accessLog, s.securityHeaders)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint", "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
	})

	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.apiRateLimit)
		r.Get("/meta", s.handleMeta)
		r.Get("/openapi.yaml", s.handleOpenAPI)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/agent/ca", s.handleAgentCA)
		r.Get("/agent/downloads", s.handleAgentDownloads)
		r.Get("/agent/download/{os}/{arch}", s.handleAgentDownload)
		r.Get("/agent/download/{os}/{arch}/sha256", s.handleAgentDownloadSHA)
		r.Get("/agent/install.sh", s.handleAgentInstallSh)
		r.Get("/agent/install.ps1", s.handleAgentInstallPs1)
		r.Post("/agent/enroll", s.handleAgentEnroll)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/logout", s.handleLogout)
			r.Post("/auth/reauth", s.handleReauth)
			r.Post("/auth/password", s.handleChangePassword)
			r.Get("/auth/sessions", s.handleListSessions)
			r.Delete("/auth/sessions/{id}", s.handleRevokeSession)
			r.Post("/auth/sessions/revoke-others", s.handleRevokeOtherSessions)
			r.Get("/auth/login-history", s.handleLoginHistory)
			r.Post("/auth/mfa/totp/setup", s.handleTOTPSetup)
			r.Post("/auth/mfa/totp/enable", s.handleTOTPEnable)
			r.Post("/auth/mfa/totp/disable", s.handleTOTPDisable)

			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Get("/users/{id}", s.handleGetUser)
			r.Patch("/users/{id}", s.handleUpdateUser)
			r.Get("/users/{id}/sessions", s.handleUserSessions)
			r.Post("/users/{id}/sessions/revoke", s.handleRevokeUserSessions)

			r.Get("/roles", s.handleListRoles)
			r.Get("/audit-logs", s.handleListAudit)
			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings/panel-mode", s.handleSetPanelMode)

			r.Get("/servers", s.handleListServers)
			r.Post("/servers", s.handleCreateServer)
			r.Get("/servers/agent-endpoint", s.handleAgentEndpointInfo)
			r.Get("/servers/{id}", s.handleGetServer)
			r.Post("/servers/{id}/enrollment-token", s.handleRegenerateEnrollment)
			r.Post("/servers/{id}/revoke", s.handleRevokeServer)
			r.Get("/servers/{id}/metrics", s.handleServerMetrics)
			r.Get("/servers/{id}/tasks", s.handleListTasks)
			r.Post("/servers/{id}/tasks", s.handleQueueTask)
			r.Get("/servers/{id}/tasks/{taskId}", s.handleGetTask)
			r.Get("/servers/{id}/apps", s.handleListApps)
			r.Post("/servers/{id}/apps", s.handleCreateApp)
			r.Get("/servers/{id}/apps/{appId}", s.handleGetApp)
			r.Put("/servers/{id}/apps/{appId}", s.handleUpdateApp)
			r.Delete("/servers/{id}/apps/{appId}", s.handleDeleteApp)
			r.Post("/servers/{id}/apps/{appId}/deployments", s.handleDeployApp)
			r.Get("/servers/{id}/apps/{appId}/deployments/{depId}", s.handleGetDeployment)
			r.Post("/servers/{id}/apps/{appId}/deployments/{depId}/rollback", s.handleRollbackApp)
			r.Get("/servers/{id}/websites", s.handleWebsites)
			r.Put("/servers/{id}/proxy", s.handleSaveProxy)
			r.Post("/servers/{id}/sites", s.handleCreateSite)
			r.Put("/servers/{id}/sites/{siteId}", s.handleUpdateSite)
			r.Delete("/servers/{id}/sites/{siteId}", s.handleDeleteSite)
		})
	})
	return r
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.log.Warn("readiness check failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "checks": map[string]string{"database": "unreachable"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "checks": map[string]string{"database": "ok"}})
}

func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name": "OSRIVA Panel", "tagline": "One Platform. Every Server. Total Control.",
		"version": s.version, "api_version": "v1", "license": "AGPL-3.0-only",
	})
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(openapi.Spec)
}
