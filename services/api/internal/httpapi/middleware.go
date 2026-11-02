package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/osriva/osriva-panel/services/api/internal/identity"
	"github.com/osriva/osriva-panel/services/api/internal/reqmeta"
)

const (
	SessionCookie = "osriva_session"
	CSRFHeader    = "X-CSRF-Token"
)

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// clientIP returns the caller's address. X-Forwarded-For is honoured only
// when the direct peer is a configured trusted proxy, walking right to left
// past trusted hops so clients cannot spoof their address.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !isTrusted(peer) {
		return peer.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !isTrusted(a) {
			return a.String()
		}
		peer = a
	}
	return peer.String()
}

func (s *Server) withMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		m := reqmeta.Meta{RequestID: id, IP: clientIP(r, s.trustedProxies), UserAgent: r.UserAgent()}
		next.ServeHTTP(w, r.WithContext(reqmeta.With(r.Context(), m)))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "value", v, "stack", string(debug.Stack()), "request_id", reqmeta.From(r.Context()).RequestID)
				s.writeError(w, r, http.StatusInternalServerError, "internal_error", "an internal error occurred", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// accessLog writes one structured line per request. Query strings are omitted
// because they may carry user input; bodies and headers are never logged.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		m := reqmeta.From(r.Context())
		s.log.Info("http request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"bytes", rec.bytes, "duration_ms", time.Since(start).Milliseconds(), "ip", m.IP, "request_id", m.RequestID)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if s.cookieSecure {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) apiRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.apiLimiter.Allow(reqmeta.From(r.Context()).IP) {
			w.Header().Set("Retry-After", "1")
			s.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type principalKey struct{}

func principalFrom(ctx context.Context) *identity.Principal {
	p, _ := ctx.Value(principalKey{}).(*identity.Principal)
	return p
}

// requireAuth resolves the session cookie and enforces CSRF on unsafe methods.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			s.fail(w, r, identity.ErrUnauthenticated)
			return
		}
		p, err := s.id.Authenticate(r.Context(), c.Value)
		if err != nil {
			s.clearSessionCookie(w)
			s.fail(w, r, err)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !s.id.VerifyCSRF(p, r.Header.Get(CSRFHeader)) {
				s.writeError(w, r, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token", "")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cookieSecure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(s.sessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.cookieSecure,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}
