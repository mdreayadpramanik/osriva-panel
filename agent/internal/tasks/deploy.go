package tasks

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// Deployments (docs/adr/0007-deployments.md): shallow-clone a Git ref, build
// an image through the Engine API, replace the app's container and restart
// the previous one if the new version does not stay up.

var (
	// buildRoot holds temporary checkouts; replaced in tests.
	buildRoot = "/var/lib/osriva-agent/builds"
	// settleTime is how long a new container must stay up before the old one is removed.
	settleTime = 5 * time.Second
	// fetchSource is replaced in tests (which have no Git server).
	fetchSource = gitFetch
)

const (
	maxContextBytes = 512 << 20
	maxContextFiles = 100_000
	keepAppImages   = 5
	presetFile      = ".osriva.Dockerfile"
)

// ResultError is a failure that still carries a result, such as deployment logs.
type ResultError struct {
	Result any
	Err    error
}

func (e *ResultError) Error() string { return e.Err.Error() }
func (e *ResultError) Unwrap() error { return e.Err }

// deployLog keeps the last MaxDeployLogBytes of output.
type deployLog struct {
	b         []byte
	truncated bool
}

func (l *deployLog) write(s string) {
	l.b = append(l.b, s...)
	if over := len(l.b) - agenttask.MaxDeployLogBytes; over > 0 {
		l.b, l.truncated = l.b[over:], true
	}
}

func (l *deployLog) String() string {
	if l.truncated {
		return "[earlier output truncated]\n" + string(l.b)
	}
	return string(l.b)
}

func (l *deployLog) step(format string, args ...any) {
	l.write(fmt.Sprintf("==> "+format+"\n", args...))
}

func appDeploy(ctx context.Context, c *dockerClient, p agenttask.AppDeployParams) (agenttask.AppDeployResult, error) {
	start := time.Now()
	res := agenttask.AppDeployResult{App: p.App, Image: p.Image}
	log := &deployLog{}
	fail := func(err error) (agenttask.AppDeployResult, error) {
		log.step("FAILED: %v", err)
		res.Logs, res.DurationMilli = log.String(), time.Since(start).Milliseconds()
		return res, &ResultError{Result: res, Err: err}
	}

	if p.Source != nil {
		res.Image = agenttask.AppImage(p.App, p.Deployment)
		if err := os.MkdirAll(buildRoot, 0o700); err != nil {
			return fail(err)
		}
		dir, err := os.MkdirTemp(buildRoot, p.App+"-")
		if err != nil {
			return fail(err)
		}
		defer os.RemoveAll(dir)
		src := filepath.Join(dir, "src")
		log.step("fetching %s (%s)", p.Source.URL, p.Source.Ref)
		if res.Commit, err = fetchSource(ctx, log, *p.Source, src); err != nil {
			return fail(err)
		}
		log.step("commit %s", res.Commit)
		ctxDir, dockerfile, err := prepareBuild(src, *p.Build)
		if err != nil {
			return fail(err)
		}
		log.step("building %s (%s mode)", res.Image, p.Build.Mode)
		if err := dockerBuild(ctx, c, log, ctxDir, dockerfile, res.Image, p.App, p.Deployment); err != nil {
			return fail(err)
		}
	} else {
		log.step("rolling back to %s", p.Image)
		var img struct {
			Config struct{ Labels map[string]string }
		}
		if err := c.json(ctx, http.MethodGet, "/images/"+p.Image+"/json", nil, nil, &img); err != nil {
			return fail(fmt.Errorf("image %s is no longer on this server: %w", p.Image, err))
		}
		if img.Config.Labels[agenttask.AppLabel] != p.App {
			return fail(fmt.Errorf("image %s was not built by OSRIVA for %s", p.Image, p.App))
		}
	}

	id, state, recovered, err := swapContainer(ctx, c, log, p, res.Image)
	res.ContainerID, res.State, res.Recovered = id, state, recovered
	if err != nil {
		return fail(err)
	}
	pruneAppImages(ctx, c, log, p.App, res.Image)
	log.step("done: %s is %s on port %d", agenttask.AppContainer(p.App), state, p.Run.HostPort)
	res.Logs, res.DurationMilli = log.String(), time.Since(start).Milliseconds()
	return res, nil
}

// gitFetch makes a shallow clone of one branch or tag over HTTP(S) only, with
// no user or system Git configuration, no prompts and no submodules.
func gitFetch(ctx context.Context, log *deployLog, s agenttask.GitSource, dest string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is not installed on this server")
	}
	home := filepath.Dir(dest)
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ASKPASS=", "SSH_ASKPASS=", "LC_ALL=C"}
	gitRun := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env, cmd.Dir = env, home
		out, err := cmd.CombinedOutput()
		if len(out) > 64<<10 {
			out = out[len(out)-64<<10:]
		}
		log.write(string(out))
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[len(args)-1], err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := gitRun("-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.http.allow=always",
		"clone", "--quiet", "--depth", "1", "--single-branch", "--no-tags", "--branch", s.Ref, "--", s.URL, dest); err != nil {
		return "", errors.New("could not fetch the repository: check the URL and the branch or tag (private repositories are not supported yet)")
	}
	return gitRun("-C", dest, "rev-parse", "HEAD")
}

const staticDockerfile = `FROM nginx:1.27-alpine
COPY . /usr/share/nginx/html
`

const nodeDockerfile = `FROM node:22-alpine
WORKDIR /app
COPY . .
RUN if [ -f package-lock.json ]; then npm ci --omit=dev; else npm install --omit=dev; fi
ENV NODE_ENV=production
USER node
CMD ["npm", "start"]
`

// prepareBuild resolves the build context inside the checkout and writes the
// preset Dockerfile for static and node builds.
func prepareBuild(src string, b agenttask.BuildSpec) (string, string, error) {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return "", "", err
	}
	ctxDir := root
	if b.Context != "" && b.Context != "." {
		ctxDir, err = filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(b.Context)))
		if err != nil {
			return "", "", fmt.Errorf("build context %q not found in the repository", b.Context)
		}
		if !strings.HasPrefix(ctxDir, root+string(filepath.Separator)) {
			return "", "", fmt.Errorf("build context %q points outside the repository", b.Context)
		}
	}
	if fi, err := os.Stat(ctxDir); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("build context %q is not a directory", b.Context)
	}
	switch b.Mode {
	case agenttask.BuildDockerfile:
		df := b.Dockerfile
		if df == "" {
			df = "Dockerfile"
		}
		fi, err := os.Lstat(filepath.Join(ctxDir, filepath.FromSlash(df)))
		if err != nil || !fi.Mode().IsRegular() {
			return "", "", fmt.Errorf("%s not found in the build context (it must be a regular file, not a link)", df)
		}
		return ctxDir, df, nil
	case agenttask.BuildStatic, agenttask.BuildNode:
		content := staticDockerfile
		if b.Mode == agenttask.BuildNode {
			if _, err := os.Lstat(filepath.Join(ctxDir, "package.json")); err != nil {
				return "", "", errors.New("node mode needs a package.json with a start script")
			}
			content = nodeDockerfile
		}
		path := filepath.Join(ctxDir, presetFile)
		_ = os.Remove(path)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return "", "", err
		}
		return ctxDir, presetFile, nil
	}
	return "", "", fmt.Errorf("unknown build mode %q", b.Mode)
}

// writeContext tars dir without following symbolic links (links are stored as
// links, so nothing outside the checkout is read) and without .git.
func writeContext(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	var total int64
	files := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.IsDir(), info.Mode().IsRegular():
		default:
			return nil // sockets, devices, pipes
		}
		if files++; files > maxContextFiles {
			return fmt.Errorf("the build context has more than %d files", maxContextFiles)
		}
		if total += info.Size(); total > maxContextBytes {
			return fmt.Errorf("the build context is larger than %d MiB", maxContextBytes>>20)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = rel, 0, 0, "", ""
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func dockerBuild(ctx context.Context, c *dockerClient, log *deployLog, ctxDir, dockerfile, image, app, deployment string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeContext(pw, ctxDir)) }()
	labels, _ := json.Marshal(map[string]string{agenttask.AppLabel: app, "dev.osriva.deployment": deployment})
	q := url.Values{"t": {image}, "dockerfile": {dockerfile}, "rm": {"1"}, "forcerm": {"1"}, "labels": {string(labels)}}
	resp, err := c.doRaw(ctx, http.MethodPost, "/build", q, pr, "application/x-tar")
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("build: %w", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m struct {
			Stream string `json:"stream"`
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Error != "":
			log.write(m.Error + "\n")
			return fmt.Errorf("the image build failed: %s", strings.TrimSpace(m.Error))
		case m.Stream != "":
			log.write(m.Stream)
		case m.Status != "":
			log.write(m.Status + "\n")
		}
	}
	return sc.Err()
}

// swapContainer replaces the app's container. The previous one is renamed and
// stopped (it holds the host port), and restarted if the new one fails.
func swapContainer(ctx context.Context, c *dockerClient, log *deployLog, p agenttask.AppDeployParams, image string) (string, string, bool, error) {
	name := agenttask.AppContainer(p.App)
	prev := name + "-previous"
	ours := func(in inspected) bool { return in.Config.Labels[agenttask.AppLabel] == p.App }
	remove := func(id string) error {
		return c.json(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}}, nil, nil)
	}

	if in, err := c.inspect(ctx, prev); err == nil {
		if !ours(in) {
			return "", "", false, fmt.Errorf("a container named %s exists that OSRIVA did not create", prev)
		}
		_ = remove(in.ID) // left over from an interrupted deployment
	}
	old, err := c.inspect(ctx, name)
	hadOld := err == nil
	var de *dockerError
	if err != nil && !(errors.As(err, &de) && de.Status == http.StatusNotFound) {
		return "", "", false, err
	}
	if hadOld {
		if !ours(old) {
			return "", "", false, fmt.Errorf("a container named %s exists that OSRIVA did not create", name)
		}
		log.step("stopping the current version")
		if err := c.json(ctx, http.MethodPost, "/containers/"+old.ID+"/rename", url.Values{"name": {prev}}, nil, nil); err != nil {
			return "", "", false, err
		}
		if err := c.json(ctx, http.MethodPost, "/containers/"+old.ID+"/stop", url.Values{"t": {"10"}}, nil, nil); err != nil && !(errors.As(err, &de) && de.Status == http.StatusNotModified) {
			return "", "", false, err
		}
	}
	restore := func() bool {
		if !hadOld {
			return false
		}
		log.step("restarting the previous version")
		if err := c.json(ctx, http.MethodPost, "/containers/"+old.ID+"/rename", url.Values{"name": {name}}, nil, nil); err != nil {
			log.write("rename back failed: " + err.Error() + "\n")
		}
		if err := c.json(ctx, http.MethodPost, "/containers/"+old.ID+"/start", nil, nil, nil); err != nil {
			log.write("restart failed: " + err.Error() + "\n")
			return false
		}
		return true
	}

	log.step("starting %s", name)
	labels := map[string]string{"dev.osriva.created-by": "osriva-agent", agenttask.AppLabel: p.App, "dev.osriva.deployment": p.Deployment}
	// Most runtimes read PORT; set it to the container port unless the app chose its own.
	env := map[string]string{"PORT": strconv.Itoa(p.Run.ContainerPort)}
	for k, v := range p.Run.Env {
		env[k] = v
	}
	ports := []agenttask.PortMapping{{HostPort: p.Run.HostPort, ContainerPort: p.Run.ContainerPort, Public: p.Run.Public}}
	body := containerBody(image, env, ports, nil, "unless-stopped", labels, nil)
	var created struct{ Id string }
	if err := c.json(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, &created); err != nil {
		return "", "", restore(), err
	}
	failNew := func(cause error) (string, string, bool, error) {
		if tail, err := dockerContainerLogs(ctx, c, agenttask.ContainerLogsParams{ID: created.Id, Lines: 50}); err == nil && len(tail.Lines) > 0 {
			log.step("last output of the new version")
			for _, l := range tail.Lines {
				log.write(l.Text + "\n")
			}
		}
		_ = remove(created.Id)
		return "", "", restore(), cause
	}
	if err := c.json(ctx, http.MethodPost, "/containers/"+created.Id+"/start", nil, nil, nil); err != nil {
		return failNew(fmt.Errorf("the new version did not start: %w", err))
	}
	select {
	case <-ctx.Done():
		return failNew(ctx.Err())
	case <-time.After(settleTime):
	}
	in, err := c.inspect(ctx, created.Id)
	if err != nil {
		return failNew(err)
	}
	if in.State.Status != "running" || in.RestartCount > 0 {
		return failNew(fmt.Errorf("the new version stopped right after starting (%s, exit code %d)", in.State.Status, in.State.ExitCode))
	}
	if hadOld {
		if err := remove(old.ID); err != nil {
			log.write("could not remove the previous container: " + err.Error() + "\n")
		}
	}
	return created.Id, in.State.Status, false, nil
}

type appImage struct {
	ID       string
	RepoTags []string
	Created  int64
}

func (c *dockerClient) appImages(ctx context.Context, app string) ([]appImage, error) {
	f, _ := json.Marshal(map[string][]string{"label": {agenttask.AppLabel + "=" + app}})
	var imgs []appImage
	if err := c.json(ctx, http.MethodGet, "/images/json", url.Values{"filters": {string(f)}}, nil, &imgs); err != nil {
		return nil, err
	}
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Created > imgs[j].Created })
	return imgs, nil
}

// pruneAppImages keeps the newest builds of an app for rollbacks.
func pruneAppImages(ctx context.Context, c *dockerClient, log *deployLog, app, current string) {
	imgs, err := c.appImages(ctx, app)
	if err != nil {
		return
	}
	kept := 0
	for _, img := range imgs {
		isCurrent := false
		for _, t := range img.RepoTags {
			isCurrent = isCurrent || t == current
		}
		if isCurrent || kept < keepAppImages {
			kept++
			continue
		}
		if err := c.json(ctx, http.MethodDelete, "/images/"+img.ID, nil, nil, nil); err == nil {
			log.write(fmt.Sprintf("removed old build %v\n", img.RepoTags))
		}
	}
}

func appRemove(ctx context.Context, c *dockerClient, app string) (agenttask.AppRemoveResult, error) {
	res := agenttask.AppRemoveResult{App: app}
	for _, n := range []string{agenttask.AppContainer(app), agenttask.AppContainer(app) + "-previous"} {
		in, err := c.inspect(ctx, n)
		if err != nil {
			continue
		}
		if in.Config.Labels[agenttask.AppLabel] != app {
			return res, fmt.Errorf("a container named %s exists that OSRIVA did not create; it was left alone", n)
		}
		if err := c.json(ctx, http.MethodDelete, "/containers/"+in.ID, url.Values{"force": {"1"}}, nil, nil); err != nil {
			return res, err
		}
		res.ContainerRemoved = true
	}
	imgs, err := c.appImages(ctx, app)
	if err != nil {
		return res, err
	}
	for _, img := range imgs {
		if err := c.json(ctx, http.MethodDelete, "/images/"+img.ID, url.Values{"force": {"1"}}, nil, nil); err == nil {
			res.ImagesRemoved++
		}
	}
	return res, nil
}

func appTaskRun(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	c, err := newDocker()
	if err != nil {
		return nil, err
	}
	if typ == agenttask.AppRemove {
		var p agenttask.AppRemoveParams
		_ = json.Unmarshal(params, &p)
		return appRemove(ctx, c, p.App)
	}
	var p agenttask.AppDeployParams
	_ = json.Unmarshal(params, &p)
	return appDeploy(ctx, c, p)
}
