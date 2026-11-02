package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type proxyDTO struct {
	Enabled     bool   `json:"enabled"`
	HTTPPort    int    `json:"http_port"`
	HTTPSPort   int    `json:"https_port"`
	ACMEEmail   string `json:"acme_email"`
	ACMEStaging bool   `json:"acme_staging"`
}

type dnsDTO struct {
	Addresses  []string `json:"addresses"`
	PointsHere bool     `json:"points_here"`
	Error      string   `json:"error,omitempty"`
}

type siteDTO struct {
	ID           string  `json:"id"`
	Domain       string  `json:"domain"`
	UpstreamPort int     `json:"upstream_port"`
	TLSMode      string  `json:"tls_mode"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	DNS          *dnsDTO `json:"dns,omitempty"`
}

func toProxyDTO(p *store.ProxySettings) proxyDTO {
	return proxyDTO{Enabled: p.Enabled, HTTPPort: p.HTTPPort, HTTPSPort: p.HTTPSPort, ACMEEmail: p.ACMEEmail, ACMEStaging: p.ACMEStaging}
}

func toSiteDTO(x *store.Site) siteDTO {
	return siteDTO{ID: x.ID, Domain: x.Domain, UpstreamPort: x.UpstreamPort, TLSMode: x.TLSMode, CreatedAt: ts(x.CreatedAt), UpdatedAt: ts(x.UpdatedAt)}
}

// withTask wraps a response with the agent task that applies the change (or null).
func withTask(body map[string]any, t *store.Task) map[string]any {
	body["task"] = nil
	if t != nil {
		body["task"] = toTaskDTO(t)
	}
	return body
}

func (s *Server) handleWebsites(w http.ResponseWriter, r *http.Request) {
	v, err := s.fleet.Websites(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sites := make([]siteDTO, len(v.Sites))
	for i := range v.Sites {
		sites[i] = toSiteDTO(&v.Sites[i].Site)
		d := v.Sites[i].DNS
		sites[i].DNS = &dnsDTO{Addresses: d.Addresses, PointsHere: d.PointsHere, Error: d.Error}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxy": toProxyDTO(&v.Settings), "server_ip": v.ServerIP, "sites": sites})
}

func (s *Server) handleSaveProxy(w http.ResponseWriter, r *http.Request) {
	var in fleet.ProxyInput
	if !s.decode(w, r, &in) {
		return
	}
	p, t, err := s.fleet.SaveProxy(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, withTask(map[string]any{"proxy": toProxyDTO(p)}, t))
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var in fleet.SiteInput
	if !s.decode(w, r, &in) {
		return
	}
	x, t, err := s.fleet.CreateSite(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, withTask(map[string]any{"site": toSiteDTO(x)}, t))
}

func (s *Server) handleUpdateSite(w http.ResponseWriter, r *http.Request) {
	var in fleet.SiteInput
	if !s.decode(w, r, &in) {
		return
	}
	x, t, err := s.fleet.UpdateSite(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "siteId"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, withTask(map[string]any{"site": toSiteDTO(x)}, t))
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	t, err := s.fleet.DeleteSite(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "siteId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, withTask(map[string]any{}, t))
}
