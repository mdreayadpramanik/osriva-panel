package agenttask

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Application deployments (docs/adr/0007-deployments.md): the agent fetches a
// Git repository, builds an image with Docker and swaps the app's container.

const (
	AppDeploy = "app.deploy"
	AppRemove = "app.remove"

	// AppLabel marks containers and images that belong to an OSRIVA app.
	AppLabel = "dev.osriva.app"

	BuildDockerfile = "dockerfile"
	BuildStatic     = "static"
	BuildNode       = "node"

	MaxDeployLogBytes = 256 << 10
)

func appTask(typ string) bool { return typ == AppDeploy || typ == AppRemove }

var (
	// AppName is the app slug; it becomes part of container and image names.
	AppName      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	deploymentID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	gitRef       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	relPath      = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$`)
)

// AppContainer and AppImage name the Docker objects of an app.
func AppContainer(app string) string         { return "osriva-app-" + app }
func AppImageRepo(app string) string         { return "osriva-app/" + app }
func AppImage(app, deployment string) string { return AppImageRepo(app) + ":" + deployment }

type GitSource struct {
	URL string `json:"url"` // https:// (or http://) without credentials
	Ref string `json:"ref"` // branch or tag
}

type BuildSpec struct {
	Mode       string `json:"mode"`                 // dockerfile | static | node
	Context    string `json:"context,omitempty"`    // sub-directory of the repository, default the root
	Dockerfile string `json:"dockerfile,omitempty"` // relative to the context, default "Dockerfile"
}

type AppRunSpec struct {
	ContainerPort int               `json:"container_port"`
	HostPort      int               `json:"host_port"`
	Public        bool              `json:"public,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

// AppDeployParams either builds from Source or, for a rollback, reuses Image
// (an earlier build of the same app).
type AppDeployParams struct {
	App        string     `json:"app"`
	Deployment string     `json:"deployment"`
	Source     *GitSource `json:"source,omitempty"`
	Build      *BuildSpec `json:"build,omitempty"`
	Image      string     `json:"image,omitempty"`
	Run        AppRunSpec `json:"run"`
}

type AppRemoveParams struct {
	App string `json:"app"`
}

// ValidGitURL accepts http(s) URLs without credentials, query or fragment.
func ValidGitURL(s string) error {
	if len(s) > 500 || strings.ContainsAny(s, " \t\r\n\x00") {
		return fmt.Errorf("%w: invalid repository URL", ErrInvalid)
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("%w: the repository URL must start with https://", ErrInvalid)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in the repository URL are not accepted (private repositories are not supported yet)", ErrInvalid)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: the repository URL must not have a query or fragment", ErrInvalid)
	}
	return nil
}

// ValidGitRef accepts branch and tag names (not commit hashes).
func ValidGitRef(s string) bool {
	return gitRef.MatchString(s) && !strings.Contains(s, "..") && !strings.Contains(s, "//") &&
		!strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".lock") && !strings.HasPrefix(s, "-")
}

// ValidRelPath accepts a clean relative path that stays inside its base.
func ValidRelPath(s string) bool {
	return relPath.MatchString(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../") && !strings.Contains(s, "/../")
}

func validateBuild(b *BuildSpec) error {
	switch b.Mode {
	case BuildDockerfile, BuildStatic, BuildNode:
	default:
		return fmt.Errorf("%w: build mode must be dockerfile, static or node", ErrInvalid)
	}
	if b.Context != "" && b.Context != "." && !ValidRelPath(b.Context) {
		return fmt.Errorf("%w: build context must be a relative path inside the repository", ErrInvalid)
	}
	if b.Dockerfile != "" && (b.Mode != BuildDockerfile || !ValidRelPath(b.Dockerfile)) {
		return fmt.Errorf("%w: dockerfile must be a relative path and is only used in dockerfile mode", ErrInvalid)
	}
	return nil
}

func validateRunSpec(r AppRunSpec) error {
	if r.ContainerPort < 1 || r.ContainerPort > 65535 || r.HostPort < 1 || r.HostPort > 65535 {
		return fmt.Errorf("%w: ports must be between 1 and 65535", ErrInvalid)
	}
	if len(r.Env) > 100 {
		return fmt.Errorf("%w: at most 100 environment variables", ErrInvalid)
	}
	for k, v := range r.Env {
		if !envKey.MatchString(k) || len(v) > 4096 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("%w: invalid environment variable %q", ErrInvalid, k)
		}
	}
	return nil
}

func validateApp(typ string, raw json.RawMessage) error {
	if typ == AppRemove {
		var p AppRemoveParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !AppName.MatchString(p.App) {
			return fmt.Errorf("%w: invalid app name", ErrInvalid)
		}
		return nil
	}
	var p AppDeployParams
	if err := decodeStrict(raw, &p); err != nil {
		return err
	}
	if !AppName.MatchString(p.App) {
		return fmt.Errorf("%w: app name must be lower-case letters, digits and dashes (at most 40)", ErrInvalid)
	}
	if !deploymentID.MatchString(p.Deployment) {
		return fmt.Errorf("%w: invalid deployment id", ErrInvalid)
	}
	switch {
	case p.Image != "" && p.Source == nil && p.Build == nil:
		repo, tag, ok := strings.Cut(p.Image, ":")
		if !ok || repo != AppImageRepo(p.App) || !deploymentID.MatchString(tag) {
			return fmt.Errorf("%w: a rollback can only use an earlier image of the same app", ErrInvalid)
		}
	case p.Image == "" && p.Source != nil && p.Build != nil:
		if err := ValidGitURL(p.Source.URL); err != nil {
			return err
		}
		if !ValidGitRef(p.Source.Ref) {
			return fmt.Errorf("%w: invalid branch or tag", ErrInvalid)
		}
		if err := validateBuild(p.Build); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: give either a source and build, or an image to roll back to", ErrInvalid)
	}
	return validateRunSpec(p.Run)
}

// RunTimeout is how long the control plane waits for a running task's result.
func RunTimeout(typ string) time.Duration {
	switch typ {
	case AppDeploy:
		return 20 * time.Minute
	case BackupCreate, BackupVerify, BackupRestore:
		return 60 * time.Minute // large volumes
	case VMCreate:
		return 30 * time.Minute // image download and first boot
	case VMAction, VMDelete:
		return 5 * time.Minute
	case CMSWPInstall, CMSWPUpdate:
		return 15 * time.Minute // image downloads, WordPress updates
	case ProxyApply:
		return 6 * time.Minute // may pull the Caddy image
	case DockerImagePull, DockerContainerRun:
		return 6 * time.Minute
	}
	return 5 * time.Minute
}

// LongRunning tasks run beside the agent's normal task loop so that they do
// not hold up quick tasks.
func LongRunning(typ string) bool {
	return typ == AppDeploy || typ == CMSWPInstall || typ == CMSWPUpdate || typ == BackupCreate || typ == BackupVerify || typ == BackupRestore ||
		typ == VMCreate
}

type AppDeployResult struct {
	App           string `json:"app"`
	Image         string `json:"image"`
	Commit        string `json:"commit,omitempty"`
	ContainerID   string `json:"container_id,omitempty"`
	State         string `json:"state,omitempty"`
	Recovered     bool   `json:"recovered,omitempty"` // the new version failed and the previous one was restarted
	Logs          string `json:"logs"`
	DurationMilli int64  `json:"duration_ms"`
}

type AppRemoveResult struct {
	App              string `json:"app"`
	ContainerRemoved bool   `json:"container_removed"`
	ImagesRemoved    int    `json:"images_removed"`
}
