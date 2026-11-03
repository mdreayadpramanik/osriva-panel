package tasks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// The agent talks to the local Docker Engine API over its Unix socket. The
// socket is never exposed beyond this host. Docker on Windows (named pipe)
// is not supported yet.

const (
	dockerAPIVersion = "v1.41" // Docker Engine 20.10+
	maxDockerBody    = 16 << 20
	maxLogLineBytes  = 4096
)

var errDockerUnavailable = errors.New("docker is not available")

type dockerClient struct {
	hc   *http.Client
	base string // e.g. http://docker/v1.41
}

// newDocker is replaced in tests with a client for a fake Engine API.
var newDocker = func() (*dockerClient, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("%w: Docker on Windows is not supported yet", ErrUnsupported)
	}
	sock := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		sock = strings.TrimPrefix(h, "unix://")
	}
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("%w: no Docker socket at %s", errDockerUnavailable, sock)
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns: 2,
	}
	return &dockerClient{hc: &http.Client{Transport: tr}, base: "http://docker/" + dockerAPIVersion}, nil
}

type dockerError struct {
	Status  int
	Message string
}

func (e *dockerError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

func (c *dockerClient) do(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	if body == nil {
		return c.doRaw(ctx, method, path, q, nil, "")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.doRaw(ctx, method, path, q, bytes.NewReader(b), "application/json")
}

// doRaw sends an arbitrary body (for example a tar build context).
func (c *dockerClient) doRaw(ctx context.Context, method, path string, q url.Values, rd io.Reader, contentType string) (*http.Response, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDockerUnavailable, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return nil, &dockerError{Status: resp.StatusCode, Message: e.Message}
	}
	return resp, nil
}

func (c *dockerClient) json(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.do(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDockerBody))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxDockerBody)).Decode(out)
}

func dockerTaskRun(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	c, err := newDocker()
	if typ == agenttask.DockerInfo {
		return dockerInfo(ctx, c, err), nil
	}
	if err != nil {
		return nil, err
	}
	switch typ {
	case agenttask.DockerContainers:
		return dockerContainers(ctx, c)
	case agenttask.DockerContainerAction:
		var p agenttask.ContainerActionParams
		_ = json.Unmarshal(params, &p)
		return dockerContainerAction(ctx, c, p)
	case agenttask.DockerContainerLogs:
		var p agenttask.ContainerLogsParams
		_ = json.Unmarshal(params, &p)
		if p.Lines == 0 {
			p.Lines = 200
		}
		return dockerContainerLogs(ctx, c, p)
	case agenttask.DockerContainerRun:
		var p agenttask.ContainerRunParams
		_ = json.Unmarshal(params, &p)
		return dockerRun(ctx, c, p)
	case agenttask.DockerImages:
		return dockerImages(ctx, c)
	case agenttask.DockerImagePull:
		var p agenttask.ImageParams
		_ = json.Unmarshal(params, &p)
		return dockerPull(ctx, c, p.Image)
	case agenttask.DockerImageRemove:
		var p agenttask.ImageParams
		_ = json.Unmarshal(params, &p)
		return dockerImageRemove(ctx, c, p.Image)
	case agenttask.DockerImagesPrune:
		return dockerPrune(ctx, c)
	case agenttask.DockerVolumes:
		return dockerVolumes(ctx, c)
	case agenttask.DockerNetworks:
		return dockerNetworks(ctx, c)
	}
	return nil, fmt.Errorf("%w: %s", agenttask.ErrInvalid, typ)
}

func dockerInfo(ctx context.Context, c *dockerClient, cerr error) agenttask.DockerInfoResult {
	if cerr != nil {
		return agenttask.DockerInfoResult{Reason: cerr.Error()}
	}
	var v struct {
		Version, APIVersion, Os string
	}
	if err := c.json(ctx, http.MethodGet, "/version", nil, nil, &v); err != nil {
		return agenttask.DockerInfoResult{Reason: err.Error()}
	}
	var i struct {
		Containers, ContainersRunning, Images, NCPU int
		Driver                                      string
		MemTotal                                    int64
		OperatingSystem                             string
	}
	if err := c.json(ctx, http.MethodGet, "/info", nil, nil, &i); err != nil {
		return agenttask.DockerInfoResult{Reason: err.Error()}
	}
	return agenttask.DockerInfoResult{
		Available: true, Version: v.Version, APIVersion: v.APIVersion, OS: i.OperatingSystem,
		Driver: i.Driver, Containers: i.Containers, ContainersRunning: i.ContainersRunning,
		Images: i.Images, CPUs: i.NCPU, MemoryBytes: i.MemTotal,
	}
}

func protected(labels map[string]string) bool { return labels[agenttask.ControlPlaneLabel] == "true" }

func unixTime(s int64) string {
	if s <= 0 {
		return ""
	}
	return time.Unix(s, 0).UTC().Format(time.RFC3339)
}

func dockerContainers(ctx context.Context, c *dockerClient) ([]agenttask.Container, error) {
	var raw []struct {
		ID, Image, State, Status string
		Names                    []string
		Created                  int64
		Labels                   map[string]string
		Ports                    []struct {
			IP                      string
			PrivatePort, PublicPort int
			Type                    string
		}
	}
	if err := c.json(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]agenttask.Container, 0, len(raw))
	for _, r := range raw {
		ct := agenttask.Container{
			ID: r.ID, Image: r.Image, State: r.State, Status: r.Status, Created: unixTime(r.Created),
			ComposeProject: r.Labels["com.docker.compose.project"], Protected: protected(r.Labels),
			Ports: []agenttask.ContainerPort{},
		}
		if len(r.Names) > 0 {
			ct.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		for _, p := range r.Ports {
			ct.Ports = append(ct.Ports, agenttask.ContainerPort{IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Type: p.Type})
		}
		out = append(out, ct)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type inspected struct {
	ID           string
	Name         string
	Image        string
	RestartCount int
	State        struct {
		Status   string
		ExitCode int
		Health   *struct{ Status string }
	}
	Config struct {
		Tty    bool
		Labels map[string]string
		Env    []string
	}
}

func (c *dockerClient) inspect(ctx context.Context, ref string) (inspected, error) {
	var in inspected
	err := c.json(ctx, http.MethodGet, "/containers/"+url.PathEscape(ref)+"/json", nil, nil, &in)
	in.Name = strings.TrimPrefix(in.Name, "/")
	return in, err
}

func dockerContainerAction(ctx context.Context, c *dockerClient, p agenttask.ContainerActionParams) (agenttask.ContainerActionResult, error) {
	res := agenttask.ContainerActionResult{ID: p.ID, Action: p.Action}
	in, err := c.inspect(ctx, p.ID)
	if err != nil {
		return res, err
	}
	res.ID, res.Name = in.ID, in.Name
	if protected(in.Config.Labels) {
		return res, fmt.Errorf("%w: %s is part of the OSRIVA control plane; manage it with osriva-install.sh", agenttask.ErrInvalid, in.Name)
	}
	path := "/containers/" + url.PathEscape(in.ID)
	switch p.Action {
	case "start", "stop", "restart":
		err = c.json(ctx, http.MethodPost, path+"/"+p.Action, nil, nil, nil)
		var de *dockerError
		if errors.As(err, &de) && de.Status == http.StatusNotModified {
			err = nil // already in the requested state
		}
	case "remove":
		// No force: a running container must be stopped first. Anonymous volumes are kept.
		err = c.json(ctx, http.MethodDelete, path, nil, nil, nil)
	}
	if err != nil || p.Action == "remove" {
		return res, err
	}
	after, err := c.inspect(ctx, in.ID)
	res.State = after.State.Status
	return res, err
}

func dockerContainerLogs(ctx context.Context, c *dockerClient, p agenttask.ContainerLogsParams) (agenttask.ContainerLogsResult, error) {
	res := agenttask.ContainerLogsResult{ID: p.ID, Lines: []agenttask.ContainerLogLine{}}
	in, err := c.inspect(ctx, p.ID)
	if err != nil {
		return res, err
	}
	res.ID = in.ID
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(p.Lines)}}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(in.ID)+"/logs", q, nil)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxDockerBody)
	add := func(stream string, data []byte) {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), maxDockerBody)
		for sc.Scan() {
			res.Lines = append(res.Lines, logLine(stream, sc.Text()))
		}
	}
	if in.Config.Tty {
		data, err := io.ReadAll(body)
		if err != nil {
			return res, err
		}
		add("stdout", data)
	} else {
		// Multiplexed stream: 8-byte header (stream type, 3 zero bytes, big-endian length).
		var hdr [8]byte
		for {
			if _, err := io.ReadFull(body, hdr[:]); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return res, fmt.Errorf("reading log stream: %w", err)
			}
			n := binary.BigEndian.Uint32(hdr[4:])
			frame := make([]byte, n)
			if _, err := io.ReadFull(body, frame); err != nil {
				return res, fmt.Errorf("reading log stream: %w", err)
			}
			stream := "stdout"
			if hdr[0] == 2 {
				stream = "stderr"
			}
			add(stream, frame)
		}
	}
	if len(res.Lines) > p.Lines {
		res.Lines = res.Lines[len(res.Lines)-p.Lines:]
	}
	return res, nil
}

func logLine(stream, s string) agenttask.ContainerLogLine {
	l := agenttask.ContainerLogLine{Stream: stream, Text: s}
	if ts, rest, ok := strings.Cut(s, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			l.Time, l.Text = t.UTC().Format(time.RFC3339Nano), rest
		}
	}
	if len(l.Text) > maxLogLineBytes {
		l.Text = l.Text[:maxLogLineBytes]
	}
	return l
}

// splitImage turns "name[:tag][@digest]" into the fromImage/tag pair the API
// expects; without a tag the API would pull every tag.
func splitImage(ref string) (string, string) {
	if name, digest, ok := strings.Cut(ref, "@"); ok {
		return name, digest
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func dockerPull(ctx context.Context, c *dockerClient, ref string) (agenttask.ImagePullResult, error) {
	res := agenttask.ImagePullResult{Image: ref}
	name, tag := splitImage(ref)
	resp, err := c.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {name}, "tag": {tag}}, nil)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	for {
		var m struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return res, fmt.Errorf("reading pull progress: %w", err)
		}
		if m.Error != "" {
			return res, fmt.Errorf("docker pull: %s", m.Error)
		}
		if m.Status != "" {
			res.Status = m.Status
		}
	}
	return res, nil
}

// guardVolumes refuses named volumes that belong to the OSRIVA control plane
// (for example its PostgreSQL data): mounting them would expose the panel's database.
func guardVolumes(ctx context.Context, c *dockerClient, vols []agenttask.VolumeMount) error {
	if len(vols) == 0 {
		return nil
	}
	var cts []struct {
		Labels map[string]string
		Mounts []struct{ Type, Name string }
	}
	if err := c.json(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &cts); err != nil {
		return err
	}
	used, projects := map[string]bool{}, map[string]bool{}
	for _, ct := range cts {
		if !protected(ct.Labels) {
			continue
		}
		if pr := ct.Labels["com.docker.compose.project"]; pr != "" {
			projects[pr] = true
		}
		for _, m := range ct.Mounts {
			if m.Type == "volume" {
				used[m.Name] = true
			}
		}
	}
	for _, v := range vols {
		var vol struct{ Labels map[string]string }
		err := c.json(ctx, http.MethodGet, "/volumes/"+url.PathEscape(v.Volume), nil, nil, &vol)
		var de *dockerError
		if errors.As(err, &de) && de.Status == http.StatusNotFound {
			err = nil // a new volume
		}
		if err != nil {
			return err
		}
		if used[v.Volume] || projects[vol.Labels["com.docker.compose.project"]] || protected(vol.Labels) {
			return fmt.Errorf("%w: volume %s belongs to the OSRIVA control plane", agenttask.ErrInvalid, v.Volume)
		}
	}
	return nil
}

func dockerRun(ctx context.Context, c *dockerClient, p agenttask.ContainerRunParams) (agenttask.ContainerActionResult, error) {
	res := agenttask.ContainerActionResult{Action: "run", Name: p.Name}
	if err := guardVolumes(ctx, c, p.Volumes); err != nil {
		return res, err
	}
	body := containerBody(p.Image, p.Env, p.Ports, p.Volumes, p.Restart, map[string]string{"dev.osriva.created-by": "osriva-agent"}, p.Command)
	q := url.Values{}
	if p.Name != "" {
		q.Set("name", p.Name)
	}
	var created struct{ Id string }
	err := c.json(ctx, http.MethodPost, "/containers/create", q, body, &created)
	var de *dockerError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		// Image not present locally: pull it, then try once more.
		if _, err = dockerPull(ctx, c, p.Image); err != nil {
			return res, err
		}
		err = c.json(ctx, http.MethodPost, "/containers/create", q, body, &created)
	}
	if err != nil {
		return res, err
	}
	res.ID = created.Id
	if err := c.json(ctx, http.MethodPost, "/containers/"+url.PathEscape(created.Id)+"/start", nil, nil, nil); err != nil {
		return res, fmt.Errorf("container created but did not start: %w", err)
	}
	in, err := c.inspect(ctx, created.Id)
	res.Name, res.State = in.Name, in.State.Status
	return res, err
}

func dockerImages(ctx context.Context, c *dockerClient) ([]agenttask.Image, error) {
	var raw []struct {
		Id         string
		RepoTags   []string
		Size       int64
		Created    int64
		Containers int
	}
	if err := c.json(ctx, http.MethodGet, "/images/json", nil, nil, &raw); err != nil {
		return nil, err
	}
	inUse := map[string]bool{}
	var cts []struct{ ImageID string }
	if err := c.json(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &cts); err != nil {
		return nil, err
	}
	for _, ct := range cts {
		inUse[ct.ImageID] = true
	}
	out := make([]agenttask.Image, 0, len(raw))
	for _, r := range raw {
		tags := []string{}
		for _, t := range r.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		out = append(out, agenttask.Image{ID: r.Id, Tags: tags, Size: r.Size, Created: unixTime(r.Created), InUse: inUse[r.Id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}

func dockerImageRemove(ctx context.Context, c *dockerClient, ref string) (map[string]any, error) {
	// No force: Docker refuses to remove an image that a container uses. The
	// reference was validated to path-safe characters (it may contain "/").
	var deleted []map[string]string
	if err := c.json(ctx, http.MethodDelete, "/images/"+ref, nil, nil, &deleted); err != nil {
		return nil, err
	}
	return map[string]any{"image": ref, "removed": len(deleted)}, nil
}

func dockerPrune(ctx context.Context, c *dockerClient) (agenttask.ImagesPruneResult, error) {
	// Dangling (untagged, unused) images only.
	var r struct {
		ImagesDeleted  []map[string]string
		SpaceReclaimed int64
	}
	q := url.Values{"filters": {`{"dangling":["true"]}`}}
	err := c.json(ctx, http.MethodPost, "/images/prune", q, nil, &r)
	return agenttask.ImagesPruneResult{Deleted: len(r.ImagesDeleted), SpaceReclaimed: r.SpaceReclaimed}, err
}

func dockerVolumes(ctx context.Context, c *dockerClient) ([]agenttask.Volume, error) {
	var r struct {
		Volumes []struct{ Name, Driver, Mountpoint string }
	}
	if err := c.json(ctx, http.MethodGet, "/volumes", nil, nil, &r); err != nil {
		return nil, err
	}
	out := make([]agenttask.Volume, 0, len(r.Volumes))
	for _, v := range r.Volumes {
		out = append(out, agenttask.Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func dockerNetworks(ctx context.Context, c *dockerClient) ([]agenttask.Network, error) {
	var raw []struct{ Id, Name, Driver, Scope string }
	if err := c.json(ctx, http.MethodGet, "/networks", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]agenttask.Network, 0, len(raw))
	for _, n := range raw {
		out = append(out, agenttask.Network{ID: n.Id, Name: n.Name, Driver: n.Driver, Scope: n.Scope})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// containerBody is the create request for every container the agent starts:
// never privileged, bridge network, no-new-privileges, named volumes only and
// ports on 127.0.0.1 unless marked public.
func containerBody(image string, envMap map[string]string, ports []agenttask.PortMapping, vols []agenttask.VolumeMount, restart string, labels map[string]string, cmd []string) map[string]any {
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	type binding struct{ HostIp, HostPort string }
	exposed := map[string]struct{}{}
	bindings := map[string][]binding{}
	for _, m := range ports {
		proto := m.Protocol
		if proto == "" {
			proto = "tcp"
		}
		key := fmt.Sprintf("%d/%s", m.ContainerPort, proto)
		ip := "127.0.0.1"
		if m.Public {
			ip = "0.0.0.0"
		}
		exposed[key] = struct{}{}
		bindings[key] = append(bindings[key], binding{HostIp: ip, HostPort: strconv.Itoa(m.HostPort)})
	}
	type mount struct{ Type, Source, Target string }
	mounts := make([]mount, 0, len(vols))
	for _, v := range vols {
		mounts = append(mounts, mount{Type: "volume", Source: v.Volume, Target: v.Target})
	}
	if restart == "" {
		restart = "no"
	}
	body := map[string]any{
		"Image":        image,
		"Env":          env,
		"ExposedPorts": exposed,
		"Labels":       labels,
		"HostConfig": map[string]any{
			"PortBindings":  bindings,
			"Mounts":        mounts,
			"RestartPolicy": map[string]string{"Name": restart},
			"Privileged":    false,
			"NetworkMode":   "bridge",
			"SecurityOpt":   []string{"no-new-privileges"},
		},
	}
	if len(cmd) > 0 {
		body["Cmd"] = cmd
	}
	return body
}
