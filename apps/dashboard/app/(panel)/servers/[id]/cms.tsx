"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, runTask, waitTask, type CMSInstance, type Task, type TLSMode, type WPCommandResult, type WPComponent, type WPInfo } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, EmptyState, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
const statusTone: Record<string, "success" | "danger" | "warning" | "neutral"> = { succeeded: "success", failed: "danger", expired: "danger", queued: "warning", running: "warning" };
const statusLabel: Record<string, string> = { succeeded: "installed", failed: "install failed", expired: "install expired", queued: "queued", running: "installing" };

export function CMSPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const { can } = useSession();
  const [list, setList] = useState<CMSInstance[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [installing, setInstalling] = useState(false);
  const [created, setCreated] = useState<{ instance: CMSInstance; password: string } | null>(null);
  const [selected, setSelected] = useState<CMSInstance | null>(null);

  const load = useCallback(async () => {
    try {
      setList((await api<{ instances: CMSInstance[] }>(`/servers/${serverId}/cms`)).instances);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId]);
  useEffect(() => {
    void load();
  }, [load]);
  const busy = list?.some((c) => c.install_status === "queued" || c.install_status === "running") ?? false;
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(() => void load(), 4000);
    return () => clearInterval(t);
  }, [busy, load]);

  if (selected) return <WordPressDetail serverId={serverId} instance={selected} online={online} onBack={() => { setSelected(null); void load(); }} />;
  const canOperate = can("servers.operate");

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">WordPress from the official Docker images (WordPress, MariaDB, WP-CLI). Docker must be installed on the server.</p>
        <div className="flex gap-2">
          {canOperate && <Button size="sm" disabled={!online} onClick={() => setInstalling(true)}>Install WordPress</Button>}
          <Button size="sm" variant="secondary" onClick={() => void load()}>Refresh</Button>
        </div>
      </div>
      {error && <Alert>{error}</Alert>}
      {created && (
        <Alert tone="success" title="Save the WordPress admin password now">
          <p>
            {created.instance.title}: user <span className="font-mono">{created.instance.admin_user}</span>, password{" "}
            <span className="select-all font-mono">{created.password}</span>
          </p>
          <p className="mt-1 text-xs">OSRIVA does not store it and cannot show it again. Change it in WordPress after your first login.</p>
        </Alert>
      )}
      {!list ? (
        !error && <LoadingBlock label="Loading…" />
      ) : list.length === 0 ? (
        <EmptyState title="No CMS installed">Install WordPress in a few minutes. Other CMSes follow in later releases.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Site</Th>
              <Th>Address</Th>
              <Th>Status</Th>
              <Th>Installed</Th>
            </tr>
          </thead>
          <tbody>
            {list.map((c) => (
              <tr key={c.id} className="cursor-pointer hover:bg-surface-2" onClick={() => setSelected(c)}>
                <Td>
                  <p className="font-medium">{c.title}</p>
                  <p className="text-[11px] text-muted">WordPress · {c.name}</p>
                </Td>
                <Td className="font-mono text-[11px]">
                  {c.url}
                  <br />
                  <span className="text-muted">127.0.0.1:{c.host_port}</span>
                </Td>
                <Td>
                  <Badge tone={statusTone[c.install_status]}>{statusLabel[c.install_status]}</Badge>
                  {c.install_error && <p className="mt-0.5 max-w-xs text-[11px] text-danger">{c.install_error}</p>}
                </Td>
                <Td className="text-xs text-muted">{formatDate(c.created_at)}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {installing && (
        <InstallForm
          serverId={serverId}
          onClose={() => setInstalling(false)}
          onCreated={(instance, password) => {
            setInstalling(false);
            setCreated({ instance, password });
            void load();
          }}
        />
      )}
    </div>
  );
}

function InstallForm({ serverId, onClose, onCreated }: { serverId: string; onClose: () => void; onCreated: (c: CMSInstance, password: string) => void }) {
  const { privileged } = useSession();
  const [f, setF] = useState({ name: "", title: "", admin_user: "admin", admin_email: "", host_port: 18095, domain: "", tls_mode: "acme" as TLSMode });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      const r = await privileged(() => api<{ instance: CMSInstance; admin_password: string }>(`/servers/${serverId}/cms/wordpress`, { method: "POST", body: f }));
      onCreated(r.instance, r.admin_password);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const field = (label: string, el: React.ReactNode, hint?: string) => (
    <label className="flex flex-col gap-1">
      {label}
      {el}
      {hint && <span className="text-xs text-muted">{hint}</span>}
    </label>
  );
  return (
    <Modal
      open
      onClose={onClose}
      title="Install WordPress"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={busy} disabled={!f.name || !f.title || !f.admin_email} onClick={() => void submit()}>Install</Button>
        </>
      }
    >
      <div className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto text-sm">
        {field("Name", <input className={inputClass} placeholder="blog" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />, "Lower-case letters, digits and dashes; used for the containers and volumes.")}
        {field("Site title", <input className={inputClass} value={f.title} onChange={(e) => setF({ ...f, title: e.target.value })} />)}
        <div className="flex gap-3">
          {field("Admin user", <input className={inputClass} value={f.admin_user} onChange={(e) => setF({ ...f, admin_user: e.target.value })} />)}
          {field("Admin e-mail", <input className={inputClass} value={f.admin_email} onChange={(e) => setF({ ...f, admin_email: e.target.value })} />)}
        </div>
        {field("Local port", <input type="number" className={inputClass + " w-28"} value={f.host_port} onChange={(e) => setF({ ...f, host_port: Number(e.target.value) })} />, "WordPress listens on 127.0.0.1 only.")}
        {field("Domain (optional)", <input className={inputClass} placeholder="blog.example.com" value={f.domain} onChange={(e) => setF({ ...f, domain: e.target.value })} />, "Creates a website that serves it through the reverse proxy. Without a domain the site URL is http://127.0.0.1:<port>.")}
        {f.domain &&
          field(
            "Certificate",
            <select className={inputClass} value={f.tls_mode} onChange={(e) => setF({ ...f, tls_mode: e.target.value as TLSMode })}>
              <option value="acme">Let&apos;s Encrypt</option>
              <option value="internal">Internal CA</option>
              <option value="none">None: HTTP only</option>
            </select>,
          )}
        <p className="text-xs text-muted">A strong admin password is generated and shown once. The database password is generated on the server and never leaves it.</p>
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}

function WordPressDetail({ serverId, instance: c, online, onBack }: { serverId: string; instance: CMSInstance; online: boolean; onBack: () => void }) {
  const { can, privileged } = useSession();
  const [info, setInfo] = useState<WPInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [output, setOutput] = useState<WPCommandResult | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [removing, setRemoving] = useState(false);

  const load = useCallback(async () => {
    if (!online) return;
    setError(null);
    try {
      setInfo(await runTask<WPInfo>(serverId, "cms.wp.info", { name: c.name }));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId, c.name, online]);
  useEffect(() => {
    void load();
  }, [load]);

  async function command(key: string, type: string, params: Record<string, unknown>, privilegedCall: boolean, done: string) {
    setBusy(key);
    setError(null);
    setNotice(null);
    setOutput(null);
    try {
      const call = () => runTask<WPCommandResult>(serverId, type, params, 45);
      const r = privilegedCall ? await privileged(call) : await call();
      setOutput(r);
      setNotice(r.ok ? done : null);
      if (!r.ok) setError(`${type}: the check did not pass; see the output below.`);
      await load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  const canOperate = can("servers.operate");
  const updates = (list: WPComponent[]) => list.filter((x) => x.update_version).length;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <button className="text-xs text-muted hover:text-fg" onClick={onBack}>← All CMS sites</button>
          <h2 className="text-lg font-semibold">{c.title}</h2>
          <p className="font-mono text-[11px] text-muted">
            <a className="hover:underline" href={c.url} target="_blank" rel="noreferrer">{c.url}</a> · admin {c.admin_user} · 127.0.0.1:{c.host_port}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="secondary" loading={busy === "verify"} disabled={!info?.installed} onClick={() => void command("verify", "cms.wp.verify", { name: c.name }, false, "All core and plugin files match the checksums published by wordpress.org.")}>
            Verify files
          </Button>
          {canOperate && (
            <Button size="sm" variant="ghost" onClick={() => setRemoving(true)}>Remove</Button>
          )}
        </div>
      </div>
      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {!info ? (
        online ? <LoadingBlock label="Asking WordPress…" /> : <EmptyState title="Agent offline" />
      ) : (
        <>
          <div className="flex flex-wrap gap-2 text-sm">
            <Badge tone={info.running ? "success" : "danger"}>WordPress {info.state}</Badge>
            <Badge tone={info.db_state === "running" ? "success" : "danger"}>database {info.db_state || "missing"}</Badge>
            {info.core_version && <Badge tone="neutral">core {info.core_version}</Badge>}
            {info.core_update && <Badge tone="warning">update to {info.core_update} available</Badge>}
            {!info.installed && info.running && <Badge tone="warning">not installed</Badge>}
          </div>
          {canOperate && info.installed && (
            <div className="flex flex-wrap gap-2">
              {(["core", "plugins", "themes"] as const).map((t) => (
                <Button
                  key={t}
                  size="sm"
                  variant="secondary"
                  loading={busy === t}
                  onClick={() => {
                    if (!confirm(`Update WordPress ${t}? Take a backup first if this site matters: updates can break plugins.`)) return;
                    void command(t, "cms.wp.update", { name: c.name, target: t }, true, `${t} updated.`);
                  }}
                >
                  Update {t}
                  {t === "plugins" && updates(info.plugins) > 0 && ` (${updates(info.plugins)})`}
                  {t === "themes" && updates(info.themes) > 0 && ` (${updates(info.themes)})`}
                </Button>
              ))}
            </div>
          )}
          {[["Plugins", info.plugins], ["Themes", info.themes]].map(([title, items]) => (
            <div key={title as string}>
              <h3 className="mb-2 text-sm font-semibold">{title as string}</h3>
              <Table>
                <thead>
                  <tr>
                    <Th>Name</Th>
                    <Th>Status</Th>
                    <Th>Version</Th>
                  </tr>
                </thead>
                <tbody>
                  {(items as WPComponent[]).map((p) => (
                    <tr key={p.name}>
                      <Td className="font-mono text-xs">{p.name}</Td>
                      <Td className="text-xs">{p.status}</Td>
                      <Td className="text-xs">
                        {p.version}
                        {p.update_version && <Badge tone="warning">→ {p.update_version}</Badge>}
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </div>
          ))}
        </>
      )}
      {output && <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded-lg bg-bg p-3 font-mono text-[11px]">{output.output || "(no output)"}</pre>}
      {removing && <RemoveDialog serverId={serverId} instance={c} onClose={() => setRemoving(false)} onDone={onBack} />}
    </div>
  );
}

function RemoveDialog({ serverId, instance: c, onClose, onDone }: { serverId: string; instance: CMSInstance; onClose: () => void; onDone: () => void }) {
  const { privileged } = useSession();
  const [deleteData, setDeleteData] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      const t = await privileged(() => api<Task>(`/servers/${serverId}/cms/${c.id}?delete_data=${deleteData}`, { method: "DELETE" }));
      await waitTask(serverId, t);
      onDone();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`Remove ${c.title}`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={busy} disabled={deleteData && typed !== c.name} onClick={() => void submit()}>Remove</Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <p>The WordPress and database containers{c.domain && <> and the website {c.domain}</>} are removed.</p>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={deleteData} onChange={(e) => setDeleteData(e.target.checked)} /> Also delete the database and all uploaded files
        </label>
        {deleteData ? (
          <label className="flex flex-col gap-1 text-danger">
            This cannot be undone. Type {c.name} to confirm:
            <input className={inputClass} value={typed} onChange={(e) => setTyped(e.target.value)} />
          </label>
        ) : (
          <p className="text-xs text-muted">The volumes osriva-wp-{c.name}-db and osriva-wp-{c.name}-files stay on the server.</p>
        )}
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}
