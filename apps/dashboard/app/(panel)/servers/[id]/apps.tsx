"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type App, type AppInput, type BuildMode, type Deployment } from "@/lib/api";
import { formatDate, formatDuration, relativeTime } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, EmptyState, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

const statusTone: Record<string, "success" | "danger" | "warning" | "neutral"> = {
  succeeded: "success",
  failed: "danger",
  expired: "danger",
  queued: "warning",
  running: "warning",
};
const statusLabel: Record<string, string> = { succeeded: "live", running: "deploying", queued: "queued", failed: "failed", expired: "expired" };

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
const pending = (d: Deployment) => d.status === "queued" || d.status === "running";

export function AppsPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const { can } = useSession();
  const [apps, setApps] = useState<App[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      setApps((await api<{ apps: App[] }>(`/servers/${serverId}/apps`)).apps);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId]);
  useEffect(() => {
    void load();
  }, [load]);

  if (selected) return <AppDetail serverId={serverId} appId={selected} online={online} onBack={() => { setSelected(null); void load(); }} />;

  const canOperate = can("servers.operate");
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">Applications are built from a Git repository with Docker and run as one container on this server.</p>
        <div className="flex gap-2">
          {canOperate && <Button size="sm" onClick={() => setCreating(true)}>New app</Button>}
          <Button size="sm" variant="secondary" onClick={() => void load()}>Refresh</Button>
        </div>
      </div>
      {!online && <Alert tone="neutral">The agent is offline: deployments will wait for it (up to 2 minutes) and then expire.</Alert>}
      {error && <Alert>{error}</Alert>}
      {!apps ? (
        !error && <LoadingBlock label="Loading apps…" />
      ) : apps.length === 0 ? (
        <EmptyState title="No apps yet">Create an app from a public Git repository. Docker and Git must be installed on the server.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>App</Th>
              <Th>Source</Th>
              <Th>Live version</Th>
              <Th>Port</Th>
            </tr>
          </thead>
          <tbody>
            {apps.map((a) => (
              <tr key={a.id} className="cursor-pointer hover:bg-surface-2" onClick={() => setSelected(a.id)}>
                <Td>
                  <p className="font-medium">{a.name}</p>
                  <p className="text-[11px] text-muted">{a.build_mode}</p>
                </Td>
                <Td className="font-mono text-[11px]">
                  {a.git_url}
                  <br />
                  <span className="text-muted">{a.git_ref}</span>
                </Td>
                <Td>
                  {a.current ? (
                    <>
                      <Badge tone="success">{a.current.result?.commit?.slice(0, 8) ?? "live"}</Badge>
                      <p className="mt-0.5 text-[11px] text-muted">{relativeTime(a.current.finished_at)}</p>
                    </>
                  ) : (
                    <span className="text-xs text-muted">never deployed</span>
                  )}
                </Td>
                <Td className="font-mono text-xs">
                  {a.public ? "*" : "127.0.0.1"}:{a.host_port} → {a.container_port}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {creating && (
        <AppForm
          serverId={serverId}
          onClose={() => setCreating(false)}
          onSaved={(a) => {
            setCreating(false);
            setSelected(a.id);
          }}
        />
      )}
    </div>
  );
}

function AppDetail({ serverId, appId, online, onBack }: { serverId: string; appId: string; online: boolean; onBack: () => void }) {
  const { can, privileged } = useSession();
  const [app, setApp] = useState<App | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [logsFor, setLogsFor] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const base = `/servers/${serverId}/apps/${appId}`;

  const load = useCallback(async () => {
    try {
      setApp(await api<App>(base));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [base]);
  useEffect(() => {
    void load();
  }, [load]);
  const active = app?.deployments?.some(pending) ?? false;
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => void load(), 3000);
    return () => clearInterval(t);
  }, [active, load]);

  async function run(key: string, fn: () => Promise<unknown>, msg: string) {
    setBusy(key);
    setError(null);
    setNotice(null);
    try {
      await privileged(fn);
      setNotice(msg);
      await load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  if (!app) return error ? <Alert>{error}</Alert> : <LoadingBlock label="Loading app…" />;
  const canOperate = can("servers.operate");
  const deployments = app.deployments ?? [];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <button className="text-xs text-muted hover:text-fg" onClick={onBack}>← All apps</button>
          <h2 className="text-lg font-semibold">{app.name}</h2>
          <p className="font-mono text-[11px] text-muted">
            {app.git_url} · {app.git_ref} · {app.build_mode}
            {app.build_context && <> · {app.build_context}</>} · {app.public ? "public" : "127.0.0.1"}:{app.host_port} → {app.container_port}
          </p>
          {app.env_names.length > 0 && <p className="text-[11px] text-muted">Environment: {app.env_names.join(", ")} (values hidden)</p>}
        </div>
        {canOperate && (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" loading={busy === "deploy"} disabled={active || !online} onClick={() => void run("deploy", () => api(`${base}/deployments`, { method: "POST" }), "Deployment queued.")}>
              Deploy {app.git_ref}
            </Button>
            <Button size="sm" variant="secondary" onClick={() => setEditing(true)}>Settings</Button>
            <Button
              size="sm"
              variant="ghost"
              loading={busy === "delete"}
              onClick={() => {
                if (!confirm(`Delete ${app.name}? Its container and builds are removed from the server and its history from the panel.`)) return;
                void run("delete", () => api(base, { method: "DELETE" }), "").then(onBack);
              }}
            >
              Delete
            </Button>
          </div>
        )}
      </div>
      {!app.public && <Alert tone="neutral">The app listens on 127.0.0.1:{app.host_port} only. Put a reverse proxy in front of it (Phase 5) or make the port public in Settings.</Alert>}
      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {deployments.length === 0 ? (
        <EmptyState title="Not deployed yet">Press Deploy to fetch {app.git_ref}, build it and start it.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Deployment</Th>
              <Th>Status</Th>
              <Th>Commit</Th>
              <Th>Duration</Th>
              <Th className="text-right">Actions</Th>
            </tr>
          </thead>
          <tbody>
            {deployments.map((d) => (
              <tr key={d.id}>
                <Td>
                  <p className="text-xs">{d.kind === "rollback" ? "Rollback" : `Deploy ${d.git_ref}`}</p>
                  <p className="text-[11px] text-muted">{formatDate(d.created_at)}</p>
                </Td>
                <Td>
                  <Badge tone={app.current?.id === d.id ? "success" : d.status === "succeeded" ? "neutral" : statusTone[d.status]}>
                    {app.current?.id === d.id ? "live" : d.status === "succeeded" ? "previous" : statusLabel[d.status]}
                  </Badge>
                  {d.result?.recovered && <p className="mt-0.5 text-[11px] text-warning">previous version restarted</p>}
                  {d.error && <p className="mt-0.5 max-w-xs text-[11px] text-danger">{d.error}</p>}
                </Td>
                <Td className="font-mono text-[11px]">{d.result?.commit?.slice(0, 12) ?? "—"}</Td>
                <Td className="text-xs text-muted">{d.result ? formatDuration(Math.round(d.result.duration_ms / 1000)) : pending(d) ? "…" : "—"}</Td>
                <Td className="text-right">
                  <div className="flex justify-end gap-1">
                    {(d.result || !pending(d)) && <Button size="sm" variant="ghost" onClick={() => setLogsFor(d.id)}>Logs</Button>}
                    {canOperate && d.status === "succeeded" && app.current?.id !== d.id && (
                      <Button
                        size="sm"
                        variant="secondary"
                        loading={busy === d.id}
                        disabled={active || !online}
                        onClick={() => {
                          if (!confirm("Roll back to this build? It runs with the app's current settings and environment.")) return;
                          void run(d.id, () => api(`${base}/deployments/${d.id}/rollback`, { method: "POST" }), "Rollback queued.");
                        }}
                      >
                        Roll back
                      </Button>
                    )}
                  </div>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {logsFor && <DeploymentLogs url={`${base}/deployments/${logsFor}`} onClose={() => setLogsFor(null)} />}
      {editing && (
        <AppForm
          serverId={serverId}
          app={app}
          onClose={() => setEditing(false)}
          onSaved={(a) => {
            setEditing(false);
            setApp(a);
            setNotice("Settings saved. They apply from the next deployment.");
          }}
        />
      )}
    </div>
  );
}

function DeploymentLogs({ url, onClose }: { url: string; onClose: () => void }) {
  const [d, setD] = useState<Deployment | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api<Deployment>(url).then(setD, (e) => setError(errorMessage(e)));
  }, [url]);
  return (
    <Modal open onClose={onClose} title="Deployment log" footer={<Button variant="ghost" onClick={onClose}>Close</Button>}>
      {error && <Alert>{error}</Alert>}
      {!d && !error && <LoadingBlock label="Loading…" />}
      {d && (
        <>
          {d.error && <Alert>{d.error}</Alert>}
          <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap rounded-lg bg-bg p-3 font-mono text-[11px] leading-relaxed">
            {d.result?.logs || (pending(d) ? "The agent sends the log when the deployment finishes." : "No log was reported.")}
          </pre>
        </>
      )}
    </Modal>
  );
}

function AppForm({ serverId, app, onClose, onSaved }: { serverId: string; app?: App; onClose: () => void; onSaved: (a: App) => void }) {
  const { privileged } = useSession();
  const [f, setF] = useState<AppInput>({
    name: app?.name ?? "",
    git_url: app?.git_url ?? "",
    git_ref: app?.git_ref ?? "main",
    build_mode: app?.build_mode ?? "dockerfile",
    build_context: app?.build_context ?? "",
    dockerfile: app?.dockerfile ?? "",
    container_port: app?.container_port ?? 80,
    host_port: app?.host_port ?? 8080,
    public: app?.public ?? false,
  });
  const [env, setEnv] = useState("");
  const [replaceEnv, setReplaceEnv] = useState(!app);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = <K extends keyof AppInput>(k: K, v: AppInput[K]) => setF((x) => ({ ...x, [k]: v }));

  async function submit() {
    setError(null);
    const body: AppInput = { ...f, dockerfile: f.build_mode === "dockerfile" ? f.dockerfile : "" };
    if (replaceEnv) {
      const vars: Record<string, string> = {};
      for (const line of env.split("\n").map((l) => l.trim()).filter(Boolean)) {
        const i = line.indexOf("=");
        if (i < 1) return setError(`Environment line "${line}" must look like KEY=value.`);
        vars[line.slice(0, i)] = line.slice(i + 1);
      }
      body.env = vars;
    }
    setBusy(true);
    try {
      const saved = await privileged(() =>
        app ? api<App>(`/servers/${serverId}/apps/${app.id}`, { method: "PUT", body }) : api<App>(`/servers/${serverId}/apps`, { method: "POST", body }),
      );
      onSaved(saved);
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
      title={app ? `Settings: ${app.name}` : "New app"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={busy} disabled={!f.name || !f.git_url} onClick={() => void submit()}>{app ? "Save" : "Create"}</Button>
        </>
      }
    >
      <div className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto text-sm">
        {!app && field("Name", <input className={inputClass} placeholder="web" value={f.name} onChange={(e) => set("name", e.target.value)} />, "Lower-case letters, digits and dashes. The container is called osriva-app-<name>.")}
        {field("Git repository (https://)", <input className={inputClass} placeholder="https://github.com/you/site.git" value={f.git_url} onChange={(e) => set("git_url", e.target.value)} />, "Public repositories only for now.")}
        {field("Branch or tag", <input className={inputClass} value={f.git_ref} onChange={(e) => set("git_ref", e.target.value)} />)}
        {field(
          "Build",
          <select className={inputClass} value={f.build_mode} onChange={(e) => {
            const m = e.target.value as BuildMode;
            setF((x) => ({ ...x, build_mode: m, container_port: m === "static" ? 80 : m === "node" ? 3000 : x.container_port }));
          }}>
            <option value="dockerfile">Dockerfile in the repository</option>
            <option value="static">Static site (served by nginx)</option>
            <option value="node">Node.js (npm start)</option>
          </select>,
          f.build_mode === "node" ? "Uses node:22-alpine, installs production dependencies and runs npm start. PORT is set to the container port unless you set it yourself." : undefined,
        )}
        {field("Sub-directory (optional)", <input className={inputClass} placeholder="apps/web" value={f.build_context} onChange={(e) => set("build_context", e.target.value)} />)}
        {f.build_mode === "dockerfile" && field("Dockerfile (optional)", <input className={inputClass} placeholder="Dockerfile" value={f.dockerfile} onChange={(e) => set("dockerfile", e.target.value)} />)}
        <div className="flex gap-3">
          {field("Container port", <input type="number" className={inputClass + " w-28"} value={f.container_port} onChange={(e) => set("container_port", Number(e.target.value))} />)}
          {field("Host port", <input type="number" className={inputClass + " w-28"} value={f.host_port} onChange={(e) => set("host_port", Number(e.target.value))} />)}
        </div>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={f.public} onChange={(e) => set("public", e.target.checked)} /> Public port (all interfaces; may be reachable from the internet)
        </label>
        {app && (
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={replaceEnv} onChange={(e) => setReplaceEnv(e.target.checked)} /> Replace environment variables ({app.env_names.length} set)
          </label>
        )}
        {replaceEnv &&
          field(
            "Environment (KEY=value, one per line)",
            <textarea className="min-h-20 rounded-lg border border-line bg-surface p-2 font-mono text-xs" value={env} onChange={(e) => setEnv(e.target.value)} />,
            "Stored encrypted. Values are never shown again, and they are not kept in task records or the audit log.",
          )}
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}
