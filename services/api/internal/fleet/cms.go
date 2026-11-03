package fleet

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// CMS instances (docs/adr/0009-cms-wordpress.md). The panel keeps what was
// installed where; versions, plugins and health are read live from the agent.

type WordPressInput struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	AdminUser  string `json:"admin_user"`
	AdminEmail string `json:"admin_email"`
	HostPort   int    `json:"host_port"`
	// Domain, when set, also creates a website (Phase 5) pointing at the instance.
	Domain  string `json:"domain"`
	TLSMode string `json:"tls_mode"`
}

type CMSView struct {
	store.CMSInstance
	InstallStatus store.TaskStatus
	InstallError  string
	Domain        string
}

// CreatedCMS is returned once: AdminPassword is never stored by the panel.
type CreatedCMS struct {
	CMSView
	AdminPassword string
	Task          *store.Task
}

func newAdminPassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b) // 24 characters
}

func (s *Service) cmsView(ctx context.Context, c *store.CMSInstance) CMSView {
	v := CMSView{CMSInstance: *c, InstallStatus: store.TaskExpired, InstallError: "the install task record is gone"}
	if c.InstallTaskID != nil {
		if t, err := s.store.GetTask(ctx, *c.InstallTaskID); err == nil {
			v.InstallStatus, v.InstallError = t.Status, t.Error
		}
	}
	if c.SiteID != nil {
		if site, err := s.store.GetSite(ctx, *c.SiteID); err == nil {
			v.Domain = site.Domain
		}
	}
	return v
}

func (s *Service) ListCMS(ctx context.Context, actor *identity.Principal, serverID string) ([]CMSView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersRead, serverID, "")
	if err != nil {
		return nil, err
	}
	list, err := s.store.ListCMS(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	out := make([]CMSView, len(list))
	for i := range list {
		out[i] = s.cmsView(ctx, &list[i])
	}
	return out, nil
}

func (s *Service) cmsInOrg(ctx context.Context, actor *identity.Principal, perm, serverID, id string) (*store.Server, *store.CMSInstance, error) {
	x, _, err := s.appInOrg(ctx, actor, perm, serverID, "")
	if err != nil {
		return nil, nil, err
	}
	c, err := s.store.GetCMS(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if c.ServerID != x.ID {
		return nil, nil, store.ErrNotFound
	}
	return x, c, nil
}

func (s *Service) GetCMS(ctx context.Context, actor *identity.Principal, serverID, id string) (*CMSView, error) {
	_, c, err := s.cmsInOrg(ctx, actor, rbac.ServersRead, serverID, id)
	if err != nil {
		return nil, err
	}
	v := s.cmsView(ctx, c)
	return &v, nil
}

// portUsed reports a host port already taken by an app or a CMS instance on the server.
func (s *Service) portUsed(ctx context.Context, serverID string, port int) (bool, error) {
	apps, err := s.store.ListApps(ctx, serverID)
	if err != nil {
		return false, err
	}
	for _, a := range apps {
		if a.HostPort == port {
			return true, nil
		}
	}
	list, err := s.store.ListCMS(ctx, serverID)
	if err != nil {
		return false, err
	}
	for _, c := range list {
		if c.HostPort == port {
			return true, nil
		}
	}
	return false, nil
}

// InstallWordPress queues the installation and, when a domain is given, adds
// the website that serves it.
func (s *Service) InstallWordPress(ctx context.Context, actor *identity.Principal, serverID string, in WordPressInput) (*CreatedCMS, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, err
	}
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return nil, err
	}
	in.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.Domain), "."))
	siteURL := fmt.Sprintf("http://127.0.0.1:%d", in.HostPort)
	if in.Domain != "" {
		if !agenttask.ValidDomain(in.Domain) {
			return nil, &identity.ValidationError{Field: "domain", Message: "is not a valid domain name"}
		}
		scheme := "https"
		if in.TLSMode == agenttask.TLSNone {
			scheme = "http"
		}
		siteURL = scheme + "://" + in.Domain
	}
	p := agenttask.WPInstallParams{Name: in.Name, HostPort: in.HostPort, URL: siteURL, Title: strings.TrimSpace(in.Title),
		AdminUser: strings.TrimSpace(in.AdminUser), AdminEmail: strings.TrimSpace(in.AdminEmail), AdminPassword: newAdminPassword()}
	raw, _ := json.Marshal(p)
	if err := agenttask.Validate(agenttask.CMSWPInstall, raw); err != nil {
		return nil, validationErr("wordpress", err)
	}
	if used, err := s.portUsed(ctx, x.ID, in.HostPort); err != nil {
		return nil, err
	} else if used {
		return nil, &identity.ValidationError{Field: "host_port", Message: "is already used by an app or CMS on this server"}
	}
	if list, err := s.store.ListCMS(ctx, x.ID); err != nil {
		return nil, err
	} else {
		for _, c := range list {
			if c.Name == p.Name {
				return nil, &identity.ValidationError{Field: "name", Message: "is already used on this server"}
			}
		}
	}

	var site *store.Site
	if in.Domain != "" {
		// Validates the domain against the proxy configuration before anything is queued.
		if site, _, err = s.CreateSite(ctx, actor, serverID, SiteInput{Domain: in.Domain, UpstreamPort: in.HostPort, TLSMode: in.TLSMode}); err != nil {
			return nil, err
		}
	}
	t, err := s.QueueTask(ctx, actor, serverID, agenttask.CMSWPInstall, raw)
	if err != nil {
		if site != nil {
			_ = s.store.DeleteSite(ctx, site.ID)
		}
		return nil, err
	}
	c := &store.CMSInstance{ID: auth.NewID(), ServerID: x.ID, Kind: "wordpress", Name: p.Name, Title: p.Title, URL: p.URL,
		HostPort: p.HostPort, AdminUser: p.AdminUser, AdminEmail: p.AdminEmail, InstallTaskID: &t.ID, CreatedBy: &actor.User.ID}
	if site != nil {
		c.SiteID = &site.ID
	}
	if err := s.store.CreateCMS(ctx, c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &identity.ValidationError{Field: "name", Message: "or host port is already used on this server"}
		}
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "cms.install", "cms", c.ID, store.OutcomeSuccess, map[string]any{"kind": c.Kind, "name": c.Name,
		"url": c.URL, "server": x.Name, "admin_user": c.AdminUser, "task_id": t.ID})
	return &CreatedCMS{CMSView: s.cmsView(ctx, c), AdminPassword: p.AdminPassword, Task: t}, nil
}

// DeleteCMS removes the containers (and, with deleteData, the database and
// files) from the server, its website and the panel record.
func (s *Service) DeleteCMS(ctx context.Context, actor *identity.Principal, serverID, id string, deleteData bool) (*store.Task, error) {
	x, c, err := s.cmsInOrg(ctx, actor, rbac.ServersOperate, serverID, id)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(agenttask.CMSRemoveParams{Name: c.Name, DeleteData: deleteData})
	t, err := s.QueueTask(ctx, actor, serverID, agenttask.CMSRemove, raw)
	if err != nil {
		return nil, err
	}
	if c.SiteID != nil {
		if _, err := s.DeleteSite(ctx, actor, serverID, *c.SiteID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("remove CMS website", "cms", c.Name, "err", err)
		}
	}
	if err := s.store.DeleteCMS(ctx, c.ID); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "cms.delete", "cms", c.ID, store.OutcomeSuccess, map[string]any{"name": c.Name, "server": x.Name,
		"delete_data": deleteData, "task_id": t.ID})
	return t, nil
}
