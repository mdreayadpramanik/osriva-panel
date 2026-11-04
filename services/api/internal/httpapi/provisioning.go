package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provisioning"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/reqmeta"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type callerKey struct{}

func callerFrom(ctx context.Context) *provisioning.Caller {
	c, _ := ctx.Value(callerKey{}).(*provisioning.Caller)
	return c
}

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

// requireSignature authenticates /provisioning requests by their HMAC
// signature (docs/adr/0014-provisioning-api.md); cookies play no part.
func (s *Server) requireSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.provisioning == nil {
			s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the provisioning API is not available", "")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			s.writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large", "")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		meta := reqmeta.From(r.Context())
		c, err := s.provisioning.Verify(r.Context(), provisioning.SignedRequest{
			Method: r.Method, PathQuery: r.URL.RequestURI(), KeyID: r.Header.Get("X-Osriva-Key"),
			Timestamp: r.Header.Get("X-Osriva-Timestamp"), Nonce: r.Header.Get("X-Osriva-Nonce"),
			Signature: r.Header.Get("X-Osriva-Signature"), Body: body, RemoteIP: meta.IP,
		})
		switch {
		case errors.Is(err, provisioning.ErrReplayed):
			s.writeError(w, r, http.StatusUnauthorized, "replayed_request", err.Error(), "")
			return
		case errors.Is(err, provisioning.ErrBadSignature):
			s.writeError(w, r, http.StatusUnauthorized, "invalid_signature", err.Error(), "")
			return
		case err != nil:
			s.fail(w, r, err)
			return
		}
		if !s.clientLimiter.Allow(c.Client.ID) {
			w.Header().Set("Retry-After", "1")
			s.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests for this API client", "")
			return
		}
		meta.APIClient = c.Client.ID
		ctx := context.WithValue(reqmeta.With(r.Context(), meta), callerKey{}, c)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// idempotent makes POST and DELETE replay-safe with an Idempotency-Key.
func (s *Server) idempotent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}
		c := callerFrom(r.Context())
		key := r.Header.Get("Idempotency-Key")
		if !idempotencyKeyRe.MatchString(key) {
			s.writeError(w, r, http.StatusBadRequest, "idempotency_key_required", "POST and DELETE need an Idempotency-Key header (8-128 characters)", "")
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := provisioning.IdempotencyHash(r.Method, r.URL.RequestURI(), body)
		old, err := s.provisioning.Begin(r.Context(), c.Client.ID, key, hash)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if old != nil {
			switch {
			case old.RequestHash != hash:
				s.writeError(w, r, http.StatusUnprocessableEntity, "idempotency_mismatch", "this Idempotency-Key was used for a different request", "")
			case old.StatusCode == 0:
				s.writeError(w, r, http.StatusConflict, "idempotency_in_progress", "the first request with this Idempotency-Key is still running", "")
			default:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(old.StatusCode)
				_, _ = w.Write(old.Response)
			}
			return
		}
		rec := &recorder{ResponseWriter: w}
		defer func() {
			status := rec.status
			if status == 0 {
				status = http.StatusInternalServerError
			}
			s.provisioning.Finish(r.Context(), c.Client.ID, key, status, rec.buf.Bytes())
		}()
		next.ServeHTTP(rec, r)
	})
}

type planDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	TemplateID string  `json:"template_id"`
	PoolID     string  `json:"pool_id"`
	NodeID     *string `json:"node_id"`
	VCPUs      int     `json:"vcpus"`
	MemoryMB   int     `json:"memory_mb"`
	DiskGB     int     `json:"disk_gb"`
	Enabled    bool    `json:"enabled"`
}

func planConv(p *store.ServicePlan) planDTO {
	return planDTO{ID: p.ID, Name: p.Name, Kind: p.Kind, TemplateID: p.TemplateID, PoolID: p.PoolID, NodeID: p.NodeID, VCPUs: p.VCPUs,
		MemoryMB: p.MemoryMB, DiskGB: p.DiskGB, Enabled: p.Enabled}
}

type serviceDTO struct {
	ID         string  `json:"id"`
	ExternalID string  `json:"external_id"`
	PlanID     *string `json:"plan_id"`
	ClientID   string  `json:"client_id"`
	Status     string  `json:"status"`
	Pending    string  `json:"pending"`
	VPSID      *string `json:"vps_id"`
	Hostname   string  `json:"hostname"`
	IPAddress  string  `json:"ip_address"`
	LastError  string  `json:"last_error"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

func serviceConv(v *provisioning.ServiceView) serviceDTO {
	x := v.Service
	op, _, _ := cutColon(x.Pending)
	d := serviceDTO{ID: x.ID, ExternalID: x.ExternalID, PlanID: x.PlanID, ClientID: x.ClientID, Status: x.Status, Pending: op, VPSID: x.VPSID,
		LastError: x.LastError, CreatedAt: ts(x.CreatedAt), UpdatedAt: ts(x.UpdatedAt)}
	if v.VPS != nil {
		d.Hostname, d.IPAddress = v.VPS.Name, v.VPS.IPAddress
	}
	return d
}

type apiClientDTO struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	KeyID        string   `json:"key_id"`
	Scopes       []string `json:"scopes"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	WebhookURL   string   `json:"webhook_url"`
	Enabled      bool     `json:"enabled"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
	LastUsedAt   *string  `json:"last_used_at"`
}

func clientConv(c *store.APIClient) apiClientDTO {
	return apiClientDTO{ID: c.ID, Name: c.Name, KeyID: c.KeyID, Scopes: c.Scopes, AllowedCIDRs: c.AllowedCIDRs, WebhookURL: c.WebhookURL,
		Enabled: c.Enabled, CreatedBy: c.CreatedBy, CreatedAt: ts(c.CreatedAt), LastUsedAt: tsPtr(c.LastUsedAt)}
}

// signedRoutes is the API for billing systems (signed requests only).
func (s *Server) signedRoutes(r chi.Router) {
	r.Use(s.requireSignature, s.idempotent)
	r.Get("/plans", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provisioning.ListPlans(r.Context(), callerFrom(r.Context()).Principal)
		list(s, w, r, "plans", items, err, planConv)
	})
	r.Get("/services", func(w http.ResponseWriter, r *http.Request) {
		ext := r.URL.Query().Get("external_id")
		if ext == "" {
			s.writeError(w, r, http.StatusBadRequest, "external_id_required", "look services up by ?external_id=", "")
			return
		}
		v, err := s.provisioning.GetByExternal(r.Context(), callerFrom(r.Context()), ext)
		one(s, w, r, http.StatusOK, v, err, serviceConv)
	})
	r.Post("/services", func(w http.ResponseWriter, r *http.Request) {
		var in provisioning.ServiceInput
		if !s.decode(w, r, &in) {
			return
		}
		v, created, err := s.provisioning.Create(r.Context(), callerFrom(r.Context()), in)
		code := http.StatusOK
		if created {
			code = http.StatusAccepted
		}
		one(s, w, r, code, v, err, serviceConv)
	})
	r.Get("/services/{svcId}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.provisioning.Get(r.Context(), callerFrom(r.Context()), chi.URLParam(r, "svcId"))
		one(s, w, r, http.StatusOK, v, err, serviceConv)
	})
	r.Post("/services/{svcId}/actions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		v, err := s.provisioning.Action(r.Context(), callerFrom(r.Context()), chi.URLParam(r, "svcId"), in.Action)
		one(s, w, r, http.StatusAccepted, v, err, serviceConv)
	})
	r.Delete("/services/{svcId}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.provisioning.Terminate(r.Context(), callerFrom(r.Context()), chi.URLParam(r, "svcId"))
		one(s, w, r, http.StatusAccepted, v, err, serviceConv)
	})
}

// provisioningAdminRoutes are the session-authenticated management endpoints.
func (s *Server) provisioningAdminRoutes(r chi.Router) {
	p := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.provisioning == nil {
				s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the provisioning API is not available", "")
				return
			}
			h(w, r)
		}
	}
	r.Get("/api-clients", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provisioning.ListClients(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "clients", items, err, clientConv)
	}))
	r.Post("/api-clients", p(func(w http.ResponseWriter, r *http.Request) {
		var in provisioning.ClientInput
		if !s.decode(w, r, &in) {
			return
		}
		c, err := s.provisioning.CreateClient(r.Context(), principalFrom(r.Context()), in)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// The secret is shown once; the panel stores it encrypted.
		writeJSON(w, http.StatusCreated, map[string]any{"client": clientConv(&c.APIClient), "secret": c.Secret})
	}))
	r.Patch("/api-clients/{clientId}", p(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		c, err := s.provisioning.SetClientEnabled(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "clientId"), in.Enabled)
		one(s, w, r, http.StatusOK, c, err, clientConv)
	}))
	r.Delete("/api-clients/{clientId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provisioning.DeleteClient(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "clientId")))
	}))
	r.Get("/provider/plans", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provisioning.ListPlans(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "plans", items, err, planConv)
	}))
	r.Post("/provider/plans", p(func(w http.ResponseWriter, r *http.Request) {
		var in provisioning.PlanInput
		if !s.decode(w, r, &in) {
			return
		}
		pl, err := s.provisioning.CreatePlan(r.Context(), principalFrom(r.Context()), in)
		one(s, w, r, http.StatusCreated, pl, err, planConv)
	}))
	r.Delete("/provider/plans/{planId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provisioning.DeletePlan(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "planId")))
	}))
	r.Get("/provider/provisioned-services", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provisioning.ListServices(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "services", items, err, serviceConv)
	}))
}
