package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Applications (docs/adr/0007-deployments.md): an app is a Git repository
// built into a Docker image and run as one container on one server. Every
// deployment is an app.deploy task; its status and logs live in the task.

// AppInput is the editable configuration of an app. On update, a nil Env
// keeps the stored variables; a non-nil Env replaces them.
type AppInput struct {
	Name          string            `json:"name"`
	GitURL        string            `json:"git_url"`
	GitRef        string            `json:"git_ref"`
	BuildMode     string            `json:"build_mode"`
	BuildContext  string            `json:"build_context"`
	Dockerfile    string            `json:"dockerfile"`
	ContainerPort int               `json:"container_port"`
	HostPort      int               `json:"host_port"`
	Public        bool              `json:"public"`
	Env           map[string]string `json:"env"`
}

type DeploymentView struct {
	store.Deployment
	Status     store.TaskStatus
	StartedAt  *time.Time
	FinishedAt *time.Time
	Error      string
	Result     *agenttask.AppDeployResult
}

type AppView struct {
	store.App
	EnvNames []string
	// Current is the newest successful deployment (what should be running).
	Current     *DeploymentView
	Deployments []DeploymentView
}

const maxDeploymentsShown = 30

func envAAD(appID string) []byte { return []byte("osriva/app-env/" + appID) }

func (s *Service) appEnv(a *store.App) (map[string]string, error) {
	env := map[string]string{}
	if len(a.EnvEnc) == 0 {
		return env, nil
	}
	plain, err := s.keys.Decrypt(a.EnvEnc, envAAD(a.ID))
	if err != nil {
		return nil, err
	}
	return env, json.Unmarshal(plain, &env)
}

func (s *Service) sealEnv(appID string, env map[string]string) ([]byte, error) {
	if len(env) == 0 {
		return nil, nil
	}
	plain, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return s.keys.Encrypt(plain, envAAD(appID))
}

func deployParams(a *store.App, deploymentID string, env map[string]string) agenttask.AppDeployParams {
	return agenttask.AppDeployParams{
		App: a.Name, Deployment: deploymentID,
		Source: &agenttask.GitSource{URL: a.GitURL, Ref: a.GitRef},
		Build:  &agenttask.BuildSpec{Mode: a.BuildMode, Context: a.BuildContext, Dockerfile: a.Dockerfile},
		Run:    agenttask.AppRunSpec{ContainerPort: a.ContainerPort, HostPort: a.HostPort, Public: a.Public, Env: env},
	}
}

// validateApp checks a configuration with the same rules the agent applies.
func validateApp(a *store.App, env map[string]string) error {
	raw, _ := json.Marshal(deployParams(a, "validation", env))
	if err := agenttask.Validate(agenttask.AppDeploy, raw); err != nil {
		return &identity.ValidationError{Field: "app", Message: "settings are invalid: " + strings.TrimPrefix(err.Error(), agenttask.ErrInvalid.Error()+": ")}
	}
	return nil
}

func (s *Service) appInOrg(ctx context.Context, actor *identity.Principal, perm, serverID, appID string) (*store.Server, *store.App, error) {
	if err := s.id.Require(ctx, actor, perm); err != nil {
		return nil, nil, err
	}
	x, err := s.getInOrg(ctx, actor, serverID)
	if err != nil {
		return nil, nil, err
	}
	if appID == "" {
		return x, nil, nil
	}
	a, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return nil, nil, err
	}
	if a.ServerID != x.ID {
		return nil, nil, store.ErrNotFound
	}
	return x, a, nil
}

func (s *Service) appView(ctx context.Context, a *store.App, withHistory bool) (*AppView, error) {
	v := &AppView{App: *a, EnvNames: []string{}}
	v.EnvEnc = nil
	env, err := s.appEnv(a)
	if err != nil {
		return nil, err
	}
	for k := range env {
		v.EnvNames = append(v.EnvNames, k)
	}
	sort.Strings(v.EnvNames)
	limit := maxDeploymentsShown
	if !withHistory {
		limit = 10 // enough to find the current deployment in the list view
	}
	ds, err := s.store.ListDeployments(ctx, a.ID, limit)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		dv := s.deploymentView(ctx, d, false)
		if v.Current == nil && dv.Status == store.TaskSucceeded {
			c := dv
			v.Current = &c
		}
		if withHistory {
			v.Deployments = append(v.Deployments, dv)
		}
	}
	return v, nil
}

func (s *Service) deploymentView(ctx context.Context, d store.Deployment, withLogs bool) DeploymentView {
	dv := DeploymentView{Deployment: d, Status: store.TaskExpired, Error: "the task record is gone"}
	if d.TaskID == nil {
		return dv
	}
	t, err := s.store.GetTask(ctx, *d.TaskID)
	if err != nil {
		return dv
	}
	dv.Status, dv.StartedAt, dv.FinishedAt, dv.Error = t.Status, t.StartedAt, t.FinishedAt, t.Error
	if len(t.Result) > 0 {
		var r agenttask.AppDeployResult
		if json.Unmarshal(t.Result, &r) == nil {
			if !withLogs {
				r.Logs = ""
			}
			dv.Result = &r
		}
	}
	return dv
}

func (s *Service) ListApps(ctx context.Context, actor *identity.Principal, serverID string) ([]AppView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersRead, serverID, "")
	if err != nil {
		return nil, err
	}
	apps, err := s.store.ListApps(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	out := make([]AppView, 0, len(apps))
	for i := range apps {
		v, err := s.appView(ctx, &apps[i], false)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func (s *Service) GetApp(ctx context.Context, actor *identity.Principal, serverID, appID string) (*AppView, error) {
	_, a, err := s.appInOrg(ctx, actor, rbac.ServersRead, serverID, appID)
	if err != nil {
		return nil, err
	}
	return s.appView(ctx, a, true)
}

// GetDeployment returns one deployment including its build and run logs.
func (s *Service) GetDeployment(ctx context.Context, actor *identity.Principal, serverID, appID, deploymentID string) (*DeploymentView, error) {
	_, a, err := s.appInOrg(ctx, actor, rbac.ServersRead, serverID, appID)
	if err != nil {
		return nil, err
	}
	d, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if d.AppID != a.ID {
		return nil, store.ErrNotFound
	}
	dv := s.deploymentView(ctx, *d, true)
	return &dv, nil
}

func (s *Service) CreateApp(ctx context.Context, actor *identity.Principal, serverID string, in AppInput) (*AppView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, err
	}
	a := &store.App{ID: auth.NewID(), ServerID: x.ID, Name: in.Name, CreatedBy: &actor.User.ID}
	applyInput(a, in)
	if err := validateApp(a, in.Env); err != nil {
		return nil, err
	}
	if a.EnvEnc, err = s.sealEnv(a.ID, in.Env); err != nil {
		return nil, err
	}
	if err := s.store.CreateApp(ctx, a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &identity.ValidationError{Field: "app", Message: "name or host port is already used by another app on this server"}
		}
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "apps.create", "app", a.ID, store.OutcomeSuccess, appAudit(a, x, in.Env))
	return s.appView(ctx, a, true)
}

func applyInput(a *store.App, in AppInput) {
	a.GitURL, a.GitRef, a.BuildMode = strings.TrimSpace(in.GitURL), strings.TrimSpace(in.GitRef), in.BuildMode
	a.BuildContext, a.Dockerfile = strings.Trim(strings.TrimSpace(in.BuildContext), "/"), strings.TrimSpace(in.Dockerfile)
	if a.BuildContext == "." {
		a.BuildContext = ""
	}
	a.ContainerPort, a.HostPort, a.Public = in.ContainerPort, in.HostPort, in.Public
}

func appAudit(a *store.App, x *store.Server, env map[string]string) map[string]any {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	return map[string]any{"app": a.Name, "server": x.Name, "git_url": a.GitURL, "git_ref": a.GitRef,
		"build_mode": a.BuildMode, "host_port": a.HostPort, "public": a.Public, "env_names": names}
}

func (s *Service) UpdateApp(ctx context.Context, actor *identity.Principal, serverID, appID string, in AppInput) (*AppView, error) {
	x, a, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, appID)
	if err != nil {
		return nil, err
	}
	env := in.Env
	if env == nil {
		if env, err = s.appEnv(a); err != nil {
			return nil, err
		}
	}
	applyInput(a, in)
	if err := validateApp(a, env); err != nil {
		return nil, err
	}
	if a.EnvEnc, err = s.sealEnv(a.ID, env); err != nil {
		return nil, err
	}
	if err := s.store.UpdateApp(ctx, a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &identity.ValidationError{Field: "host_port", Message: "is already used by another app on this server"}
		}
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "apps.update", "app", a.ID, store.OutcomeSuccess, appAudit(a, x, env))
	return s.appView(ctx, a, true)
}

// DeleteApp removes the app's container and images from the server and its
// record from the panel. Deployment history goes with it.
func (s *Service) DeleteApp(ctx context.Context, actor *identity.Principal, serverID, appID string) (*store.Task, error) {
	_, a, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, appID)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(agenttask.AppRemoveParams{App: a.Name})
	t, err := s.QueueTask(ctx, actor, serverID, agenttask.AppRemove, raw)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteApp(ctx, a.ID); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "apps.delete", "app", a.ID, store.OutcomeSuccess, map[string]any{"app": a.Name, "task_id": t.ID})
	return t, nil
}

// Deploy builds the configured ref and replaces the running container.
func (s *Service) Deploy(ctx context.Context, actor *identity.Principal, serverID, appID string) (*DeploymentView, error) {
	_, a, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, appID)
	if err != nil {
		return nil, err
	}
	env, err := s.appEnv(a)
	if err != nil {
		return nil, err
	}
	d := &store.Deployment{ID: auth.NewID(), AppID: a.ID, Kind: "deploy", GitRef: a.GitRef, CreatedBy: &actor.User.ID}
	d.Image = agenttask.AppImage(a.Name, d.ID)
	return s.queueDeployment(ctx, actor, serverID, d, deployParams(a, d.ID, env))
}

// Rollback runs an earlier successful build again with the app's current settings.
func (s *Service) Rollback(ctx context.Context, actor *identity.Principal, serverID, appID, deploymentID string) (*DeploymentView, error) {
	_, a, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, appID)
	if err != nil {
		return nil, err
	}
	old, err := s.store.GetDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if old.AppID != a.ID {
		return nil, store.ErrNotFound
	}
	if s.deploymentView(ctx, *old, false).Status != store.TaskSucceeded {
		return nil, &identity.PolicyError{Message: "only a successful deployment can be rolled back to"}
	}
	env, err := s.appEnv(a)
	if err != nil {
		return nil, err
	}
	d := &store.Deployment{ID: auth.NewID(), AppID: a.ID, Kind: "rollback", RollbackOf: &old.ID, Image: old.Image,
		GitRef: old.GitRef, CreatedBy: &actor.User.ID}
	p := deployParams(a, d.ID, env)
	p.Source, p.Build, p.Image = nil, nil, old.Image
	return s.queueDeployment(ctx, actor, serverID, d, p)
}

func (s *Service) queueDeployment(ctx context.Context, actor *identity.Principal, serverID string, d *store.Deployment, p agenttask.AppDeployParams) (*DeploymentView, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	t, err := s.QueueTask(ctx, actor, serverID, agenttask.AppDeploy, raw)
	if err != nil {
		return nil, err
	}
	d.TaskID = &t.ID
	if err := s.store.CreateDeployment(ctx, d); err != nil {
		return nil, err
	}
	dv := s.deploymentView(ctx, *d, false)
	return &dv, nil
}
