package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type taskDTO struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Params      json.RawMessage `json:"params"`
	Status      string          `json:"status"`
	RequestedBy *string         `json:"requested_by"`
	CreatedAt   string          `json:"created_at"`
	StartedAt   *string         `json:"started_at"`
	FinishedAt  *string         `json:"finished_at"`
	Result      json.RawMessage `json:"result"`
	Error       string          `json:"error"`
}

func toTaskDTO(t *store.Task) taskDTO {
	res := t.Result
	if len(res) == 0 {
		res = json.RawMessage("null")
	}
	return taskDTO{ID: t.ID, Type: t.Type, Params: redacted(t.Params), Status: string(t.Status), RequestedBy: t.RequestedBy,
		CreatedAt: ts(t.CreatedAt), StartedAt: tsPtr(t.StartedAt), FinishedAt: tsPtr(t.FinishedAt), Result: res, Error: t.Error}
}

func (s *Server) handleQueueTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type   string          `json:"type"`
		Params json.RawMessage `json:"params"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	t, err := s.fleet.QueueTask(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in.Type, in.Params)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toTaskDTO(t))
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	wait = min(max(wait, 0), 25)
	t, err := s.fleet.GetTask(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "taskId"), time.Duration(wait)*time.Second)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(t))
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	list, err := s.fleet.ListTasks(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), queryInt(r, "limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]taskDTO, len(list))
	for i := range list {
		d := toTaskDTO(&list[i])
		d.Result = nil // the list stays small; fetch a task for its result
		out[i] = d
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func (s *Server) handleServerMetrics(w http.ResponseWriter, r *http.Request) {
	minutes := queryInt(r, "minutes")
	if minutes > 24*60 {
		rollups, err := s.fleet.MetricRollups(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), time.Duration(minutes)*time.Minute)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"resolution": "hour", "samples": rollups})
		return
	}
	samples, err := s.fleet.Metrics(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), time.Duration(minutes)*time.Minute)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if samples == nil {
		samples = []metrics.Sample{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolution": "raw", "samples": samples})
}

// ---- agent listener ----

func (s *Server) handleAgentPoll(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.fleet.PollTasks(r.Context(), agentFrom(r.Context()), agentproto.PollWait)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := agentproto.PollResponse{Tasks: make([]agentproto.TaskEnvelope, len(tasks))}
	for i, t := range tasks {
		out.Tasks[i] = agentproto.TaskEnvelope{ID: t.ID, Type: t.Type, Params: t.Params}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAgentTaskResult(w http.ResponseWriter, r *http.Request) {
	var in agentproto.TaskResult
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.fleet.CompleteTask(r.Context(), agentFrom(r.Context()), chi.URLParam(r, "taskId"), in); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentMetrics(w http.ResponseWriter, r *http.Request) {
	var in agentproto.MetricsRequest
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.fleet.IngestMetrics(r.Context(), agentFrom(r.Context()), in); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func redacted(params json.RawMessage) json.RawMessage {
	raw, err := json.Marshal(fleet.RedactedParams(params))
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
