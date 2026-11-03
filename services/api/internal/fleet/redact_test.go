package fleet

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactedParamsHidesSecrets(t *testing.T) {
	got, _ := json.Marshal(RedactedParams(json.RawMessage(`{"image":"postgres:17","env":{"POSTGRES_PASSWORD":"hunter2","A":"b"}}`)))
	if strings.Contains(string(got), "hunter2") || !strings.Contains(string(got), `"env_names":["A","POSTGRES_PASSWORD"]`) {
		t.Fatalf("env not redacted: %s", got)
	}
	got, _ = json.Marshal(RedactedParams(json.RawMessage(`{"path":"/srv/a","content":"secret"}`)))
	if strings.Contains(string(got), "secret") || !strings.Contains(string(got), `"content_bytes":6`) {
		t.Fatalf("content not redacted: %s", got)
	}
}

func TestRedactedParamsHidesPasswords(t *testing.T) {
	got, _ := json.Marshal(RedactedParams(json.RawMessage(`{"name":"blog","admin_password":"hunter6","nested":{"db_password":"x"},"backup_key":"AGE-SECRET-KEY-1SECRET"}`)))
	if strings.Contains(string(got), "hunter6") || strings.Contains(string(got), `"x"`) || strings.Contains(string(got), "SECRET") || !strings.Contains(string(got), `"name":"blog"`) {
		t.Fatalf("passwords not redacted: %s", got)
	}
}
