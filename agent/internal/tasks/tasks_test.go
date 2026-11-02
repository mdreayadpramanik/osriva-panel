package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func TestParseSystemd(t *testing.T) {
	units := []byte(`[
	 {"unit":"nginx.service","load":"loaded","active":"active","sub":"running","description":"A high performance web server"},
	 {"unit":"apt-daily.service","load":"loaded","active":"inactive","sub":"dead","description":"Daily apt download activities"},
	 {"unit":"broken.service","load":"loaded","active":"failed","sub":"failed","description":"Broken"},
	 {"unit":"setup.service","load":"loaded","active":"active","sub":"exited","description":"Oneshot"},
	 {"unit":"ghost.service","load":"not-found","active":"inactive","sub":"dead","description":"ghost"},
	 {"unit":"dbus.socket","load":"loaded","active":"active","sub":"running","description":"not a service"}
	]`)
	files := []byte(`[
	 {"unit_file":"nginx.service","state":"enabled"},
	 {"unit_file":"apt-daily.service","state":"static"},
	 {"unit_file":"broken.service","state":"disabled"},
	 {"unit_file":"never-started.service","state":"disabled"},
	 {"unit_file":"getty@.service","state":"enabled"},
	 {"unit_file":"masked.service","state":"masked"}
	]`)
	svcs, err := ParseSystemd(units, files)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]agenttask.Service{}
	for _, s := range svcs {
		got[s.Name] = s
	}
	want := map[string][2]string{
		"nginx.service":         {"running", "enabled"},
		"apt-daily.service":     {"stopped", "static"},
		"broken.service":        {"failed", "disabled"},
		"setup.service":         {"stopped", "unknown"},
		"never-started.service": {"stopped", "disabled"},
		"masked.service":        {"stopped", "disabled"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d services: %+v", len(got), svcs)
	}
	for name, w := range want {
		if s, ok := got[name]; !ok || s.State != w[0] || s.Startup != w[1] {
			t.Errorf("%s: %+v, want state=%s startup=%s", name, s, w[0], w[1])
		}
	}
	if got["nginx.service"].Detail != "active (running)" || got["nginx.service"].Description == "" {
		t.Errorf("detail/description: %+v", got["nginx.service"])
	}
	if _, err := ParseSystemd([]byte("not json"), nil); err == nil {
		t.Error("bad JSON accepted")
	}
}

func TestParseSystemctlShow(t *testing.T) {
	p := ParseSystemctlShow([]byte("ActiveState=active\nUnitFileState=enabled\n"))
	if p["ActiveState"] != "active" || p["UnitFileState"] != "enabled" {
		t.Fatal(p)
	}
}

func TestParseProcPIDStat(t *testing.T) {
	// A command name containing spaces and parentheses.
	line := "1234 (my (weird) app) S 1 1234 1234 0 -1 4194560 100 0 0 0 5 3 0 0 20 0 7 0 12345 1000000 250 18446744073709551615"
	st, err := ParseProcPIDStat(line)
	if err != nil || st.PID != 1234 || st.PPID != 1 || st.Comm != "my (weird) app" || st.Threads != 7 || st.RSSPages != 250 {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := ParseProcPIDStat("garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
	if uid, ok := ParseUID("Name:\tbash\nUid:\t1000\t1000\t1000\t1000\n"); !ok || uid != "1000" {
		t.Fatal(uid)
	}
	if c := CmdLine([]byte("/usr/bin/python3\x00-m\x00http.server\x00"), 100); c != "/usr/bin/python3 -m http.server" {
		t.Fatal(c)
	}
}

func TestExecuteRejectsInvalidTasks(t *testing.T) {
	for _, c := range []struct{ typ, params string }{
		{"shell.exec", `{"cmd":"whoami"}`},
		{agenttask.ServiceAction, `{"name":"x; reboot","action":"stop"}`},
		{agenttask.ServiceAction, `{"name":"sshd","action":"mask"}`},
	} {
		if _, err := Execute(context.Background(), c.typ, json.RawMessage(c.params)); !errors.Is(err, agenttask.ErrInvalid) {
			t.Errorf("%s %s: %v", c.typ, c.params, err)
		}
	}
}

// Read-only tasks against the real host (Windows locally, Linux in CI).
func TestReadOnlyTasksOnThisHost(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip()
	}
	res, err := Execute(context.Background(), agenttask.ProcessesList, nil)
	if err != nil {
		t.Fatal(err)
	}
	procs := res.(agenttask.ProcessesResult).Processes
	if len(procs) < 3 {
		t.Fatalf("only %d processes", len(procs))
	}
	res, err = Execute(context.Background(), agenttask.ServicesList, nil)
	if errors.Is(err, ErrUnsupported) {
		t.Skipf("service listing unsupported here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if svcs := res.(agenttask.ServicesResult).Services; len(svcs) < 5 {
		t.Fatalf("only %d services", len(svcs))
	}
}
