"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, runTask, waitTask, type App, type ProxySettings, type ProxyStatus, type Site, type Task, type TLSMode, type Websites } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
const tlsLabel: Record<TLSMode, string> = { acme: "Let's Encrypt", internal: "Internal CA", none: "HTTP only" };

export function WebsitesPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const { can, privileged } = useSession();
  const [data, setData] = useState<Websites | null>(null);
  const [status, setStatus] = useState<ProxyStatus | null>(null);
  const [apps, setApps] = useState<App[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [editing, setEditing] = useState<Site | "new" | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api<Websites>(`/servers/${serverId}/websites`));
      setApps((await api<{ apps: App[] }>(`/servers/${serverId}/apps`)).apps);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId]);
  const loadStatus = useCallback(async () => {
    if (!online) return;
    try {
      setStatus(await runTask<ProxyStatus>(serverId, "proxy.status"));
    } catch (e) {
      setStatus(null);
      setError(errorMessage(e));
    }
  }, [serverId, online]);
  useEffect(() => {
    void load();
    void loadStatus();
  }, [load, loadStatus]);

  // Runs a change, then waits for the agent to apply it.
  async function change(key: string, fn: () => Promise<{ task: Task | null }>, done: string) {
    setBusy(key);
    setError(null);
    setNotice(null);
    try {
      const r = await privileged(fn);
      if (r.task) {
        setNotice("Applying on the server…");
        await waitTask(serverId, r.task);
      }
      setNotice(done);
    } catch (e) {
      setNotice(null);
      setError(errorMessage(e));
    } finally {
      setBusy(null);
      await load();
      await loadStatus();
    }
  }

  if (!data) return error ? <Alert>{error}</Alert> : <LoadingBlock label="Loading websites…" />;
  const canOperate = can("servers.operate");
  const p = data.proxy;
  const certFor = (domain: string) => status?.certificates.find((c) => c.domain === domain);
  const appFor = (port: number) => apps.find((a) => a.host_port === port);

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Reverse proxy"
        description="Caddy, run by the agent in a container. It serves your sites on the web ports and obtains certificates automatically."
        actions={
          canOperate && (
            <Button size="sm" variant="secondary" onClick={() => setSettingsOpen(true)}>
              Settings
            </Button>
          )
        }
      >
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <Badge tone={p.enabled ? (status?.running ? "success" : "warning") : "neutral"}>
            {!p.enabled ? "off" : status ? (status.running ? "running" : status.installed ? status.state ?? "stopped" : "not installed") : online ? "checking…" : "agent offline"}
          </Badge>
          <span className="text-muted">
            HTTP {p.http_port} · HTTPS {p.https_port}
            {p.acme_email && <> · ACME e-mail {p.acme_email}</>}
            {p.acme_staging && <> · Let&apos;s Encrypt staging (untrusted test certificates)</>}
          </span>
        </div>
        {p.enabled && (p.http_port !== 80 || p.https_port !== 443) && (
          <p className="mt-2 text-xs text-warning">Let&apos;s Encrypt validates on ports 80 and 443. With other ports, only internal-CA or HTTP-only sites work.</p>
        )}
        <p className="mt-2 text-xs text-muted">OSRIVA does not change your firewall: open the web ports yourself if visitors come from the internet.</p>
      </Card>

      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}

      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Websites</h3>
        <div className="flex gap-2">
          {canOperate && (
            <Button size="sm" onClick={() => setEditing("new")}>
              Add website
            </Button>
          )}
          <Button size="sm" variant="secondary" onClick={() => { void load(); void loadStatus(); }}>
            Refresh
          </Button>
        </div>
      </div>
      {!p.enabled && data.sites.length > 0 && <Alert tone="neutral">The proxy is off, so these sites are saved but not served. Turn it on in Settings.</Alert>}
      {data.sites.length === 0 ? (
        <EmptyState title="No websites yet">Add a domain and the local port it should forward to, for example an app deployed on this server.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Domain</Th>
              <Th>Forwards to</Th>
              <Th>Certificate</Th>
              <Th>DNS</Th>
              {canOperate && <Th className="text-right">Actions</Th>}
            </tr>
          </thead>
          <tbody>
            {data.sites.map((s) => {
              const cert = certFor(s.domain);
              const app = appFor(s.upstream_port);
              return (
                <tr key={s.id}>
                  <Td>
                    <p className="font-mono text-xs">{s.domain}</p>
                    <p className="text-[11px] text-muted">{tlsLabel[s.tls_mode]}</p>
                  </Td>
                  <Td className="text-xs">
                    127.0.0.1:{s.upstream_port}
                    {app && <p className="text-[11px] text-muted">app {app.name}</p>}
                  </Td>
                  <Td className="text-xs">
                    {s.tls_mode === "none" ? (
                      <span className="text-muted">none</span>
                    ) : cert ? (
                      <>
                        <Badge tone={new Date(cert.not_after).getTime() - Date.now() > 7 * 86400e3 ? "success" : "warning"}>valid until {formatDate(cert.not_after)}</Badge>
                        <p className="mt-0.5 text-[11px] text-muted">{cert.issuer}</p>
                      </>
                    ) : (
                      <span className="text-muted">{p.enabled ? "not issued yet" : "—"}</span>
                    )}
                  </Td>
                  <Td className="text-xs">
                    {s.dns?.error ? (
                      <Badge tone="danger">{s.dns.error}</Badge>
                    ) : s.dns?.points_here ? (
                      <Badge tone="success">points here</Badge>
                    ) : (
                      <>
                        <Badge tone="warning">points elsewhere</Badge>
                        <p className="mt-0.5 max-w-48 truncate text-[11px] text-muted" title={s.dns?.addresses.join(", ")}>
                          {s.dns?.addresses.join(", ")} (agent connects from {data.server_ip || "?"})
                        </p>
                      </>
                    )}
                  </Td>
                  {canOperate && (
                    <Td className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>
                          Edit
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          loading={busy === s.id}
                          onClick={() => {
                            if (!confirm(`Stop serving ${s.domain}?`)) return;
                            void change(s.id, () => api(`/servers/${serverId}/sites/${s.id}`, { method: "DELETE" }), `${s.domain} removed.`);
                          }}
                        >
                          Remove
                        </Button>
                      </div>
                    </Td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}

      {settingsOpen && (
        <ProxyForm
          initial={p}
          busy={busy === "proxy"}
          onClose={() => setSettingsOpen(false)}
          onSave={(body) => {
            setSettingsOpen(false);
            void change("proxy", () => api(`/servers/${serverId}/proxy`, { method: "PUT", body }), body.enabled ? "Proxy settings applied." : "Proxy turned off. Certificates are kept on the server.");
          }}
        />
      )}
      {editing && (
        <SiteForm
          site={editing === "new" ? null : editing}
          apps={apps}
          onClose={() => setEditing(null)}
          onSave={(body) => {
            const s = editing;
            setEditing(null);
            void change(
              "site",
              () => (s === "new" ? api(`/servers/${serverId}/sites`, { method: "POST", body }) : api(`/servers/${serverId}/sites/${s.id}`, { method: "PUT", body })),
              `${body.domain} saved.`,
            );
          }}
        />
      )}
    </div>
  );
}

function ProxyForm({ initial, busy, onClose, onSave }: { initial: ProxySettings; busy: boolean; onClose: () => void; onSave: (p: ProxySettings) => void }) {
  const [f, setF] = useState<ProxySettings>(initial);
  return (
    <Modal
      open
      onClose={onClose}
      title="Reverse proxy settings"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={busy} onClick={() => onSave(f)}>Save and apply</Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={f.enabled} onChange={(e) => setF({ ...f, enabled: e.target.checked })} /> Run the proxy on this server
        </label>
        <div className="flex gap-3">
          <label className="flex flex-col gap-1">
            HTTP port
            <input type="number" className={inputClass + " w-28"} value={f.http_port} onChange={(e) => setF({ ...f, http_port: Number(e.target.value) })} />
          </label>
          <label className="flex flex-col gap-1">
            HTTPS port
            <input type="number" className={inputClass + " w-28"} value={f.https_port} onChange={(e) => setF({ ...f, https_port: Number(e.target.value) })} />
          </label>
        </div>
        <label className="flex flex-col gap-1">
          E-mail for Let&apos;s Encrypt (optional, recommended)
          <input className={inputClass} placeholder="ops@example.com" value={f.acme_email} onChange={(e) => setF({ ...f, acme_email: e.target.value })} />
        </label>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={f.acme_staging} onChange={(e) => setF({ ...f, acme_staging: e.target.checked })} /> Use the Let&apos;s Encrypt staging CA (test certificates, higher rate limits)
        </label>
        <p className="text-xs text-muted">
          The ports must be free. On the server that runs the OSRIVA control plane, 80 and 443 are already in use by the panel itself.
        </p>
      </div>
    </Modal>
  );
}

function SiteForm({ site, apps, onClose, onSave }: { site: Site | null; apps: App[]; onClose: () => void; onSave: (s: { domain: string; upstream_port: number; tls_mode: TLSMode }) => void }) {
  const [domain, setDomain] = useState(site?.domain ?? "");
  const [port, setPort] = useState(site?.upstream_port ?? apps[0]?.host_port ?? 8080);
  const [tls, setTls] = useState<TLSMode>(site?.tls_mode ?? "acme");
  return (
    <Modal
      open
      onClose={onClose}
      title={site ? `Edit ${site.domain}` : "Add website"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button disabled={!domain.trim()} onClick={() => onSave({ domain: domain.trim(), upstream_port: port, tls_mode: tls })}>Save</Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <label className="flex flex-col gap-1">
          Domain
          <input className={inputClass} placeholder="www.example.com" value={domain} onChange={(e) => setDomain(e.target.value)} />
          <span className="text-xs text-muted">Point its DNS A/AAAA record at this server first. Wildcards are not supported yet.</span>
        </label>
        <label className="flex flex-col gap-1">
          Forward to
          <select className={inputClass} value={apps.some((a) => a.host_port === port) ? String(port) : "custom"} onChange={(e) => e.target.value !== "custom" && setPort(Number(e.target.value))}>
            {apps.map((a) => (
              <option key={a.id} value={a.host_port}>
                app {a.name} (127.0.0.1:{a.host_port})
              </option>
            ))}
            <option value="custom">another local port…</option>
          </select>
        </label>
        <label className="flex flex-col gap-1">
          Local port
          <input type="number" className={inputClass + " w-28"} value={port} onChange={(e) => setPort(Number(e.target.value))} />
        </label>
        <label className="flex flex-col gap-1">
          Certificate
          <select className={inputClass} value={tls} onChange={(e) => setTls(e.target.value as TLSMode)}>
            <option value="acme">Let&apos;s Encrypt (public, automatic renewal)</option>
            <option value="internal">Internal CA (browsers will warn; for testing and internal names)</option>
            <option value="none">None: HTTP only</option>
          </select>
        </label>
      </div>
    </Modal>
  );
}
