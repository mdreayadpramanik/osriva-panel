package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type groupDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ServerIDs   []string `json:"server_ids"`
	CreatedAt   string   `json:"created_at"`
}

func toGroupDTO(g *store.ServerGroup) groupDTO {
	ids := g.ServerIDs
	if ids == nil {
		ids = []string{}
	}
	return groupDTO{ID: g.ID, Name: g.Name, Description: g.Description, ServerIDs: ids, CreatedAt: ts(g.CreatedAt)}
}

type batchTaskDTO struct {
	TaskID     string  `json:"task_id"`
	ServerID   string  `json:"server_id"`
	ServerName string  `json:"server_name"`
	Status     string  `json:"status"`
	Error      string  `json:"error"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

type batchDTO struct {
	ID          string                `json:"id"`
	Type        string                `json:"type"`
	Params      json.RawMessage       `json:"params"`
	GroupID     *string               `json:"group_id"`
	Target      string                `json:"target"`
	RequestedBy *string               `json:"requested_by"`
	CreatedAt   string                `json:"created_at"`
	Counts      map[string]int        `json:"counts"`
	Tasks       []batchTaskDTO        `json:"tasks"`
	Skipped     []fleet.SkippedServer `json:"skipped"`
}

func toBatchDTO(v *fleet.BatchView) batchDTO {
	b := v.Batch
	d := batchDTO{ID: b.ID, Type: b.Type, Params: redacted(b.Params), GroupID: b.GroupID, Target: b.Target, RequestedBy: b.RequestedBy,
		CreatedAt: ts(b.CreatedAt), Counts: v.Counts, Tasks: []batchTaskDTO{}, Skipped: v.Skipped}
	if d.Skipped == nil {
		d.Skipped = []fleet.SkippedServer{}
	}
	for _, t := range v.Tasks {
		d.Tasks = append(d.Tasks, batchTaskDTO{TaskID: t.Task.ID, ServerID: t.Task.ServerID, ServerName: t.ServerName, Status: string(t.Task.Status),
			Error: t.Task.Error, StartedAt: tsPtr(t.Task.StartedAt), FinishedAt: tsPtr(t.Task.FinishedAt)})
	}
	return d
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	list, err := s.fleet.ListGroups(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]groupDTO, len(list))
	for i := range list {
		out[i] = toGroupDTO(&list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var in fleet.GroupInput
	if !s.decode(w, r, &in) {
		return
	}
	g, err := s.fleet.CreateGroup(r.Context(), principalFrom(r.Context()), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toGroupDTO(g))
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.fleet.GetGroup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "groupId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var in fleet.GroupInput
	if !s.decode(w, r, &in) {
		return
	}
	g, err := s.fleet.UpdateGroup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "groupId"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.fleet.DeleteGroup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "groupId")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetGroupMembers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerIDs []string `json:"server_ids"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	g, err := s.fleet.SetGroupMembers(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "groupId"), in.ServerIDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

func (s *Server) handleFleetTaskTypes(w http.ResponseWriter, r *http.Request) {
	if err := s.id.Require(r.Context(), principalFrom(r.Context()), rbac.ServersRead); err != nil {
		s.fail(w, r, err)
		return
	}
	types := agenttask.BulkTypes()
	sort.Strings(types)
	writeJSON(w, http.StatusOK, map[string]any{"types": types, "max_servers": fleet.MaxBatchServers})
}

func (s *Server) handleRunBatch(w http.ResponseWriter, r *http.Request) {
	var in fleet.BatchInput
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.fleet.RunBatch(r.Context(), principalFrom(r.Context()), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toBatchDTO(v))
}

func (s *Server) handleListBatches(w http.ResponseWriter, r *http.Request) {
	list, err := s.fleet.ListBatches(r.Context(), principalFrom(r.Context()), queryInt(r, "limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]batchDTO, len(list))
	for i := range list {
		out[i] = toBatchDTO(&list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": out})
}

func (s *Server) handleGetBatch(w http.ResponseWriter, r *http.Request) {
	v, err := s.fleet.GetBatch(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "batchId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBatchDTO(v))
}

type jobDTO struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
	LastError   string `json:"last_error"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if err := s.id.Require(r.Context(), principalFrom(r.Context()), rbac.SettingsRead); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.jobs == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the job queue is not running", "")
		return
	}
	st, err := s.jobs.Status(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts := map[string]int{}
	for _, k := range []store.JobStatus{store.JobQueued, store.JobRunning, store.JobSucceeded, store.JobFailed} {
		counts[string(k)] = st.Counts[k]
	}
	failed := make([]jobDTO, len(st.Recent))
	for i, j := range st.Recent {
		failed[i] = jobDTO{ID: j.ID, Kind: j.Kind, Status: string(j.Status), Attempts: j.Attempts, MaxAttempts: j.MaxAttempts,
			LastError: j.LastError, CreatedAt: ts(j.CreatedAt), UpdatedAt: ts(j.UpdatedAt)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "recent_failures": failed})
}
