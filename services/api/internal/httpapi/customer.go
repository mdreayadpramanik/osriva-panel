package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/customer"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	// CustomerCookie is only ever sent to the customer API (its Path), never to operator endpoints.
	CustomerCookie     = "osriva_customer"
	customerCookiePath = "/api/v1/customer"
)

type customerKey struct{}

func customerFrom(ctx context.Context) *customer.Principal {
	p, _ := ctx.Value(customerKey{}).(*customer.Principal)
	return p
}

func (s *Server) setCustomerCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: CustomerCookie, Value: token, Path: customerCookiePath, HttpOnly: true, Secure: s.cookieSecure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(customer.SessionTTL.Seconds())})
}

func (s *Server) clearCustomerCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CustomerCookie, Value: "", Path: customerCookiePath, HttpOnly: true, Secure: s.cookieSecure,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// requireCustomer accepts only a customer session (never an operator cookie)
// and enforces the customer CSRF token on unsafe methods.
func (s *Server) requireCustomer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CustomerCookie)
		if err != nil {
			s.fail(w, r, identity.ErrUnauthenticated)
			return
		}
		p, err := s.customers.Authenticate(r.Context(), c.Value)
		if err != nil {
			s.clearCustomerCookie(w)
			s.fail(w, r, err)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !s.customers.VerifyCSRF(p, r.Header.Get(CSRFHeader)) {
				s.writeError(w, r, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token", "")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), customerKey{}, p)))
	})
}

// customerVPSDTO is what a customer sees: no node, task or internal error details.
type customerVPSDTO struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	IPAddress string   `json:"ip_address"`
	VCPUs     int      `json:"vcpus"`
	MemoryMB  int      `json:"memory_mb"`
	DiskGB    int      `json:"disk_gb"`
	SSHKeys   []string `json:"ssh_keys"`
	State     string   `json:"state"`
	Pending   string   `json:"pending"`
	Failed    bool     `json:"last_operation_failed"`
	CreatedAt string   `json:"created_at"`
}

func customerVPSConv(v *store.VPS) customerVPSDTO {
	op, _, _ := cutColon(v.PendingOp)
	return customerVPSDTO{ID: v.ID, Name: v.Name, IPAddress: v.IPAddress, VCPUs: v.VCPUs, MemoryMB: v.MemoryMB, DiskGB: v.DiskGB, SSHKeys: v.SSHKeys,
		State: v.State, Pending: op, Failed: v.LastError != "", CreatedAt: ts(v.CreatedAt)}
}

type customerDTO struct {
	ID             string   `json:"id"`
	Email          string   `json:"email"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	PasswordSet    bool     `json:"password_set"`
	SetupExpiresAt *string  `json:"setup_expires_at"`
	CreatedAt      string   `json:"created_at"`
	LastLoginAt    *string  `json:"last_login_at"`
	VPSIDs         []string `json:"vps_ids,omitempty"`
}

func customerConv(c *store.Customer) customerDTO {
	return customerDTO{ID: c.ID, Email: c.Email, Name: c.Name, Status: string(c.Status), PasswordSet: c.PasswordHash != "",
		SetupExpiresAt: tsPtr(c.SetupExpiresAt), CreatedAt: ts(c.CreatedAt), LastLoginAt: tsPtr(c.LastLoginAt)}
}

func (s *Server) customersOr503(w http.ResponseWriter, r *http.Request) bool {
	if s.customers == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the Customer Panel is not available", "")
		return false
	}
	return true
}

// customerRoutes is the Customer Panel API under /api/v1/customer.
func (s *Server) customerRoutes(r chi.Router) {
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.customersOr503(w, r) {
				next.ServeHTTP(w, r)
			}
		})
	})
	r.Post("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		res, err := s.customers.Login(r.Context(), in.Email, in.Password)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.setCustomerCookie(w, res.Token)
		writeJSON(w, http.StatusOK, map[string]any{"customer": customerConv(res.Principal.Customer), "csrf_token": s.customers.CSRFToken(res.Principal)})
	})
	r.Post("/auth/setup", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		s.noContent(w, r, s.customers.Setup(r.Context(), in.Token, in.Password))
	})
	r.Group(func(r chi.Router) {
		r.Use(s.requireCustomer)
		r.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
			p := customerFrom(r.Context())
			writeJSON(w, http.StatusOK, map[string]any{"customer": customerConv(p.Customer), "csrf_token": s.customers.CSRFToken(p)})
		})
		r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
			err := s.customers.Logout(r.Context(), customerFrom(r.Context()))
			s.clearCustomerCookie(w)
			s.noContent(w, r, err)
		})
		r.Post("/auth/password", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				CurrentPassword string `json:"current_password"`
				NewPassword     string `json:"new_password"`
			}
			if !s.decode(w, r, &in) {
				return
			}
			s.noContent(w, r, s.customers.ChangePassword(r.Context(), customerFrom(r.Context()), in.CurrentPassword, in.NewPassword))
		})
		r.Get("/vps", func(w http.ResponseWriter, r *http.Request) {
			items, err := s.customers.ListVPS(r.Context(), customerFrom(r.Context()))
			list(s, w, r, "vps", items, err, customerVPSConv)
		})
		r.Get("/vps/{vpsId}", func(w http.ResponseWriter, r *http.Request) {
			v, err := s.customers.GetVPS(r.Context(), customerFrom(r.Context()), chi.URLParam(r, "vpsId"))
			one(s, w, r, http.StatusOK, v, err, customerVPSConv)
		})
		r.Post("/vps/{vpsId}/actions", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Action string `json:"action"`
			}
			if !s.decode(w, r, &in) {
				return
			}
			v, err := s.customers.VPSAction(r.Context(), customerFrom(r.Context()), chi.URLParam(r, "vpsId"), in.Action)
			one(s, w, r, http.StatusAccepted, v, err, customerVPSConv)
		})
	})
}

// customerAdminRoutes are the operator endpoints for customer accounts.
func (s *Server) customerAdminRoutes(r chi.Router) {
	p := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.customersOr503(w, r) {
				h(w, r)
			}
		}
	}
	created := func(w http.ResponseWriter, r *http.Request, code int, c *customer.Created, err error) {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// The token goes in the URL fragment, which browsers never send to a server.
		writeJSON(w, code, map[string]any{"customer": customerConv(&c.Customer), "setup_path": "/customer/setup#" + c.SetupToken})
	}
	r.Get("/customers", p(func(w http.ResponseWriter, r *http.Request) {
		views, err := s.customers.List(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]customerDTO, len(views))
		for i, v := range views {
			out[i] = customerConv(&v.Customer)
			out[i].VPSIDs = []string{}
			for _, x := range v.VPS {
				out[i].VPSIDs = append(out[i].VPSIDs, x.ID)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"customers": out})
	}))
	r.Post("/customers", p(func(w http.ResponseWriter, r *http.Request) {
		var in customer.Input
		if !s.decode(w, r, &in) {
			return
		}
		c, err := s.customers.Create(r.Context(), principalFrom(r.Context()), in)
		created(w, r, http.StatusCreated, c, err)
	}))
	r.Post("/customers/{customerId}/setup-link", p(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.customers.NewSetupLink(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "customerId"))
		created(w, r, http.StatusOK, c, err)
	}))
	r.Patch("/customers/{customerId}", p(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		c, err := s.customers.SetEnabled(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "customerId"), in.Enabled)
		one(s, w, r, http.StatusOK, c, err, customerConv)
	}))
	r.Put("/provider/vps/{vpsId}/customer", func(w http.ResponseWriter, r *http.Request) {
		if !s.providerOr503(w, r) {
			return
		}
		var in struct {
			CustomerID *string `json:"customer_id"`
		}
		if !s.decode(w, r, &in) {
			return
		}
		v, err := s.provider.AssignCustomer(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "vpsId"), in.CustomerID)
		one(s, w, r, http.StatusOK, v, err, toVPSDTO)
	})
}
