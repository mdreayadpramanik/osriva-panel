package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func TestParseJournal(t *testing.T) {
	out := []byte(`{"__REALTIME_TIMESTAMP":"1793640000000000","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","MESSAGE":"bind() failed"}
{"__REALTIME_TIMESTAMP":"1793640001000000","PRIORITY":"6","SYSLOG_IDENTIFIER":"kernel","MESSAGE":[104,105,255]}
not json
{"__REALTIME_TIMESTAMP":"1793640002000000","PRIORITY":"4","SYSLOG_IDENTIFIER":"cron","MESSAGE":"low disk"}
`)
	e := ParseJournal(out)
	if len(e) != 3 {
		t.Fatalf("entries: %+v", e)
	}
	if e[0].Level != "error" || e[0].Source != "nginx.service" || e[0].TS != "2026-11-02T17:20:00Z" {
		t.Errorf("first: %+v", e[0])
	}
	if e[1].Message != "hi�" || e[1].Source != "kernel" || e[1].Level != "info" {
		t.Errorf("byte-array message: %+v", e[1])
	}
	if e[2].Level != "warning" {
		t.Errorf("third: %+v", e[2])
	}
}

func TestParseWinEvents(t *testing.T) {
	out := []byte(`<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Service Control Manager'/><Level>2</Level><TimeCreated SystemTime='2026-11-02T17:38:20.1362194Z'/></System><RenderingInfo Culture='en-US'><Message>The X service terminated unexpectedly.</Message></RenderingInfo></Event><Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='EventLog'/><Level>4</Level><TimeCreated SystemTime='2026-11-02T17:30:00Z'/></System><RenderingInfo Culture='en-US'><Message>Started.</Message></RenderingInfo></Event>`)
	e := ParseWinEvents(out)
	if len(e) != 2 || e[0].Source != "EventLog" || e[0].Level != "info" || e[1].Level != "error" || e[1].TS != "2026-11-02T17:38:20Z" {
		t.Fatalf("events (want oldest first): %+v", e)
	}
	if got := ParseWinEvents(nil); got == nil || len(got) != 0 {
		t.Fatal("empty output must be an empty list")
	}
}

func TestValidateLogs(t *testing.T) {
	ok := []string{`{"source":"system"}`, `{"source":"service","service":"nginx.service","lines":50,"min_level":"warning"}`, `{"source":"application"}`}
	for _, p := range ok {
		if err := agenttask.Validate(agenttask.LogsRead, json.RawMessage(p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	bad := []string{`{"source":"file","path":"/etc/shadow"}`, `{"source":"service","service":"--root=/"}`, `{"source":"system","lines":5000}`,
		`{"source":"system","service":"x"}`, `{"source":"system","min_level":"trace"}`}
	for _, p := range bad {
		if err := agenttask.Validate(agenttask.LogsRead, json.RawMessage(p)); !errors.Is(err, agenttask.ErrInvalid) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// Read the real system log (Windows Event Log locally, journald in CI).
func TestReadLogsOnThisHost(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip()
	}
	res, err := Execute(context.Background(), agenttask.LogsRead, json.RawMessage(`{"source":"system","lines":20}`))
	if errors.Is(err, ErrUnsupported) {
		t.Skipf("%v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	r := res.(agenttask.LogsResult)
	if len(r.Entries) == 0 || r.Entries[len(r.Entries)-1].TS == "" {
		t.Fatalf("no entries from %s", r.Backend)
	}
}
