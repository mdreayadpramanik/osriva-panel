package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Websites (docs/adr/0008-reverse-proxy.md): the panel keeps the proxy
// settings and sites of each server and sends the agent the complete desired
// configuration (proxy.apply) after every change.

type ProxyInput struct {
	Enabled     bool   `json:"enabled"`
	HTTPPort    int    `json:"http_port"`
	HTTPSPort   int    `json:"https_port"`
	ACMEEmail   string `json:"acme_email"`
	ACMEStaging bool   `json:"acme_staging"`
}

type SiteInput struct {
	Domain       string `json:"domain"`
	UpstreamPort int    `json:"upstream_port"`
	TLSMode      string `json:"tls_mode"`
}

// DNSCheck is what the panel's resolver sees for a domain. It is a hint, not
// proof: the agent's address may differ from the public one behind NAT.
type DNSCheck struct {
	Addresses []string
	// PointsHere is true when a resolved address equals the address the agent connects from.
	PointsHere bool
	Error      string
}

type SiteView struct {
	store.Site
	DNS DNSCheck
}

type WebsitesView struct {
	Settings store.ProxySettings
	ServerIP string
	Sites    []SiteView
}

// lookupHost is replaced in tests.
var lookupHost = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (s *Service) proxySettings(ctx context.Context, serverID string) (*store.ProxySettings, error) {
	p, err := s.store.GetProxySettings(ctx, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return &store.ProxySettings{ServerID: serverID, HTTPPort: 80, HTTPSPort: 443}, nil
	}
	return p, err
}

func desiredConfig(p *store.ProxySettings, sites []store.Site) agenttask.ProxyConfig {
	c := agenttask.ProxyConfig{HTTPPort: p.HTTPPort, HTTPSPort: p.HTTPSPort, ACMEEmail: p.ACMEEmail, ACMEStaging: p.ACMEStaging,
		Sites: make([]agenttask.ProxySite, 0, len(sites))}
	for _, x := range sites {
		c.Sites = append(c.Sites, agenttask.ProxySite{Domain: x.Domain, UpstreamPort: x.UpstreamPort, TLS: x.TLSMode})
	}
	return c
}

func validationErr(field string, err error) error {
	return &identity.ValidationError{Field: field, Message: strings.TrimPrefix(err.Error(), agenttask.ErrInvalid.Error()+": ")}
}

func (s *Service) Websites(ctx context.Context, actor *identity.Principal, serverID string) (*WebsitesView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersRead, serverID, "")
	if err != nil {
		return nil, err
	}
	p, err := s.proxySettings(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	sites, err := s.store.ListSites(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	v := &WebsitesView{Settings: *p, ServerIP: x.LastSeenIP, Sites: make([]SiteView, len(sites))}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range sites {
		v.Sites[i].Site = sites[i]
		wg.Add(1)
		go func(sv *SiteView) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sv.DNS = checkDNS(ctx, sv.Domain, x.LastSeenIP)
		}(&v.Sites[i])
	}
	wg.Wait()
	return v, nil
}

func checkDNS(ctx context.Context, domain, serverIP string) DNSCheck {
	if strings.HasSuffix(domain, ".localhost") {
		return DNSCheck{Addresses: []string{"127.0.0.1"}, PointsHere: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := lookupHost(ctx, domain)
	if err != nil {
		return DNSCheck{Addresses: []string{}, Error: "the domain does not resolve"}
	}
	sort.Strings(addrs)
	c := DNSCheck{Addresses: addrs}
	for _, a := range addrs {
		c.PointsHere = c.PointsHere || (serverIP != "" && a == serverIP)
	}
	return c
}

// applyProxy queues the complete configuration (or the shutdown) for the agent.
func (s *Service) applyProxy(ctx context.Context, actor *identity.Principal, serverID string, p *store.ProxySettings) (*store.Task, error) {
	if !p.Enabled {
		return s.QueueTask(ctx, actor, serverID, agenttask.ProxyDisable, nil)
	}
	sites, err := s.store.ListSites(ctx, serverID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(desiredConfig(p, sites))
	if err != nil {
		return nil, err
	}
	return s.QueueTask(ctx, actor, serverID, agenttask.ProxyApply, raw)
}

// SaveProxy stores the proxy settings and applies them; turning the proxy off
// removes the proxy container (certificates are kept on the server).
func (s *Service) SaveProxy(ctx context.Context, actor *identity.Principal, serverID string, in ProxyInput) (*store.ProxySettings, *store.Task, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, nil, err
	}
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return nil, nil, err
	}
	old, err := s.proxySettings(ctx, x.ID)
	if err != nil {
		return nil, nil, err
	}
	p := &store.ProxySettings{ServerID: x.ID, Enabled: in.Enabled, HTTPPort: in.HTTPPort, HTTPSPort: in.HTTPSPort,
		ACMEEmail: strings.TrimSpace(in.ACMEEmail), ACMEStaging: in.ACMEStaging}
	sites, err := s.store.ListSites(ctx, x.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := agenttask.ValidateProxyConfig(desiredConfig(p, sites)); err != nil {
		return nil, nil, validationErr("proxy", err)
	}
	var t *store.Task
	if p.Enabled || old.Enabled {
		if t, err = s.applyProxy(ctx, actor, x.ID, p); err != nil {
			return nil, nil, err
		}
	}
	if err := s.store.SaveProxySettings(ctx, p); err != nil {
		return nil, nil, err
	}
	s.id.Audit(ctx, actor.User, "proxy.update", "server", x.ID, store.OutcomeSuccess, map[string]any{"server": x.Name,
		"enabled": p.Enabled, "http_port": p.HTTPPort, "https_port": p.HTTPSPort, "acme_staging": p.ACMEStaging})
	return p, t, nil
}

func (s *Service) siteInOrg(ctx context.Context, actor *identity.Principal, serverID, siteID string) (*store.Server, *store.Site, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, nil, err
	}
	if siteID == "" {
		return x, nil, nil
	}
	site, err := s.store.GetSite(ctx, siteID)
	if err != nil {
		return nil, nil, err
	}
	if site.ServerID != x.ID {
		return nil, nil, store.ErrNotFound
	}
	return x, site, nil
}

// changeSites validates the configuration with the change applied, stores it
// and, when the proxy is on, queues the new configuration.
func (s *Service) changeSites(ctx context.Context, actor *identity.Principal, x *store.Server, apply func([]store.Site) []store.Site, persist func() error) (*store.Task, error) {
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return nil, err
	}
	p, err := s.proxySettings(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	sites, err := s.store.ListSites(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	if err := agenttask.ValidateProxyConfig(desiredConfig(p, apply(sites))); err != nil {
		return nil, validationErr("site", err)
	}
	if err := persist(); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &identity.ValidationError{Field: "domain", Message: "is already served by another site"}
		}
		return nil, err
	}
	if !p.Enabled {
		return nil, nil
	}
	return s.applyProxy(ctx, actor, x.ID, p)
}

func siteFromInput(in SiteInput) store.Site {
	return store.Site{Domain: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.Domain), ".")), UpstreamPort: in.UpstreamPort, TLSMode: in.TLSMode}
}

func (s *Service) CreateSite(ctx context.Context, actor *identity.Principal, serverID string, in SiteInput) (*store.Site, *store.Task, error) {
	x, _, err := s.siteInOrg(ctx, actor, serverID, "")
	if err != nil {
		return nil, nil, err
	}
	site := siteFromInput(in)
	site.ID, site.ServerID, site.CreatedBy = auth.NewID(), x.ID, &actor.User.ID
	t, err := s.changeSites(ctx, actor, x,
		func(list []store.Site) []store.Site { return append(list, site) },
		func() error { return s.store.CreateSite(ctx, &site) })
	if err != nil {
		return nil, nil, err
	}
	s.id.Audit(ctx, actor.User, "sites.create", "site", site.ID, store.OutcomeSuccess,
		map[string]any{"domain": site.Domain, "upstream_port": site.UpstreamPort, "tls": site.TLSMode, "server": x.Name})
	return &site, t, nil
}

func (s *Service) UpdateSite(ctx context.Context, actor *identity.Principal, serverID, siteID string, in SiteInput) (*store.Site, *store.Task, error) {
	x, old, err := s.siteInOrg(ctx, actor, serverID, siteID)
	if err != nil {
		return nil, nil, err
	}
	site := siteFromInput(in)
	site.ID = old.ID
	t, err := s.changeSites(ctx, actor, x,
		func(list []store.Site) []store.Site {
			for i := range list {
				if list[i].ID == site.ID {
					list[i] = site
				}
			}
			return list
		},
		func() error { return s.store.UpdateSite(ctx, &site) })
	if err != nil {
		return nil, nil, err
	}
	s.id.Audit(ctx, actor.User, "sites.update", "site", site.ID, store.OutcomeSuccess,
		map[string]any{"domain": site.Domain, "upstream_port": site.UpstreamPort, "tls": site.TLSMode, "server": x.Name})
	return &site, t, nil
}

func (s *Service) DeleteSite(ctx context.Context, actor *identity.Principal, serverID, siteID string) (*store.Task, error) {
	x, old, err := s.siteInOrg(ctx, actor, serverID, siteID)
	if err != nil {
		return nil, err
	}
	t, err := s.changeSites(ctx, actor, x,
		func(list []store.Site) []store.Site {
			out := list[:0]
			for _, o := range list {
				if o.ID != old.ID {
					out = append(out, o)
				}
			}
			return out
		},
		func() error { return s.store.DeleteSite(ctx, old.ID) })
	if err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "sites.delete", "site", old.ID, store.OutcomeSuccess, map[string]any{"domain": old.Domain, "server": x.Name})
	return t, nil
}
