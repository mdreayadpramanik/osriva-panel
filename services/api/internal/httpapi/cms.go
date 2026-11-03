package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
)

type cmsDTO struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	HostPort      int     `json:"host_port"`
	AdminUser     string  `json:"admin_user"`
	AdminEmail    string  `json:"admin_email"`
	Domain        string  `json:"domain"`
	InstallTaskID *string `json:"install_task_id"`
	InstallStatus string  `json:"install_status"`
	InstallError  string  `json:"install_error"`
	CreatedAt     string  `json:"created_at"`
}

func toCMSDTO(v *fleet.CMSView) cmsDTO {
	return cmsDTO{ID: v.ID, Kind: v.Kind, Name: v.Name, Title: v.Title, URL: v.URL, HostPort: v.HostPort, AdminUser: v.AdminUser,
		AdminEmail: v.AdminEmail, Domain: v.Domain, InstallTaskID: v.InstallTaskID, InstallStatus: string(v.InstallStatus),
		InstallError: v.InstallError, CreatedAt: ts(v.CreatedAt)}
}

func (s *Server) handleListCMS(w http.ResponseWriter, r *http.Request) {
	list, err := s.fleet.ListCMS(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]cmsDTO, len(list))
	for i := range list {
		out[i] = toCMSDTO(&list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func (s *Server) handleGetCMS(w http.ResponseWriter, r *http.Request) {
	v, err := s.fleet.GetCMS(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "cmsId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCMSDTO(v))
}

func (s *Server) handleInstallWordPress(w http.ResponseWriter, r *http.Request) {
	var in fleet.WordPressInput
	if !s.decode(w, r, &in) {
		return
	}
	c, err := s.fleet.InstallWordPress(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The generated admin password is shown once and never stored by the panel.
	writeJSON(w, http.StatusCreated, map[string]any{"instance": toCMSDTO(&c.CMSView), "admin_password": c.AdminPassword, "task": toTaskDTO(c.Task)})
}

func (s *Server) handleDeleteCMS(w http.ResponseWriter, r *http.Request) {
	t, err := s.fleet.DeleteCMS(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "cmsId"), r.URL.Query().Get("delete_data") == "true")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toTaskDTO(t))
}
