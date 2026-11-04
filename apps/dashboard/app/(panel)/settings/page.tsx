"use client";

import { useEffect, useState } from "react";
import { api, errorMessage, type APIClient, type JobQueueStatus, type PluginInfo } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, LoadingBlock, PageHeader, Table, Td, Th, cx } from "@/components/ui";

type Mode = "personal" | "provider";

const modes: { value: Mode; title: string; body: string }[] = [
  {
    value: "personal",
    title: "Personal Server Mode",
    body: "For one person or team managing their own VPS or dedicated servers: websites, apps, CMS, databases, Docker and backups.",
  },
  {
    value: "provider",
    title: "Hosting Provider Mode",
    body: "For hosting companies: data centers, nodes, virtualization, VPS and hosting provisioning, plus a separate customer panel. Provider modules arrive in Phases 10–12.",
  },
];

export default function SettingsPage() {
  const { can, privileged, refresh } = useSession();
  const [mode, setMode] = useState<Mode | null>(null);
  const [selected, setSelected] = useState<Mode | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api<{ panel_mode: Mode }>("/settings")
      .then((s) => {
        setMode(s.panel_mode);
        setSelected(s.panel_mode);
      })
      .catch((e) => setError(errorMessage(e)));
  }, []);

  async function save() {
    if (!selected) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const s = await privileged(() => api<{ panel_mode: Mode }>("/settings/panel-mode", { method: "PUT", body: { mode: selected } }));
      setMode(s.panel_mode);
      setSaved(true);
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const writable = can("settings.write");

  return (
    <>
      <PageHeader title="Settings" description="Panel-wide configuration." />
      <Card title="Operating mode" description={writable ? "Changing the mode requires confirming your password." : "Only owners can change the operating mode."}>
        {error && (
          <div className="mb-4">
            <Alert>{error}</Alert>
          </div>
        )}
        {saved && (
          <div className="mb-4">
            <Alert tone="success">Operating mode updated.</Alert>
          </div>
        )}
        {mode === null ? (
          !error && <LoadingBlock />
        ) : (
          <div className="flex flex-col gap-4">
            <div role="radiogroup" aria-label="Operating mode" className="grid gap-3 md:grid-cols-2">
              {modes.map((m) => (
                <button
                  key={m.value}
                  role="radio"
                  aria-checked={selected === m.value}
                  disabled={!writable}
                  onClick={() => setSelected(m.value)}
                  className={cx(
                    "rounded-xl border p-4 text-left transition-colors disabled:cursor-default",
                    selected === m.value ? "border-accent bg-accent-soft" : "border-line hover:bg-surface-2",
                  )}
                >
                  <p className="font-medium">
                    {m.title} {mode === m.value && <span className="text-xs text-muted">(current)</span>}
                  </p>
                  <p className="mt-1 text-sm text-muted">{m.body}</p>
                </button>
              ))}
            </div>
            {writable && (
              <Button className="self-start" loading={busy} disabled={selected === mode} onClick={() => void save()}>
                Save mode
              </Button>
            )}
          </div>
        )}
      </Card>
      <div className="mt-6">
        <AuditRetentionCard />
      </div>
      <div className="mt-6">
        <JobQueueCard />
      </div>
      <div className="mt-6">
        <APIClientsCard />
      </div>
      <div className="mt-6">
        <PluginsCard />
      </div>
    </>
  );
}

function AuditRetentionCard() {
  const { can, privileged } = useSession();
  const [days, setDays] = useState<number | null>(null);
  const [value, setValue] = useState(365);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api<{ audit_retention_days: number }>("/settings")
      .then((s) => {
        setDays(s.audit_retention_days);
        setValue(s.audit_retention_days);
      })
      .catch((e) => setError(errorMessage(e)));
  }, []);
  async function save() {
    if (days != null && value < days && !confirm(`Shorten audit retention to ${value} days? Older entries are deleted within the hour.`)) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const s = await privileged(() => api<{ audit_retention_days: number }>("/settings/audit-retention", { method: "PUT", body: { days: value } }));
      setDays(s.audit_retention_days);
      setSaved(true);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const writable = can("settings.write");
  return (
    <Card title="Audit log retention" description="Entries older than this are deleted automatically (checked every hour). Each cleanup is itself recorded in the audit log.">
      {error && <Alert>{error}</Alert>}
      {saved && <Alert tone="success">Retention saved.</Alert>}
      {days === null ? (
        !error && <LoadingBlock />
      ) : (
        <div className="flex flex-wrap items-end gap-3 text-sm">
          <label className="flex flex-col gap-1">
            Keep entries for (days)
            <input type="number" min={30} max={3650} disabled={!writable} className="h-9 w-32 rounded-lg border border-line bg-surface px-3" value={value} onChange={(e) => setValue(Number(e.target.value))} />
          </label>
          {writable && (
            <Button loading={busy} disabled={value === days} onClick={() => void save()}>
              Save
            </Button>
          )}
          <span className="text-xs text-muted">30 to 3650 days. Metrics: raw samples 24 h, hourly averages 90 days.</span>
        </div>
      )}
    </Card>
  );
}

const jobKinds: Record<string, string> = { "notification.deliver": "Alert notification" };

function JobQueueCard() {
  const [st, setSt] = useState<JobQueueStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const load = () =>
      api<JobQueueStatus>("/jobs")
        .then((s) => {
          setSt(s);
          setError(null);
        })
        .catch((e) => setError(errorMessage(e)));
    void load();
    const t = setInterval(load, 15_000);
    return () => clearInterval(t);
  }, []);
  return (
    <Card
      title="Background jobs"
      description="Durable jobs (alert notifications) are retried with back-off: 30 s, 1, 2, 4 and 8 minutes, then marked failed. Finished jobs are kept 7 days."
    >
      {error && <Alert>{error}</Alert>}
      {!st ? (
        !error && <LoadingBlock />
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          <div className="flex flex-wrap gap-2">
            <Badge>{st.counts.queued} queued</Badge>
            <Badge tone="accent">{st.counts.running} running</Badge>
            <Badge tone="success">{st.counts.succeeded} succeeded</Badge>
            <Badge tone={st.counts.failed > 0 ? "danger" : "neutral"}>{st.counts.failed} failed</Badge>
          </div>
          {st.recent_failures.length > 0 && (
            <Table>
              <thead>
                <tr>
                  <Th>Failed</Th>
                  <Th>Job</Th>
                  <Th>Last error</Th>
                </tr>
              </thead>
              <tbody>
                {st.recent_failures.map((j) => (
                  <tr key={j.id}>
                    <Td className="whitespace-nowrap text-xs text-muted">{formatDate(j.updated_at)}</Td>
                    <Td className="text-xs">{jobKinds[j.kind] ?? j.kind} · {j.attempts}/{j.max_attempts} attempts</Td>
                    <Td className="text-xs text-danger">{j.last_error}</Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </div>
      )}
    </Card>
  );
}

function APIClientsCard() {
  const { can, privileged } = useSession();
  const [clients, setClients] = useState<APIClient[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<{ client: APIClient; secret: string } | null>(null);
  const [form, setForm] = useState({ name: "", write: true, cidrs: "", webhook: "" });
  const [busy, setBusy] = useState(false);
  const load = () =>
    api<{ clients: APIClient[] }>("/api-clients")
      .then((r) => {
        setClients(r.clients);
        setError(null);
      })
      .catch((e) => setError(errorMessage(e)));
  useEffect(() => void load(), []);
  const writable = can("settings.write");

  async function create() {
    setBusy(true);
    setError(null);
    try {
      const body = {
        name: form.name,
        scopes: form.write ? ["provisioning.read", "provisioning.write"] : ["provisioning.read"],
        allowed_cidrs: form.cidrs.split(/[\s,]+/).filter(Boolean),
        webhook_url: form.webhook.trim(),
      };
      setCreated(await privileged(() => api<{ client: APIClient; secret: string }>("/api-clients", { method: "POST", body })));
      setForm({ name: "", write: true, cidrs: "", webhook: "" });
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
    await load();
  }
  async function toggle(c: APIClient) {
    setError(null);
    try {
      await privileged(() => api(`/api-clients/${c.id}`, { method: "PATCH", body: { enabled: !c.enabled } }));
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }
  async function remove(c: APIClient) {
    if (!confirm(`Delete the API client ${c.name}? Requests signed with its key stop working.`)) return;
    setError(null);
    try {
      await privileged(() => api(`/api-clients/${c.id}`, { method: "DELETE" }));
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }
  const input = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
  return (
    <Card
      title="API clients (billing systems)"
      description="Billing systems such as WHMCS, Blesta or HostBill call the signed provisioning API (HMAC-SHA256, nonce, 5-minute window, Idempotency-Key). A client acts with its creator's permissions, limited to its scopes."
    >
      {error && <Alert>{error}</Alert>}
      {created && (
        <div className="mb-3">
          <Alert tone="warning" title={`Secret for ${created.client.name} (shown once)`}>
            <p>Key: <span className="select-all font-mono text-xs">{created.client.key_id}</span></p>
            <p className="select-all break-all font-mono text-xs">{created.secret}</p>
            <p className="mt-1 text-xs">Store it in the billing system. Webhooks are signed with the same secret: X-Osriva-Signature = sha256=HMAC(secret, timestamp + &quot;.&quot; + body).</p>
          </Alert>
        </div>
      )}
      {!clients ? (
        !error && <LoadingBlock />
      ) : (
        <div className="flex flex-col gap-4 text-sm">
          {clients.length === 0 ? <p className="text-muted">No API clients yet.</p> : (
            <Table>
              <thead><tr><Th>Client</Th><Th>Scopes</Th><Th>Last used</Th><Th /></tr></thead>
              <tbody>
                {clients.map((c) => (
                  <tr key={c.id}>
                    <Td>
                      <p className="font-medium">{c.name} {!c.enabled && <Badge tone="warning">disabled</Badge>}</p>
                      <p className="font-mono text-[11px] text-muted">{c.key_id}</p>
                      {c.allowed_cidrs.length > 0 && <p className="text-[11px] text-muted">from {c.allowed_cidrs.join(", ")}</p>}
                      {c.webhook_url && <p className="text-[11px] text-muted">webhook {c.webhook_url}</p>}
                    </Td>
                    <Td className="text-xs">{c.scopes.join(", ")}</Td>
                    <Td className="text-xs text-muted">{c.last_used_at ? formatDate(c.last_used_at) : "never"}</Td>
                    <Td className="whitespace-nowrap text-right">
                      {writable && (
                        <>
                          <Button size="sm" variant="ghost" onClick={() => void toggle(c)}>{c.enabled ? "Disable" : "Enable"}</Button>
                          <Button size="sm" variant="ghost" onClick={() => void remove(c)}>Delete</Button>
                        </>
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
          {writable && (
            <div className="flex flex-wrap items-end gap-3 border-t border-line pt-4">
              <label className="flex flex-col gap-1">Name<input className={input} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="whmcs" /></label>
              <label className="flex flex-col gap-1">Allowed networks (optional)<input className={input} value={form.cidrs} onChange={(e) => setForm({ ...form, cidrs: e.target.value })} placeholder="203.0.113.10/32" /></label>
              <label className="flex flex-col gap-1">Webhook URL (optional)<input className={input + " w-72"} value={form.webhook} onChange={(e) => setForm({ ...form, webhook: e.target.value })} placeholder="https://billing.example.com/osriva" /></label>
              <label className="flex items-center gap-2"><input type="checkbox" checked={form.write} onChange={(e) => setForm({ ...form, write: e.target.checked })} /> Can create and change services</label>
              <Button loading={busy} disabled={!form.name.trim()} onClick={() => void create()}>Create client</Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

function PluginsCard() {
  const { can, privileged } = useSession();
  const [data, setData] = useState<{ enabled: boolean; plugins: PluginInfo[] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const load = () =>
    api<{ enabled: boolean; plugins: PluginInfo[] }>("/plugins")
      .then((r) => {
        setData(r);
        setError(null);
      })
      .catch((e) => setError(errorMessage(e)));
  useEffect(() => void load(), []);
  const writable = can("settings.write");
  async function run(fn: () => Promise<unknown>) {
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }
  return (
    <Card
      title="Plugins"
      description="Out-of-process plugins signed with a trusted publisher key. Each call runs the plugin as a separate process without the panel's environment. Approving pins the binary; a changed binary stops until it is approved again."
    >
      {error && <Alert>{error}</Alert>}
      {!data ? (
        !error && <LoadingBlock />
      ) : !data.enabled ? (
        <p className="text-sm text-muted">Plugins are off. Set OSRIVA_PLUGIN_DIR and OSRIVA_PLUGIN_TRUSTED_KEYS (base64 Ed25519 public keys) and restart the API.</p>
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          {data.plugins.length === 0 ? <p className="text-muted">No plugins in the plugin directory.</p> : (
            <Table>
              <thead><tr><Th>Plugin</Th><Th>Signature</Th><Th>State</Th><Th /></tr></thead>
              <tbody>
                {data.plugins.map((p) => (
                  <tr key={p.name}>
                    <Td>
                      <p className="font-medium">{p.name} <span className="text-xs text-muted">{p.version}</span></p>
                      <p className="text-xs text-muted">{p.publisher}{p.kinds.length > 0 && ` · ${p.kinds.join(", ")}`}</p>
                    </Td>
                    <Td className="text-xs">
                      {p.valid ? <span className="font-mono">key {p.key_id.slice(0, 12)}…</span> : <span className="text-danger">{p.reason}</span>}
                    </Td>
                    <Td>
                      {p.runnable ? <Badge tone="success">enabled</Badge> : p.enabled && p.approved_sha256 !== p.sha256 ? <Badge tone="danger">changed since approval</Badge> : <Badge>not enabled</Badge>}
                    </Td>
                    <Td className="whitespace-nowrap text-right">
                      {writable && p.valid && !p.runnable && (
                        <Button size="sm" variant="ghost" onClick={() => confirm(`Approve ${p.name} ${p.version}? It runs as the API's user with network access.`) && void run(() => privileged(() => api(`/plugins/${p.name}`, { method: "PUT", body: { enabled: true } })))}>Approve</Button>
                      )}
                      {writable && p.enabled && (
                        <Button size="sm" variant="ghost" onClick={() => void run(() => privileged(() => api(`/plugins/${p.name}`, { method: "PUT", body: { enabled: false } })))}>Disable</Button>
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
          {writable && <div><Button size="sm" variant="secondary" onClick={() => void run(() => api("/plugins/rescan", { method: "POST" }))}>Rescan directory</Button></div>}
        </div>
      )}
    </Card>
  );
}
