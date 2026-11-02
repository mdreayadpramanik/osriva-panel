// Package config loads and validates the API configuration from OSRIVA_*
// environment variables. Invalid configuration is a startup error.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/osriva/osriva-panel/services/api/internal/logging"
	"github.com/osriva/osriva-panel/services/api/internal/secrets"
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
}

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
