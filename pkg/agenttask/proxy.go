package agenttask

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
)

// Reverse proxy and websites (docs/adr/0008-reverse-proxy.md): the agent runs
// Caddy in a container and serves the sites the panel sends it, with automatic
// HTTPS from an ACME CA or Caddy's internal CA.

const (
	ProxyStatus  = "proxy.status"
	ProxyApply   = "proxy.apply"
	ProxyDisable = "proxy.disable"

	ProxyContainer = "osriva-proxy"
	ProxyImage     = "caddy:2.8.4-alpine"
	ProxyLabel     = "dev.osriva.proxy"

	TLSACME     = "acme"     // public certificate (Let's Encrypt by default)
	TLSInternal = "internal" // Caddy's local CA (for testing and internal names)
	TLSNone     = "none"     // plain HTTP only

	MaxSites = 200
)

func proxyTask(typ string) bool {
	return typ == ProxyStatus || typ == ProxyApply || typ == ProxyDisable
}

type ProxySite struct {
	Domain       string `json:"domain"`
	UpstreamPort int    `json:"upstream_port"` // a service on 127.0.0.1 of the server
	TLS          string `json:"tls"`
}

// ProxyConfig is the complete desired state; every apply replaces the previous one.
type ProxyConfig struct {
	HTTPPort    int         `json:"http_port"`
	HTTPSPort   int         `json:"https_port"`
	ACMEEmail   string      `json:"acme_email,omitempty"`
	ACMEStaging bool        `json:"acme_staging,omitempty"`
	Sites       []ProxySite `json:"sites"`
}

// ValidDomain accepts lower-case DNS names with at least two labels (or
// *.localhost names); no wildcards, IP addresses or trailing dots.
func ValidDomain(d string) bool {
	if d != strings.ToLower(d) || !ValidHostname(d) || !strings.Contains(d, ".") {
		return false
	}
	last := d[strings.LastIndex(d, ".")+1:]
	return last[0] < '0' || last[0] > '9' // the last label of an IPv4 address is numeric
}

func validateProxyConfig(c ProxyConfig) error {
	if c.HTTPPort < 1 || c.HTTPPort > 65535 || c.HTTPSPort < 1 || c.HTTPSPort > 65535 || c.HTTPPort == c.HTTPSPort {
		return fmt.Errorf("%w: HTTP and HTTPS ports must be different ports between 1 and 65535", ErrInvalid)
	}
	if c.ACMEEmail != "" {
		a, err := mail.ParseAddress(c.ACMEEmail)
		if err != nil || a.Address != c.ACMEEmail || strings.ContainsAny(c.ACMEEmail, " \t\r\n{}\"'`#") {
			return fmt.Errorf("%w: invalid ACME e-mail address", ErrInvalid)
		}
	}
	if len(c.Sites) > MaxSites {
		return fmt.Errorf("%w: at most %d sites per server", ErrInvalid, MaxSites)
	}
	seen := map[string]bool{}
	for _, s := range c.Sites {
		if !ValidDomain(s.Domain) {
			return fmt.Errorf("%w: invalid domain %q", ErrInvalid, s.Domain)
		}
		if seen[s.Domain] {
			return fmt.Errorf("%w: domain %s is used twice", ErrInvalid, s.Domain)
		}
		seen[s.Domain] = true
		if s.UpstreamPort < 1 || s.UpstreamPort > 65535 || s.UpstreamPort == c.HTTPPort || s.UpstreamPort == c.HTTPSPort {
			return fmt.Errorf("%w: invalid upstream port for %s", ErrInvalid, s.Domain)
		}
		switch s.TLS {
		case TLSACME, TLSInternal, TLSNone:
		default:
			return fmt.Errorf("%w: tls must be acme, internal or none", ErrInvalid)
		}
	}
	return nil
}

func validateProxy(typ string, raw json.RawMessage) error {
	if typ != ProxyApply {
		var empty struct{}
		return decodeStrict(raw, &empty)
	}
	var c ProxyConfig
	if err := decodeStrict(raw, &c); err != nil {
		return err
	}
	return validateProxyConfig(c)
}

// ValidateProxyConfig is used by the control plane before it stores settings.
func ValidateProxyConfig(c ProxyConfig) error { return validateProxyConfig(c) }

type Certificate struct {
	Domain   string `json:"domain"`
	Issuer   string `json:"issuer"`
	NotAfter string `json:"not_after"`
}

type ProxyStatusResult struct {
	Installed    bool          `json:"installed"`
	Running      bool          `json:"running"`
	State        string        `json:"state,omitempty"`
	Image        string        `json:"image,omitempty"`
	Applied      *ProxyConfig  `json:"applied,omitempty"` // the configuration Caddy is running
	Certificates []Certificate `json:"certificates"`
}
