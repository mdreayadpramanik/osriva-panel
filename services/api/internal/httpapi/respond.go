package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/reqmeta"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const maxBodyBytes = 1 << 20

type errorBody struct {
	Error     apiError `json:"error"`
	RequestID string   `json:"request_id,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, msg, field string) {
	writeJSON(w, status, errorBody{Error: apiError{Code: code, Message: msg, Field: field}, RequestID: reqmeta.From(r.Context()).RequestID})
}

// fail maps service errors to HTTP responses. Unknown errors are logged and
// reported generically so internals never leak to clients.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *identity.ValidationError
	var pe *identity.PolicyError
	switch {
	case errors.As(err, &ve):
		s.writeError(w, r, http.StatusBadRequest, "validation_failed", ve.Field+" "+ve.Message, ve.Field)
	case errors.As(err, &pe):
		s.writeError(w, r, http.StatusConflict, "policy_violation", pe.Message, "")
	case errors.Is(err, fleet.ErrInvalidEnrollment):
		s.writeError(w, r, http.StatusUnauthorized, "invalid_enrollment_token", err.Error(), "")
	case errors.Is(err, fleet.ErrAgentNotConfigured):
		s.writeError(w, r, http.StatusServiceUnavailable, "agent_endpoint_not_configured", err.Error(), "")
	case errors.Is(err, fleet.ErrAgentForbidden):
		s.writeError(w, r, http.StatusForbidden, "agent_forbidden", err.Error(), "")
	case errors.Is(err, identity.ErrInvalidCredentials):
		s.writeError(w, r, http.StatusUnauthorized, "invalid_credentials", err.Error(), "")
	case errors.Is(err, identity.ErrMFARequired):
		s.writeError(w, r, http.StatusUnauthorized, "mfa_required", err.Error(), "")
	case errors.Is(err, identity.ErrInvalidMFA):
		s.writeError(w, r, http.StatusUnauthorized, "invalid_mfa_code", err.Error(), "")
	case errors.Is(err, identity.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		s.writeError(w, r, http.StatusTooManyRequests, "rate_limited", err.Error(), "")
	case errors.Is(err, identity.ErrUnauthenticated):
		s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", err.Error(), "")
	case errors.Is(err, identity.ErrForbidden):
		s.writeError(w, r, http.StatusForbidden, "forbidden", err.Error(), "")
	case errors.Is(err, identity.ErrReauthRequired):
		s.writeError(w, r, http.StatusForbidden, "reauth_required", err.Error(), "")
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "not_found", "the requested resource does not exist", "")
	case errors.Is(err, store.ErrConflict):
		s.writeError(w, r, http.StatusConflict, "conflict", "the resource conflicts with an existing one", "")
	default:
		s.log.Error("internal error", "err", err, "path", r.URL.Path, "request_id", reqmeta.From(r.Context()).RequestID)
		s.writeError(w, r, http.StatusInternalServerError, "internal_error", "an internal error occurred", "")
	}
}

// decode reads a single JSON object into dst, rejecting unknown fields,
// trailing data, oversized bodies and non-JSON content types.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		s.writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "request body must be application/json", "")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large", "")
		} else {
			s.writeError(w, r, http.StatusBadRequest, "invalid_json", fmt.Sprintf("invalid JSON body: %v", err), "")
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		s.writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON object", "")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}
