// Package config loads and validates the API configuration from OSRIVA_*
// environment variables. Invalid configuration is a startup error.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/logging"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

type Config struct {
	Env                string
	HTTPAddr           string
	DatabaseURL        string
	SecretKey          []byte
	CookieSecure       bool
	SessionTTL         time.Duration
	SessionIdleTimeout time.Duration
	ReauthWindow       time.Duration
	LogLevel           slog.Level
	LogFormat          string
	TrustedProxies     []netip.Prefix
	DevMemoryStore     bool

	// Agent endpoint (all three empty = agents cannot enrol yet).
	PublicURL      string // base URL users and agents use for the panel, e.g. https://panel.example.com
	AgentAddr      string // listen address of the mutual-TLS agent listener, e.g. :8443
	AgentPublicURL string // https URL agents use to reach AgentAddr, e.g. https://panel.example.com:8443
	AgentDistDir   string // directory with osriva-agent-<os>-<arch>[.exe] builds served to servers

	// RedisURL (optional) shares rate limits and login lockouts between API instances.
	RedisURL string

	// PluginDir and PluginTrustedKeys (base64 Ed25519 public keys) enable signed plugins.
	PluginDir         string
	PluginTrustedKeys []string
}

// AgentEnabled reports whether agent enrolment is configured.
func (c *Config) AgentEnabled() bool { return c.AgentAddr != "" }

// Load reads configuration using getenv (os.Getenv in production).
func Load(getenv func(string) string) (*Config, error) {
	var errs []error
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := get(k, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid positive duration %q", k, v))
			return def
		}
		return d
	}
	boolean := func(k string, def bool) bool {
		v := get(k, "")
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid boolean %q", k, v))
			return def
		}
		return b
	}

	c := &Config{
		Env:         get("OSRIVA_ENV", EnvProduction),
		HTTPAddr:    get("OSRIVA_HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL: get("OSRIVA_DATABASE_URL", ""),
		LogFormat:   get("OSRIVA_LOG_FORMAT", "json"),
	}
	if c.Env != EnvDevelopment && c.Env != EnvProduction {
		errs = append(errs, fmt.Errorf("OSRIVA_ENV: must be %q or %q", EnvDevelopment, EnvProduction))
	}
	c.CookieSecure = boolean("OSRIVA_COOKIE_SECURE", c.Env == EnvProduction)
	c.SessionTTL = dur("OSRIVA_SESSION_TTL", 12*time.Hour)
	c.SessionIdleTimeout = dur("OSRIVA_SESSION_IDLE_TIMEOUT", 2*time.Hour)
	c.ReauthWindow = dur("OSRIVA_REAUTH_WINDOW", 10*time.Minute)
	c.DevMemoryStore = boolean("OSRIVA_DEV_MEMORY_STORE", false)

	if lvl, err := logging.ParseLevel(get("OSRIVA_LOG_LEVEL", "info")); err != nil {
		errs = append(errs, fmt.Errorf("OSRIVA_LOG_LEVEL: %w", err))
	} else {
		c.LogLevel = lvl
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, errors.New("OSRIVA_LOG_FORMAT: must be json or text"))
	}

	if raw := get("OSRIVA_SECRET_KEY", ""); raw == "" {
		errs = append(errs, errors.New("OSRIVA_SECRET_KEY: required (generate with `osriva-api gen-secret`)"))
	} else if key, err := base64.StdEncoding.DecodeString(raw); err != nil {
		errs = append(errs, errors.New("OSRIVA_SECRET_KEY: must be standard base64"))
	} else if len(key) < secrets.MinMasterKeyLen {
		errs = append(errs, fmt.Errorf("OSRIVA_SECRET_KEY: must decode to at least %d bytes", secrets.MinMasterKeyLen))
	} else {
		c.SecretKey = key
	}

	for _, p := range strings.Split(get("OSRIVA_TRUSTED_PROXIES", ""), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			if addr, aerr := netip.ParseAddr(p); aerr == nil {
				pfx = netip.PrefixFrom(addr, addr.BitLen())
			} else {
				errs = append(errs, fmt.Errorf("OSRIVA_TRUSTED_PROXIES: invalid CIDR or IP %q", p))
				continue
			}
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx.Masked())
	}

	c.PublicURL = strings.TrimRight(get("OSRIVA_PUBLIC_URL", ""), "/")
	c.AgentAddr = get("OSRIVA_AGENT_ADDR", "")
	c.AgentPublicURL = strings.TrimRight(get("OSRIVA_AGENT_PUBLIC_URL", ""), "/")
	c.AgentDistDir = get("OSRIVA_AGENT_DIST_DIR", "")
	c.RedisURL = get("OSRIVA_REDIS_URL", "")
	c.PluginDir = get("OSRIVA_PLUGIN_DIR", "")
	for _, k := range strings.Split(get("OSRIVA_PLUGIN_TRUSTED_KEYS", ""), ",") {
		if k = strings.TrimSpace(k); k != "" {
			c.PluginTrustedKeys = append(c.PluginTrustedKeys, k)
		}
	}
	if (c.PluginDir == "") != (len(c.PluginTrustedKeys) == 0) {
		errs = append(errs, errors.New("OSRIVA_PLUGIN_DIR and OSRIVA_PLUGIN_TRUSTED_KEYS must be set together"))
	}
	if u, err := url.Parse(c.RedisURL); c.RedisURL != "" && (err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Host == "") {
		errs = append(errs, errors.New("OSRIVA_REDIS_URL: must be a redis:// or rediss:// URL"))
	}
	if c.AgentAddr != "" || c.AgentPublicURL != "" {
		if c.AgentAddr == "" || c.AgentPublicURL == "" || c.PublicURL == "" {
			errs = append(errs, errors.New("OSRIVA_AGENT_ADDR, OSRIVA_AGENT_PUBLIC_URL and OSRIVA_PUBLIC_URL must be set together"))
		}
		if u, err := url.Parse(c.AgentPublicURL); c.AgentPublicURL != "" && (err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Path != "") {
			errs = append(errs, errors.New("OSRIVA_AGENT_PUBLIC_URL: must be an https:// URL without a path"))
		}
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		insecureOK := c.Env == EnvDevelopment && err == nil && u.Scheme == "http"
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && !insecureOK) {
			errs = append(errs, errors.New("OSRIVA_PUBLIC_URL: must be an https:// URL (http:// only in development)"))
		}
	}

	if c.DevMemoryStore && c.Env != EnvDevelopment {
		errs = append(errs, errors.New("OSRIVA_DEV_MEMORY_STORE: only allowed with OSRIVA_ENV=development"))
	}
	if !c.DevMemoryStore && c.DatabaseURL == "" {
		errs = append(errs, errors.New("OSRIVA_DATABASE_URL: required"))
	}
	if c.Env == EnvProduction && !c.CookieSecure {
		errs = append(errs, errors.New("OSRIVA_COOKIE_SECURE: must not be false in production"))
	}
	if c.SessionIdleTimeout > c.SessionTTL {
		errs = append(errs, errors.New("OSRIVA_SESSION_IDLE_TIMEOUT: must not exceed OSRIVA_SESSION_TTL"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}
