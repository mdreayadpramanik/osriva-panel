package tasks

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

type fakeCt struct {
	ID, Name, Image, State string
	Labels                 map[string]string
	Body                   map[string]any
}

// fakeEngine is a small stateful Docker Engine: containers, images and builds.
type fakeEngine struct {
	mu         sync.Mutex
	cts        map[string]*fakeCt // by ID
	images     map[string]map[string]string
	n          int
	buildFiles map[string]string // name -> content or "-> target" for links
	buildFail  bool
	crashImage string // containers of this image exit at once
	stops      int
}

func (f *fakeEngine) byRef(ref string) *fakeCt {
	if c, ok := f.cts[ref]; ok {
		return c
	}
	for _, c := range f.cts {
		if c.Name == ref {
			return c
		}
	}
	return nil
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v1.41")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	notFound := func() { w.WriteHeader(404); _, _ = io.WriteString(w, `{"message":"no such object"}`) }
	switch {
	case p == "/build":
		f.buildFiles = map[string]string{}
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Typeflag == tar.TypeSymlink {
				f.buildFiles[h.Name] = "-> " + h.Linkname
				continue
			}
			b, _ := io.ReadAll(tr)
			f.buildFiles[h.Name] = string(b)
		}
		if f.buildFail {
			_, _ = io.WriteString(w, `{"stream":"Step 1/2 : FROM x\n"}`+"\n"+`{"error":"The command '/bin/sh -c false' returned a non-zero code: 1"}`+"\n")
			return
		}
		var labels map[string]string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("labels")), &labels)
		f.images[r.URL.Query().Get("t")] = labels
		_, _ = io.WriteString(w, `{"stream":"Step 1/2 : FROM nginx\n"}`+"\n"+`{"stream":"Successfully built\n"}`+"\n")
	case p == "/containers/create":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := r.URL.Query().Get("name")
		if f.byRef(name) != nil {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"message":"name in use"}`)
			return
		}
		f.n++
		labels := map[string]string{}
		for k, v := range body["Labels"].(map[string]any) {
			labels[k] = v.(string)
		}
		c := &fakeCt{ID: fmt.Sprintf("c%d", f.n), Name: name, Image: body["Image"].(string), State: "created", Labels: labels, Body: body}
		f.cts[c.ID] = c
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": c.ID})
	case len(parts) >= 2 && parts[0] == "containers" && (len(parts) >= 3 || r.Method == http.MethodDelete):
		c := f.byRef(parts[1])
		if c == nil {
			notFound()
			return
		}
		switch {
		case r.Method == http.MethodDelete:
			delete(f.cts, c.ID)
			w.WriteHeader(204)
		case parts[2] == "json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": c.ID, "Name": "/" + c.Name, "Image": c.Image,
				"State": map[string]any{"Status": c.State, "ExitCode": map[bool]int{true: 1}[c.State == "exited"]}, "Config": map[string]any{"Labels": c.Labels}})
		case parts[2] == "rename":
			c.Name = r.URL.Query().Get("name")
			w.WriteHeader(204)
		case parts[2] == "stop":
			f.stops++
			c.State = "exited"
			w.WriteHeader(204)
		case parts[2] == "start":
			c.State = "running"
			if c.Image == f.crashImage {
				c.State = "exited"
			}
			w.WriteHeader(204)
		case parts[2] == "logs":
			w.WriteHeader(200) // empty multiplexed stream
		default:
			notFound()
		}
	case len(parts) == 1 && parts[0] == "containers":
		notFound()
	case p == "/images/json":
		out := []map[string]any{}
		for tag, labels := range f.images {
			if labels[agenttask.AppLabel] != "" && strings.Contains(r.URL.Query().Get("filters"), labels[agenttask.AppLabel]) {
				out = append(out, map[string]any{"Id": "id-" + tag, "RepoTags": []string{tag}, "Created": 1})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		labels, ok := f.images[strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/json")]
		if !ok {
			notFound()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": labels}})
	case strings.HasPrefix(p, "/images/") && r.Method == http.MethodDelete:
		delete(f.images, strings.TrimPrefix(strings.TrimPrefix(p, "/images/"), "id-"))
		_, _ = io.WriteString(w, `[]`)
	default:
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"message":"unexpected `+r.Method+" "+p+`"}`)
	}
}

func setupDeploy(t *testing.T) (*fakeEngine, string) {
	t.Helper()
	f := &fakeEngine{cts: map[string]*fakeCt{}, images: map[string]map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldD, oldF, oldR, oldS := newDocker, fetchSource, buildRoot, settleTime
	newDocker = func() (*dockerClient, error) { return &dockerClient{hc: srv.Client(), base: srv.URL + "/v1.41"}, nil }
	buildRoot, settleTime = t.TempDir(), 0
	repo := t.TempDir()
	fetchSource = func(_ context.Context, _ *deployLog, _ agenttask.GitSource, dest string) (string, error) {
		return "c0ffee", os.CopyFS(dest, os.DirFS(repo))
	}
	t.Cleanup(func() { newDocker, fetchSource, buildRoot, settleTime = oldD, oldF, oldR, oldS })
	return f, repo
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func deploy(t *testing.T, dep string, mutate func(*agenttask.AppDeployParams)) (agenttask.AppDeployResult, error) {
	t.Helper()
	p := agenttask.AppDeployParams{App: "web", Deployment: dep,
		Source: &agenttask.GitSource{URL: "https://example.com/web.git", Ref: "main"},
		Build:  &agenttask.BuildSpec{Mode: agenttask.BuildStatic},
		Run:    agenttask.AppRunSpec{ContainerPort: 80, HostPort: 8080, Env: map[string]string{"A": "1"}}}
	if mutate != nil {
		mutate(&p)
	}
	raw, _ := json.Marshal(p)
	out, err := Execute(context.Background(), agenttask.AppDeploy, raw)
	if out == nil {
		var re *ResultError
		if errors.As(err, &re) {
			return re.Result.(agenttask.AppDeployResult), err
		}
		return agenttask.AppDeployResult{}, err
	}
	return out.(agenttask.AppDeployResult), err
}

func TestDeployStaticAndRedeploy(t *testing.T) {
	f, repo := setupDeploy(t)
	writeFile(t, filepath.Join(repo, "index.html"), "<h1>v1</h1>")
	writeFile(t, filepath.Join(repo, ".git", "config"), "secret remote")
	if runtime.GOOS != "windows" {
		if err := os.Symlink("/etc/passwd", filepath.Join(repo, "passwd")); err != nil {
			t.Fatal(err)
		}
	}

	r, err := deploy(t, "d1", nil)
	if err != nil || r.State != "running" || r.Commit != "c0ffee" || r.Image != "osriva-app/web:d1" || !strings.Contains(r.Logs, "commit c0ffee") {
		t.Fatalf("deploy 1: %+v %v", r, err)
	}
	if f.buildFiles["index.html"] != "<h1>v1</h1>" || !strings.Contains(f.buildFiles[presetFile], "nginx") {
		t.Fatalf("build context: %v", f.buildFiles)
	}
	for name := range f.buildFiles {
		if strings.HasPrefix(name, ".git") {
			t.Fatalf(".git must not be sent to the builder: %s", name)
		}
	}
	if runtime.GOOS != "windows" && f.buildFiles["passwd"] != "-> /etc/passwd" {
		t.Fatalf("symlinks must be stored as links, never followed: %q", f.buildFiles["passwd"])
	}
	c := f.byRef("osriva-app-web")
	hc := c.Body["HostConfig"].(map[string]any)
	if c.Labels[agenttask.AppLabel] != "web" || hc["Privileged"] != false ||
		hc["PortBindings"].(map[string]any)["80/tcp"].([]any)[0].(map[string]any)["HostIp"] != "127.0.0.1" {
		t.Fatalf("container: %+v", c)
	}

	r, err = deploy(t, "d2", nil)
	if err != nil || r.State != "running" || r.Recovered {
		t.Fatalf("deploy 2: %+v %v", r, err)
	}
	if len(f.cts) != 1 || f.byRef("osriva-app-web").Image != "osriva-app/web:d2" {
		t.Fatalf("old container must be replaced: %+v", f.cts)
	}
}

func TestDeployFailureRestoresPrevious(t *testing.T) {
	f, repo := setupDeploy(t)
	writeFile(t, filepath.Join(repo, "index.html"), "ok")
	if _, err := deploy(t, "good", nil); err != nil {
		t.Fatal(err)
	}
	f.crashImage = "osriva-app/web:bad"
	r, err := deploy(t, "bad", nil)
	if err == nil || !r.Recovered || !strings.Contains(r.Logs, "restarting the previous version") {
		t.Fatalf("crashing version: %+v %v", r, err)
	}
	cur := f.byRef("osriva-app-web")
	if len(f.cts) != 1 || cur.Image != "osriva-app/web:good" || cur.State != "running" {
		t.Fatalf("previous version must be back: %+v", f.cts)
	}

	// A failing build never touches the running version.
	f.buildFail, f.stops = true, 0
	r, err = deploy(t, "broken", nil)
	if err == nil || !strings.Contains(err.Error(), "build failed") || !strings.Contains(r.Logs, "non-zero code") || f.stops != 0 {
		t.Fatalf("build failure: %+v %v stops=%d", r, err, f.stops)
	}

	// Rollback to an earlier build.
	f.buildFail = false
	r, err = deploy(t, "rb1", func(p *agenttask.AppDeployParams) { p.Source, p.Build, p.Image = nil, nil, "osriva-app/web:good" })
	if err != nil || r.Image != "osriva-app/web:good" {
		t.Fatalf("rollback: %+v %v", r, err)
	}
	if _, err := deploy(t, "rb2", func(p *agenttask.AppDeployParams) { p.Source, p.Build, p.Image = nil, nil, "osriva-app/web:missing" }); err == nil {
		t.Fatal("rollback to a missing image must fail")
	}
}

func TestDeployRefusesForeignContainer(t *testing.T) {
	f, repo := setupDeploy(t)
	writeFile(t, filepath.Join(repo, "index.html"), "ok")
	f.cts["x"] = &fakeCt{ID: "x", Name: "osriva-app-web", Image: "someone/else", State: "running", Labels: map[string]string{}}
	if _, err := deploy(t, "d1", nil); err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("foreign container: %v", err)
	}
	if f.cts["x"].State != "running" {
		t.Fatal("foreign container was touched")
	}
	raw, _ := json.Marshal(agenttask.AppRemoveParams{App: "web"})
	if _, err := Execute(context.Background(), agenttask.AppRemove, raw); err == nil {
		t.Fatal("app.remove must not delete a foreign container")
	}
}

func TestDeployNodeAndDockerfileModes(t *testing.T) {
	f, repo := setupDeploy(t)
	if _, err := deploy(t, "n1", func(p *agenttask.AppDeployParams) { p.Build.Mode = agenttask.BuildNode }); err == nil || !strings.Contains(err.Error(), "package.json") {
		t.Fatalf("node without package.json: %v", err)
	}
	writeFile(t, filepath.Join(repo, "app", "package.json"), `{"scripts":{"start":"node server.js"}}`)
	writeFile(t, filepath.Join(repo, "app", "Dockerfile.prod"), "FROM scratch")
	if _, err := deploy(t, "n2", func(p *agenttask.AppDeployParams) {
		p.Build = &agenttask.BuildSpec{Mode: agenttask.BuildNode, Context: "app"}
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.buildFiles[presetFile], "npm") || f.buildFiles["package.json"] == "" {
		t.Fatalf("node build context: %v", f.buildFiles)
	}
	if _, err := deploy(t, "d3", func(p *agenttask.AppDeployParams) {
		p.Build = &agenttask.BuildSpec{Mode: agenttask.BuildDockerfile, Context: "app", Dockerfile: "Dockerfile.prod"}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := deploy(t, "d4", func(p *agenttask.AppDeployParams) { p.Build = &agenttask.BuildSpec{Mode: agenttask.BuildDockerfile} }); err == nil {
		t.Fatal("missing Dockerfile must fail")
	}
	raw, _ := json.Marshal(agenttask.AppRemoveParams{App: "web"})
	out, err := Execute(context.Background(), agenttask.AppRemove, raw)
	if r := out.(agenttask.AppRemoveResult); err != nil || !r.ContainerRemoved || r.ImagesRemoved == 0 || len(f.cts) != 0 {
		t.Fatalf("remove: %+v %v", out, err)
	}
}

func TestPrepareBuildConfinement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	src := t.TempDir()
	if err := os.Symlink("/etc", filepath.Join(src, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareBuild(src, agenttask.BuildSpec{Mode: agenttask.BuildStatic, Context: "escape"}); err == nil {
		t.Fatal("a context symlinked outside the checkout was accepted")
	}
	writeFile(t, filepath.Join(src, "real"), "x")
	if err := os.Symlink(filepath.Join(src, "real"), filepath.Join(src, "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareBuild(src, agenttask.BuildSpec{Mode: agenttask.BuildDockerfile}); err == nil {
		t.Fatal("a Dockerfile symlink was accepted")
	}
}

func TestDeployLogKeepsTail(t *testing.T) {
	l := &deployLog{}
	l.write(strings.Repeat("a", agenttask.MaxDeployLogBytes))
	l.write("END")
	s := l.String()
	if !strings.HasPrefix(s, "[earlier output truncated]") || !strings.HasSuffix(s, "END") || len(l.b) != agenttask.MaxDeployLogBytes {
		t.Fatalf("log tail: len=%d", len(l.b))
	}
}

// TestGitFetchDumbHTTP runs the real git client against a plain static HTTP
// server, which cannot serve shallow clones.
func TestGitFetchDumbHTTP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	work := filepath.Join(dir, "work")
	run("init", "-q", "-b", "main", work)
	writeFile(t, filepath.Join(work, "index.html"), "hi")
	run("-C", work, "add", "-A")
	run("-C", work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "v1")
	run("clone", "-q", "--bare", work, filepath.Join(dir, "site.git"))
	run("-C", filepath.Join(dir, "site.git"), "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	log := &deployLog{}
	dest := filepath.Join(t.TempDir(), "src")
	commit, err := gitFetch(context.Background(), log, agenttask.GitSource{URL: srv.URL + "/site.git", Ref: "main"}, dest)
	if err != nil || commit != run("-C", work, "rev-parse", "HEAD") {
		t.Fatalf("fetch: %q %v\n%s", commit, err, log.String())
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "index.html")); string(b) != "hi" {
		t.Fatal("checkout content")
	}
	if _, err := gitFetch(context.Background(), &deployLog{}, agenttask.GitSource{URL: srv.URL + "/site.git", Ref: "nope"}, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("unknown branch must fail")
	}
}

// The agent runs with umask 0077: images must still be readable by non-root
// users inside the container (found by the Ubuntu system test: nginx 403).
func TestContextPermissionsNormalised(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "site", "index.html"), "x")
	_ = os.Chmod(filepath.Join(dir, "site", "index.html"), 0o600)
	_ = os.Chmod(filepath.Join(dir, "site"), 0o700)
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeContext(pw, dir)) }()
	tr := tar.NewReader(pr)
	modes := map[string]int64{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		modes[h.Name] = h.Mode
	}
	if modes["site/"] != 0o755 || modes["site/index.html"] != 0o644 {
		t.Fatalf("modes: %v", modes)
	}
}
