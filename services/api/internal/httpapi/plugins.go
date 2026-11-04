package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/plugins"
)

type pluginDTO struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Publisher   string   `json:"publisher"`
	Description string   `json:"description"`
	Kinds       []string `json:"kinds"`
	KeyID       string   `json:"key_id"`
	SHA256      string   `json:"sha256"`
	Valid       bool     `json:"valid"`
	Reason      string   `json:"reason,omitempty"`
	Enabled     bool     `json:"enabled"`
	Approved    string   `json:"approved_sha256,omitempty"`
	Runnable    bool     `json:"runnable"`
}

func pluginConv(v *plugins.View) pluginDTO {
	d := pluginDTO{Name: v.Entry.Name, KeyID: v.Entry.KeyID, SHA256: v.Entry.SHA256, Valid: v.Entry.Valid, Reason: v.Entry.Reason, Runnable: v.Runnable, Kinds: []string{}}
	if m := v.Entry.Manifest; m != nil {
		d.Version, d.Publisher, d.Description, d.Kinds = m.Version, m.Publisher, m.Description, m.Kinds
	}
	if v.Approval != nil {
		d.Enabled, d.Approved = v.Approval.Enabled, v.Approval.ApprovedSHA256
	}
	return d
}

func (s *Server) pluginRoutes(r chi.Router) {
	r = r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.plugins == nil {
				s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "plugins are not available", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/plugins", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.plugins.List(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]pluginDTO, len(items))
		for i := range items {
			out[i] = pluginConv(&items[i])
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": s.plugins.Enabled(), "plugins": out})
	})
	r.Post("/plugins/rescan", func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.plugins.Rescan(r.Context(), principalFrom(r.Context())))
	})
	r.Put("/plugins/{name}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		v, err := s.plugins.SetEnabled(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "name"), in.Enabled)
		one(s, w, r, http.StatusOK, v, err, pluginConv)
	})
}
