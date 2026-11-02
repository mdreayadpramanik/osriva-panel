package agenttask

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

const (
	DockerInfo            = "docker.info"
	DockerContainers      = "docker.containers"
	DockerContainerAction = "docker.container.action"
	DockerContainerLogs   = "docker.container.logs"
	DockerContainerRun    = "docker.container.run"
	DockerImages          = "docker.images"
	DockerImagePull       = "docker.image.pull"
	DockerImageRemove     = "docker.image.remove"
	DockerImagesPrune     = "docker.images.prune"
	DockerVolumes         = "docker.volumes"
	DockerNetworks        = "docker.networks"

	// ControlPlaneLabel marks OSRIVA's own containers; the agent refuses to act on them.
	ControlPlaneLabel = "dev.osriva.control-plane"
)

func dockerTask(typ string) bool { return strings.HasPrefix(typ, "docker.") }

func dockerReadOnly(typ string) bool {
	switch typ {
	case DockerInfo, DockerContainers, DockerContainerLogs, DockerImages, DockerVolumes, DockerNetworks:
		return true
	}
	return false
}

// dockerReauth: destructive or privileged Docker operations need a fresh password.
func dockerReauth(typ string, params json.RawMessage) bool {
	switch typ {
	case DockerContainerRun, DockerImageRemove, DockerImagesPrune:
		return true
	case DockerContainerAction:
		var p ContainerActionParams
		_ = json.Unmarshal(params, &p)
		return p.Action == "stop" || p.Action == "remove"
	}
	return false
}

var (
	// ContainerRef is a container ID or name.
	ContainerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	imageRef     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,299}$`)
	volumeName   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	envKey       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// ValidImage accepts Docker image references (name[:tag][@digest]) without "..".
func ValidImage(s string) bool { return imageRef.MatchString(s) && !strings.Contains(s, "..") }

type ContainerActionParams struct {
	ID     string `json:"id"`
	Action string `json:"action"` // start | stop | restart | remove
}

type ContainerLogsParams struct {
	ID    string `json:"id"`
	Lines int    `json:"lines,omitempty"`
}

type ImageParams struct {
	Image string `json:"image"`
}

type PortMapping struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol,omitempty"` // tcp (default) | udp
	// Public binds on all interfaces; by default the port is bound to 127.0.0.1 only.
	Public bool `json:"public,omitempty"`
}

type VolumeMount struct {
	Volume string `json:"volume"` // a NAMED volume; host paths are not accepted
	Target string `json:"target"` // absolute path inside the container
}

// ContainerRunParams deliberately has no fields for privileged mode, host
// networking, capabilities, devices or host bind mounts: strict decoding
// rejects any attempt to pass them.
type ContainerRunParams struct {
	Image   string            `json:"image"`
	Name    string            `json:"name,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Ports   []PortMapping     `json:"ports,omitempty"`
	Volumes []VolumeMount     `json:"volumes,omitempty"`
	Restart string            `json:"restart,omitempty"` // no | always | unless-stopped | on-failure
}

var restartPolicies = map[string]bool{"": true, "no": true, "always": true, "unless-stopped": true, "on-failure": true}

func validateDocker(typ string, raw json.RawMessage) error {
	switch typ {
	case DockerInfo, DockerContainers, DockerImages, DockerVolumes, DockerNetworks, DockerImagesPrune:
		var empty struct{}
		return decodeStrict(raw, &empty)
	case DockerContainerAction:
		var p ContainerActionParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !ContainerRef.MatchString(p.ID) {
			return fmt.Errorf("%w: invalid container id or name", ErrInvalid)
		}
		switch p.Action {
		case "start", "stop", "restart", "remove":
		default:
			return fmt.Errorf("%w: action must be start, stop, restart or remove", ErrInvalid)
		}
	case DockerContainerLogs:
		var p ContainerLogsParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !ContainerRef.MatchString(p.ID) {
			return fmt.Errorf("%w: invalid container id or name", ErrInvalid)
		}
		if p.Lines < 0 || p.Lines > MaxLogLines {
			return fmt.Errorf("%w: lines must be between 1 and %d", ErrInvalid, MaxLogLines)
		}
	case DockerImagePull, DockerImageRemove:
		var p ImageParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !ValidImage(p.Image) {
			return fmt.Errorf("%w: invalid image reference", ErrInvalid)
		}
	case DockerContainerRun:
		var p ContainerRunParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		return validateRun(p)
	default:
		return fmt.Errorf("%w: unknown task type %q", ErrInvalid, typ)
	}
	return nil
}

func validateRun(p ContainerRunParams) error {
	if !ValidImage(p.Image) {
		return fmt.Errorf("%w: invalid image reference", ErrInvalid)
	}
	if p.Name != "" && !ContainerRef.MatchString(p.Name) {
		return fmt.Errorf("%w: invalid container name", ErrInvalid)
	}
	if len(p.Command) > 32 {
		return fmt.Errorf("%w: at most 32 command arguments", ErrInvalid)
	}
	for _, a := range p.Command {
		if len(a) > 4096 || strings.ContainsRune(a, 0) {
			return fmt.Errorf("%w: invalid command argument", ErrInvalid)
		}
	}
	if len(p.Env) > 100 {
		return fmt.Errorf("%w: at most 100 environment variables", ErrInvalid)
	}
	for k, v := range p.Env {
		if !envKey.MatchString(k) || len(v) > 4096 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("%w: invalid environment variable %q", ErrInvalid, k)
		}
	}
	if len(p.Ports) > 20 {
		return fmt.Errorf("%w: at most 20 port mappings", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, m := range p.Ports {
		if m.HostPort < 1 || m.HostPort > 65535 || m.ContainerPort < 1 || m.ContainerPort > 65535 {
			return fmt.Errorf("%w: ports must be between 1 and 65535", ErrInvalid)
		}
		if m.Protocol != "" && m.Protocol != "tcp" && m.Protocol != "udp" {
			return fmt.Errorf("%w: protocol must be tcp or udp", ErrInvalid)
		}
		key := fmt.Sprintf("%d/%s", m.HostPort, m.Protocol)
		if seen[key] {
			return fmt.Errorf("%w: host port %d is mapped twice", ErrInvalid, m.HostPort)
		}
		seen[key] = true
	}
	if len(p.Volumes) > 20 {
		return fmt.Errorf("%w: at most 20 volumes", ErrInvalid)
	}
	for _, v := range p.Volumes {
		// Named volumes only: anything that looks like a host path is a bind mount.
		if !volumeName.MatchString(v.Volume) {
			return fmt.Errorf("%w: volume must be a named volume (host paths are not allowed)", ErrInvalid)
		}
		if !strings.HasPrefix(v.Target, "/") || path.Clean(v.Target) != v.Target || v.Target == "/" || len(v.Target) > 1024 {
			return fmt.Errorf("%w: volume target must be an absolute path inside the container", ErrInvalid)
		}
	}
	if !restartPolicies[p.Restart] {
		return fmt.Errorf("%w: restart must be no, always, unless-stopped or on-failure", ErrInvalid)
	}
	return nil
}

// ---- results ----

type DockerInfoResult struct {
	Available         bool   `json:"available"`
	Reason            string `json:"reason,omitempty"` // why Docker is unavailable
	Version           string `json:"version,omitempty"`
	APIVersion        string `json:"api_version,omitempty"`
	OS                string `json:"os,omitempty"`
	Driver            string `json:"storage_driver,omitempty"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containers_running"`
	Images            int    `json:"images"`
	CPUs              int    `json:"cpus,omitempty"`
	MemoryBytes       int64  `json:"memory_bytes,omitempty"`
}

type ContainerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	Type        string `json:"type"`
}

type Container struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Image          string          `json:"image"`
	State          string          `json:"state"`  // running, exited, created, paused, restarting, dead
	Status         string          `json:"status"` // Docker's human text, e.g. "Up 3 minutes"
	Created        string          `json:"created"`
	Ports          []ContainerPort `json:"ports"`
	ComposeProject string          `json:"compose_project,omitempty"`
	Protected      bool            `json:"protected,omitempty"` // OSRIVA control plane
}

type Image struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Size    int64    `json:"size"`
	Created string   `json:"created"`
	InUse   bool     `json:"in_use"`
}

type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint,omitempty"`
}

type Network struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Driver string `json:"driver"`
	Scope  string `json:"scope"`
}

type ContainerLogLine struct {
	Stream string `json:"stream"` // stdout | stderr
	Time   string `json:"time,omitempty"`
	Text   string `json:"text"`
}

type ContainerLogsResult struct {
	ID    string             `json:"id"`
	Lines []ContainerLogLine `json:"lines"`
}

type ContainerActionResult struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Action string `json:"action"`
	State  string `json:"state,omitempty"` // after the action; empty when removed
}

type ImagePullResult struct {
	Image  string `json:"image"`
	Status string `json:"status"` // Docker's final status line
}

type ImagesPruneResult struct {
	Deleted        int   `json:"deleted"`
	SpaceReclaimed int64 `json:"space_reclaimed"`
}
