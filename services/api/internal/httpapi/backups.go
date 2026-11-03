package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type backupDTO struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Target      string  `json:"target"`
	TaskID      *string `json:"task_id"`
	PolicyID    *string `json:"policy_id"`
	Size        *int64  `json:"size"`
	SHA256      *string `json:"sha256"`
	PlainSHA256 *string `json:"plain_sha256"`
	Files       *int    `json:"files"`
	Status      string  `json:"status"`
	Error       string  `json:"error"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
}

type policyDTO struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	Target        string  `json:"target"`
	IntervalHours int     `json:"interval_hours"`
	Keep          int     `json:"keep"`
	LastRunAt     *string `json:"last_run_at"`
	CreatedAt     string  `json:"created_at"`
}

func toBackupDTO(v *fleet.BackupView) backupDTO {
	return backupDTO{ID: v.ID, Kind: v.Kind, Target: v.Target, TaskID: v.TaskID, PolicyID: v.PolicyID, Size: v.Size, SHA256: v.SHA256,
		PlainSHA256: v.PlainSHA256, Files: v.Files, Status: string(v.Status), Error: v.Error, CreatedBy: v.CreatedBy, CreatedAt: ts(v.CreatedAt)}
}

func toPolicyDTO(p *store.BackupPolicy) policyDTO {
	return policyDTO{ID: p.ID, Kind: p.Kind, Target: p.Target, IntervalHours: p.IntervalHours, Keep: p.Keep, LastRunAt: tsPtr(p.LastRunAt), CreatedAt: ts(p.CreatedAt)}
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	v, err := s.fleet.ListBackups(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	backups := make([]backupDTO, len(v.Backups))
	for i := range v.Backups {
		backups[i] = toBackupDTO(&v.Backups[i])
	}
	policies := make([]policyDTO, len(v.Policies))
	for i := range v.Policies {
		policies[i] = toPolicyDTO(&v.Policies[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups, "policies": policies})
}

type backupTargetInput struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	var in backupTargetInput
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.fleet.CreateBackup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in.Kind, in.Target)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toBackupDTO(v))
}

func (s *Server) backupTask(w http.ResponseWriter, r *http.Request, fn func() (*store.Task, error)) {
	t, err := fn()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toTaskDTO(t))
}

func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request) {
	s.backupTask(w, r, func() (*store.Task, error) {
		return s.fleet.VerifyBackup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "backupId"))
	})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	s.backupTask(w, r, func() (*store.Task, error) {
		return s.fleet.RestoreBackup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "backupId"))
	})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	s.backupTask(w, r, func() (*store.Task, error) {
		return s.fleet.DeleteBackup(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "backupId"))
	})
}

func (s *Server) handleExportRecoveryKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.fleet.ExportRecoveryKey(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"recovery_key": key})
}

func (s *Server) handleCreateBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind          string `json:"kind"`
		Target        string `json:"target"`
		IntervalHours int    `json:"interval_hours"`
		Keep          int    `json:"keep"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	p, err := s.fleet.CreateBackupPolicy(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), in.Kind, in.Target, in.IntervalHours, in.Keep)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(p))
}

func (s *Server) handleDeleteBackupPolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.fleet.DeleteBackupPolicy(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "policyId")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
