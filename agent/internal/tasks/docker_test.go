package tasks

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// fakeDocker is a minimal Docker Engine API with two containers: "web" (a
// user container) and "osriva-postgres-1" (control plane, labelled).
type fakeDocker struct {
	mu      sync.Mutex
	calls   []string
	created map[string]any
	pulled  string
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v1.41")
	cp := map[string]string{agenttask.ControlPlaneLabel: "true", "com.docker.compose.project": "osriva"}
	switch {
	case p == "/version":
		_, _ = io.WriteString(w, `{"Version":"27.3.1","ApiVersion":"1.47"}`)
	case p == "/info":
		_, _ = io.WriteString(w, `{"Containers":2,"ContainersRunning":1,"Images":3,"NCPU":4,"MemTotal":8000,"Driver":"overlay2","OperatingSystem":"Ubuntu 24.04"}`)
	case p == "/containers/json":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "aaa", "Names": []string{"/web"}, "Image": "nginx", "ImageID": "sha256:img1", "State": "running", "Status": "Up 1 minute", "Created": 1700000000,
				"Ports": []map[string]any{{"IP": "127.0.0.1", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"}}, "Labels": map[string]string{}},
			{"Id": "bbb", "Names": []string{"/osriva-postgres-1"}, "Image": "postgres:17", "ImageID": "sha256:img2", "State": "running", "Labels": cp,
				"Mounts": []map[string]string{{"Type": "volume", "Name": "osriva_postgres-data"}}},
		})
	case p == "/containers/web/json" || p == "/containers/aaa/json":
		_, _ = io.WriteString(w, `{"Id":"aaa","Name":"/web","State":{"Status":"running"},"Config":{"Tty":false,"Labels":{}}}`)
	case p == "/containers/osriva-postgres-1/json":
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "bbb", "Name": "/osriva-postgres-1", "Config": map[string]any{"Labels": cp}})
	case p == "/containers/new1/json":
		_, _ = io.WriteString(w, `{"Id":"new1","Name":"/app","State":{"Status":"running"},"Config":{}}`)
	case strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/json"):
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"message":"No such container"}`)
	case p == "/containers/aaa/restart" || p == "/containers/new1/start":
		w.WriteHeader(204)
	case p == "/containers/aaa/stop":
		w.WriteHeader(304)
	case p == "/containers/aaa/logs":
		frame := func(stream byte, s string) {
			var h [8]byte
			h[0] = stream
			binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
			_, _ = w.Write(h[:])
			_, _ = io.WriteString(w, s)
		}
		frame(1, "2026-11-03T10:00:00.000000001Z hello\n2026-11-03T10:00:01Z second\n")
		frame(2, "2026-11-03T10:00:02Z oops\n")
	case p == "/volumes/osriva_postgres-data":
		_ = json.NewEncoder(w).Encode(map[string]any{"Name": "osriva_postgres-data", "Labels": map[string]string{"com.docker.compose.project": "osriva"}})
	case strings.HasPrefix(p, "/volumes/"):
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"message":"no such volume"}`)
	case p == "/containers/create":
		f.mu.Lock()
		pulled := f.pulled != ""
		f.mu.Unlock()
		if !pulled {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"message":"No such image: app:1"}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.created = body
		f.mu.Unlock()
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"Id":"new1"}`)
	case p == "/images/create":
		f.mu.Lock()
		f.pulled = r.URL.Query().Get("fromImage") + ":" + r.URL.Query().Get("tag")
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Status: Downloaded newer image for app:1"}`+"\n")
	default:
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"message":"unexpected `+p+`"}`)
	}
}

func withFakeDocker(t *testing.T) *fakeDocker {
	f := &fakeDocker{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := newDocker
	newDocker = func() (*dockerClient, error) {
		return &dockerClient{hc: srv.Client(), base: srv.URL + "/v1.41"}, nil
	}
	t.Cleanup(func() { newDocker = old })
	return f
}

func dockerExec(t *testing.T, typ, params string) (any, error) {
	t.Helper()
	return Execute(context.Background(), typ, json.RawMessage(params))
}

func TestDockerInfoAndContainers(t *testing.T) {
	withFakeDocker(t)
	out, err := dockerExec(t, agenttask.DockerInfo, `{}`)
	info := out.(agenttask.DockerInfoResult)
	if err != nil || !info.Available || info.Version != "27.3.1" || info.ContainersRunning != 1 {
		t.Fatalf("info %+v %v", info, err)
	}
	out, err = dockerExec(t, agenttask.DockerContainers, `{}`)
	cts := out.([]agenttask.Container)
	if err != nil || len(cts) != 2 || cts[0].Name != "osriva-postgres-1" || !cts[0].Protected || cts[1].Protected || cts[1].Ports[0].PublicPort != 8080 {
		t.Fatalf("containers %+v %v", cts, err)
	}
}

func TestDockerUnavailable(t *testing.T) {
	old := newDocker
	newDocker = func() (*dockerClient, error) { return nil, errDockerUnavailable }
	defer func() { newDocker = old }()
	out, err := dockerExec(t, agenttask.DockerInfo, `{}`)
	if info := out.(agenttask.DockerInfoResult); err != nil || info.Available || info.Reason == "" {
		t.Fatalf("want honest unavailable result, got %+v %v", info, err)
	}
	if _, err := dockerExec(t, agenttask.DockerContainers, `{}`); err == nil {
		t.Fatal("listing without Docker must fail")
	}
}

func TestDockerActionsAndProtection(t *testing.T) {
	withFakeDocker(t)
	out, err := dockerExec(t, agenttask.DockerContainerAction, `{"id":"web","action":"restart"}`)
	if r := out.(agenttask.ContainerActionResult); err != nil || r.ID != "aaa" || r.State != "running" {
		t.Fatalf("restart %+v %v", r, err)
	}
	if _, err := dockerExec(t, agenttask.DockerContainerAction, `{"id":"web","action":"stop"}`); err != nil {
		t.Fatalf("304 (already stopped) must not fail: %v", err)
	}
	for _, a := range []string{"stop", "remove", "restart"} {
		if _, err := dockerExec(t, agenttask.DockerContainerAction, `{"id":"osriva-postgres-1","action":"`+a+`"}`); err == nil || !strings.Contains(err.Error(), "control plane") {
			t.Fatalf("%s on control plane: %v", a, err)
		}
	}
	if _, err := dockerExec(t, agenttask.DockerContainerAction, `{"id":"ghost","action":"start"}`); err == nil {
		t.Fatal("unknown container must fail")
	}
}

func TestDockerLogsDemux(t *testing.T) {
	withFakeDocker(t)
	out, err := dockerExec(t, agenttask.DockerContainerLogs, `{"id":"web","lines":2}`)
	r := out.(agenttask.ContainerLogsResult)
	if err != nil || len(r.Lines) != 2 || r.Lines[0].Text != "second" || r.Lines[1].Stream != "stderr" || r.Lines[1].Text != "oops" || r.Lines[1].Time == "" {
		t.Fatalf("logs %+v %v", r, err)
	}
}

func TestDockerRunSafeDefaults(t *testing.T) {
	f := withFakeDocker(t)
	out, err := dockerExec(t, agenttask.DockerContainerRun,
		`{"image":"app:1","name":"app","env":{"B":"2","A":"1"},"ports":[{"host_port":8080,"container_port":80},{"host_port":53,"container_port":53,"protocol":"udp","public":true}],"volumes":[{"volume":"app-data","target":"/data"}]}`)
	if r := out.(agenttask.ContainerActionResult); err != nil || r.ID != "new1" || r.State != "running" {
		t.Fatalf("run %+v %v", r, err)
	}
	if f.pulled != "app:1" {
		t.Fatalf("missing image should be pulled with an explicit tag, got %q", f.pulled)
	}
	hc := f.created["HostConfig"].(map[string]any)
	pb := hc["PortBindings"].(map[string]any)
	if ip := pb["80/tcp"].([]any)[0].(map[string]any)["HostIp"]; ip != "127.0.0.1" {
		t.Fatalf("default binding must be loopback, got %v", ip)
	}
	if ip := pb["53/udp"].([]any)[0].(map[string]any)["HostIp"]; ip != "0.0.0.0" {
		t.Fatalf("public binding, got %v", ip)
	}
	if hc["Privileged"] != false || hc["NetworkMode"] != "bridge" {
		t.Fatalf("unsafe host config: %v", hc)
	}
	if m := hc["Mounts"].([]any)[0].(map[string]any); m["Type"] != "volume" || m["Source"] != "app-data" {
		t.Fatalf("mount %v", m)
	}
	if env := f.created["Env"].([]any); env[0] != "A=1" || env[1] != "B=2" {
		t.Fatalf("env %v", env)
	}
}

func TestDockerRunRefusesControlPlaneVolume(t *testing.T) {
	f := withFakeDocker(t)
	_, err := dockerExec(t, agenttask.DockerContainerRun, `{"image":"alpine:3","volumes":[{"volume":"osriva_postgres-data","target":"/steal"}]}`)
	if err == nil || !strings.Contains(err.Error(), "control plane") {
		t.Fatalf("control-plane volume must be refused: %v", err)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/containers/create") {
			t.Fatal("nothing may be created after the refusal")
		}
	}
}

func TestSplitImage(t *testing.T) {
	cases := map[string][2]string{
		"nginx":                       {"nginx", "latest"},
		"nginx:1.27":                  {"nginx", "1.27"},
		"localhost:5000/app":          {"localhost:5000/app", "latest"},
		"localhost:5000/app:v2":       {"localhost:5000/app", "v2"},
		"ghcr.io/o/a@sha256:deadbeef": {"ghcr.io/o/a", "sha256:deadbeef"},
	}
	for in, want := range cases {
		if n, tag := splitImage(in); n != want[0] || tag != want[1] {
			t.Errorf("%s → %s %s", in, n, tag)
		}
	}
}
