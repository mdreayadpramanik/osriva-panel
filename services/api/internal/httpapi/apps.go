package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
)

type deploymentDTO struct {
	ID         string                     `json:"id"`
	Kind       string                     `json:"kind"`
	RollbackOf *string                    `json:"rollback_of"`
	Image      string                     `json:"image"`
	GitRef     string                     `json:"git_ref"`
	TaskID     *string                    `json:"task_id"`
	CreatedBy  *string                    `json:"created_by"`
	CreatedAt  string                     `json:"created_at"`
	Status     string                     `json:"status"`
	StartedAt  *string                    `json:"started_at"`
	FinishedAt *string                    `json:"finished_at"`
	Error      string                     `json:"error"`
	Result     *agenttask.AppDeployResult `json:"result"`
}

type appDTO struct {
	ID            string          `json:"id"`
	ServerID      string          `json:"server_id"`
	Name          string          `json:"name"`
	GitURL        string          `json:"git_url"`
	GitRef        string          `json:"git_ref"`
	BuildMode     string          `json:"build_mode"`
	BuildContext  string          `json:"build_context"`
	Dockerfile    string          `json:"dockerfile"`
	ContainerPort int             `json:"container_port"`
	HostPort      int             `json:"host_port"`
	Public        bool            `json:"public"`
	EnvNames      []string        `json:"env_names"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	Current       *deploymentDTO  `json:"current"`
	Deployments   []deploymentDTO `json:"deployments,omitempty"`
}

func toDeploymentDTO(d *fleet.DeploymentView) deploymentDTO {
	return deploymentDTO{ID: d.ID, Kind: d.Kind, RollbackOf: d.RollbackOf, Image: d.Image, GitRef: d.GitRef, TaskID: d.TaskID,
		CreatedBy: d.CreatedBy, CreatedAt: ts(d.CreatedAt), Status: string(d.Status), StartedAt: tsPtr(d.StartedAt),
		FinishedAt: tsPtr(d.FinishedAt), Error: d.Error, Result: d.Result}
}

func toAppDTO(v *fleet.AppView) appDTO {
	out := appDTO{ID: v.ID, ServerID: v.ServerID, Name: v.Name, GitURL: v.GitURL, GitRef: v.GitRef, BuildMode: v.BuildMode,
		BuildContext: v.BuildContext, Dockerfile: v.Dockerfile, ContainerPort: v.ContainerPort, HostPort: v.HostPort,
		Public: v.Public, EnvNames: v.EnvNames, CreatedAt: ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt)}
	if v.Current != nil {
		c := toDeploymentDTO(v.Current)
		out.Current = &c
	}
	for i := range v.Deployments {
		out.Deployments = append(out.Deployments, toDeploymentDTO(&v.Deployments[i]))
	}
	return out
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	list, err := s.fleet.ListApps(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]appDTO, len(list))
	for i := range list {
		out[i] = toAppDTO(&list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var in fleet.AppInput
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.fleet.CreateApp(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAppDTO(v))
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.fleet.GetApp(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "appId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAppDTO(v))
}

func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	var in fleet.AppInput
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.fleet.UpdateApp(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "appId"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAppDTO(v))
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	t, err := s.fleet.DeleteApp(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "appId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toTaskDTO(t))
}

func (s *Server) handleDeployApp(w http.ResponseWriter, r *http.Request) {
	d, err := s.fleet.Deploy(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "appId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toDeploymentDTO(d))
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := s.fleet.GetDeployment(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "appId"), chi.URLParam(r, "depId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeploymentDTO(d))
}

func (s *Server) handleRollbackApp(w http.ResponseWriter, r *http.Request) {
	d, err := s.fleet.Rollback(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "appId"), chi.URLParam(r, "depId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toDeploymentDTO(d))
}
