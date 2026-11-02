package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var validKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadProductionDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{
		"OSRIVA_DATABASE_URL": "postgres://x", "OSRIVA_SECRET_KEY": validKey,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != EnvProduction || !c.CookieSecure || c.SessionTTL != 12*time.Hour || c.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"missing secret":       {"OSRIVA_DATABASE_URL": "x"},
		"short secret":         {"OSRIVA_DATABASE_URL": "x", "OSRIVA_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 8))},
		"missing db":           {"OSRIVA_SECRET_KEY": validKey},
		"insecure cookie prod": {"OSRIVA_DATABASE_URL": "x", "OSRIVA_SECRET_KEY": validKey, "OSRIVA_COOKIE_SECURE": "false"},
		"memory store prod":    {"OSRIVA_SECRET_KEY": validKey, "OSRIVA_DEV_MEMORY_STORE": "true"},
		"bad duration":         {"OSRIVA_DATABASE_URL": "x", "OSRIVA_SECRET_KEY": validKey, "OSRIVA_SESSION_TTL": "forever"},
		"idle exceeds ttl":     {"OSRIVA_DATABASE_URL": "x", "OSRIVA_SECRET_KEY": validKey, "OSRIVA_SESSION_TTL": "1h", "OSRIVA_SESSION_IDLE_TIMEOUT": "2h"},
		"bad proxy":            {"OSRIVA_DATABASE_URL": "x", "OSRIVA_SECRET_KEY": validKey, "OSRIVA_TRUSTED_PROXIES": "not-an-ip"},
		"bad env":              {"OSRIVA_DATABASE_URL": "x", "OSRIVA_SECRET_KEY": validKey, "OSRIVA_ENV": "staging"},
	}
	for name, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadDevelopmentMemoryStoreAndProxies(t *testing.T) {
	c, err := Load(env(map[string]string{
		"OSRIVA_ENV": "development", "OSRIVA_DEV_MEMORY_STORE": "true", "OSRIVA_SECRET_KEY": validKey,
		"OSRIVA_TRUSTED_PROXIES": "127.0.0.1, 10.0.0.0/8,::1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.CookieSecure {
		t.Error("development should default to insecure cookies for http://localhost")
	}
	var got []string
	for _, p := range c.TrustedProxies {
		got = append(got, p.String())
	}
	if strings.Join(got, ",") != "127.0.0.1/32,10.0.0.0/8,::1/128" {
		t.Fatalf("proxies = %v", got)
	}
}
