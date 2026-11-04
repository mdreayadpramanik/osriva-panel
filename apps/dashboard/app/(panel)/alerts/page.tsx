"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type AlertEvent, type AlertMetric, type AlertRule, type ChannelKind, type NotificationChannel, type PluginInfo, type Server, type ServerGroup } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, Modal, PageHeader, Table, Td, Th } from "@/components/ui";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
const metricInfo: Record<AlertMetric, { label: string; unit: string; hint: string }> = {
  cpu: { label: "CPU usage", unit: "%", hint: "Average CPU over the window is above the threshold." },
  memory: { label: "Memory usage", unit: "%", hint: "Average memory use over the window is above the threshold." },
  disk: { label: "Disk usage", unit: "%", hint: "The fullest disk stayed above the threshold for the whole window." },
  load: { label: "Load average (1 min)", unit: "", hint: "Average load over the window is above the threshold." },
  offline: { label: "Agent offline", unit: "min", hint: "No report from the agent for longer than the threshold (minutes)." },
  backup: { label: "Backup failed", unit: "", hint: "The newest backup of the server failed or expired." },
};

export default function AlertsPage() {
  const { can } = useSession();
  const [channels, setChannels] = useState<NotificationChannel[] | null>(null);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [groups, setGroups] = useState<ServerGroup[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [editingRule, setEditingRule] = useState<AlertRule | "new" | null>(null);
  const [addingChannel, setAddingChannel] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [c, r, e, s, g] = await Promise.all([
        api<{ channels: NotificationChannel[] }>("/alerts/channels"),
        api<{ rules: AlertRule[] }>("/alerts/rules"),
        api<{ events: AlertEvent[] }>("/alerts/events?limit=50"),
        api<{ servers: Server[] }>("/servers").catch(() => ({ servers: [] as Server[] })),
        api<{ groups: ServerGroup[] }>("/server-groups").catch(() => ({ groups: [] as ServerGroup[] })),
      ]);
      setChannels(c.channels);
      setRules(r.rules);
      setEvents(e.events);
      setServers(s.servers);
      setGroups(g.groups);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), 30_000);
    return () => clearInterval(t);
  }, [load]);

  async function act(fn: () => Promise<string>) {
    setError(null);
    setNotice(null);
    try {
      setNotice(await fn());
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      await load();
    }
  }

  const writable = can("alerts.write");
  const target = (r: AlertRule) =>
    r.group_id ? `group ${groups.find((g) => g.id === r.group_id)?.name ?? "?"}` : r.server_id ? servers.find((s) => s.id === r.server_id)?.name ?? "unknown server" : "all servers";
  const channelName = (id: string) => channels?.find((c) => c.id === id)?.name ?? "?";

  return (
    <>
      <PageHeader title="Alerts" description="Rules are checked every 30 seconds against live metrics and server state. Notifications go to webhooks and e-mail." />
      {error && <div className="mb-4"><Alert>{error}</Alert></div>}
      {notice && <div className="mb-4"><Alert tone="success">{notice}</Alert></div>}
      {secret && (
        <div className="mb-4">
          <Alert tone="warning" title="Webhook signing secret (shown once)">
            <p className="select-all break-all font-mono text-xs">{secret}</p>
            <p className="mt-1 text-xs">
              Each request carries X-Osriva-Timestamp and X-Osriva-Signature: sha256=HMAC-SHA256(secret, timestamp + &quot;.&quot; + body). Verify it and reject old timestamps.
            </p>
          </Alert>
        </div>
      )}
      {!channels ? (
        !error && <LoadingBlock />
      ) : (
        <div className="flex flex-col gap-6">
          <Card title="Recent alerts" description="Firing and resolved events with their delivery result.">
            {events.length === 0 ? (
              <p className="text-sm text-muted">Nothing has fired yet.</p>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>When</Th>
                    <Th>Event</Th>
                    <Th>Delivery</Th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((e) => (
                    <tr key={e.id}>
                      <Td className="whitespace-nowrap text-xs text-muted">{formatDate(e.created_at)}</Td>
                      <Td>
                        <Badge tone={e.state === "firing" ? "danger" : e.state === "resolved" ? "success" : "neutral"}>{e.state}</Badge>{" "}
                        <span className="text-xs">{e.message.replace(/^\[OSRIVA\] /, "")}</span>
                      </Td>
                      <Td className="text-xs">
                        {e.delivered} sent{e.failed > 0 && <span className="text-danger">, {e.failed} failed</span>}
                        {e.pending > 0 && <span className="text-warning">, {e.pending} retrying</span>}
                        {e.delivery_error && <p className="max-w-xs text-[11px] text-danger">{e.delivery_error}</p>}
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card title="Rules" actions={writable && <Button size="sm" onClick={() => setEditingRule("new")}>Add rule</Button>}>
            {rules.length === 0 ? (
              <EmptyState title="No rules">Add a rule, for example CPU above 90% for 5 minutes, or the agent offline for 5 minutes.</EmptyState>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Rule</Th>
                    <Th>Condition</Th>
                    <Th>Notify</Th>
                    {writable && <Th className="text-right">Actions</Th>}
                  </tr>
                </thead>
                <tbody>
                  {rules.map((r) => (
                    <tr key={r.id}>
                      <Td>
                        <p className="font-medium">{r.name}</p>
                        <p className="text-[11px] text-muted">{target(r)}{!r.enabled && " · disabled"}</p>
                      </Td>
                      <Td className="text-xs">
                        {metricInfo[r.metric].label}
                        {r.metric !== "backup" && <> &gt; {r.threshold}{metricInfo[r.metric].unit}</>}
                        {!["offline", "backup"].includes(r.metric) && <> for {r.duration_minutes} min</>}
                      </Td>
                      <Td className="text-xs">{r.channel_ids.length ? r.channel_ids.map(channelName).join(", ") : <span className="text-muted">history only</span>}</Td>
                      {writable && (
                        <Td className="text-right">
                          <Button size="sm" variant="ghost" onClick={() => setEditingRule(r)}>Edit</Button>
                          <Button size="sm" variant="ghost" onClick={() => confirm(`Delete rule ${r.name}?`) && void act(async () => {
                            await api(`/alerts/rules/${r.id}`, { method: "DELETE" });
                            return "Rule deleted.";
                          })}>Delete</Button>
                        </Td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card title="Notification channels" actions={writable && <Button size="sm" variant="secondary" onClick={() => setAddingChannel(true)}>Add channel</Button>}>
            {channels.length === 0 ? (
              <p className="text-sm text-muted">No channels. Events are still recorded above.</p>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Channel</Th>
                    <Th>Target</Th>
                    {writable && <Th className="text-right">Actions</Th>}
                  </tr>
                </thead>
                <tbody>
                  {channels.map((c) => (
                    <tr key={c.id}>
                      <Td>
                        <p className="font-medium">{c.name}</p>
                        <p className="text-[11px] text-muted">{c.kind}{!c.enabled && " · disabled"}</p>
                      </Td>
                      <Td className="font-mono text-[11px]">
                        {c.kind === "webhook" ? String(c.config.url ?? "") : c.kind === "plugin" ? `plugin ${String(c.config.plugin ?? "")}` : `${c.config.host}:${c.config.port} (${c.config.tls}) → ${(c.config.to as string[] | undefined)?.join(", ")}`}
                      </Td>
                      {writable && (
                        <Td className="text-right">
                          <Button size="sm" variant="secondary" onClick={() => void act(async () => {
                            const e = await api<AlertEvent>(`/alerts/channels/${c.id}/test`, { method: "POST" });
                            if (e.failed) throw new Error(`Test failed: ${e.delivery_error}`);
                            return `Test notification sent to ${c.name}.`;
                          })}>Test</Button>
                          <Button size="sm" variant="ghost" onClick={() => confirm(`Delete channel ${c.name}? Rules stop notifying it.`) && void act(async () => {
                            await api(`/alerts/channels/${c.id}`, { method: "DELETE" });
                            return "Channel deleted.";
                          })}>Delete</Button>
                        </Td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </div>
      )}
      {editingRule && channels && (
        <RuleForm
          rule={editingRule === "new" ? null : editingRule}
          channels={channels}
          servers={servers}
          groups={groups}
          onClose={() => setEditingRule(null)}
          onSaved={() => {
            setEditingRule(null);
            void load();
          }}
        />
      )}
      {addingChannel && (
        <ChannelForm
          onClose={() => setAddingChannel(false)}
          onSaved={(s) => {
            setAddingChannel(false);
            setSecret(s);
            void load();
          }}
        />
      )}
    </>
  );
}

function RuleForm({ rule, channels, servers, groups, onClose, onSaved }: { rule: AlertRule | null; channels: NotificationChannel[]; servers: Server[]; groups: ServerGroup[]; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState({
    name: rule?.name ?? "",
    target: rule?.group_id ? `g:${rule.group_id}` : rule?.server_id ? `s:${rule.server_id}` : "",
    metric: rule?.metric ?? ("cpu" as AlertMetric),
    threshold: rule?.threshold ?? 90,
    duration_minutes: rule?.duration_minutes ?? 5,
    channel_ids: rule?.channel_ids ?? channels.map((c) => c.id),
    enabled: rule?.enabled ?? true,
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit() {
    setBusy(true);
    setError(null);
    const { target, ...rest } = f;
    const body = { ...rest, server_id: target.startsWith("s:") ? target.slice(2) : null, group_id: target.startsWith("g:") ? target.slice(2) : null };
    try {
      await (rule ? api(`/alerts/rules/${rule.id}`, { method: "PUT", body }) : api("/alerts/rules", { method: "POST", body }));
      onSaved();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const info = metricInfo[f.metric];
  return (
    <Modal open onClose={onClose} title={rule ? "Edit rule" : "Add rule"} footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button loading={busy} disabled={!f.name} onClick={() => void submit()}>Save</Button></>}>
      <div className="flex flex-col gap-3 text-sm">
        <label className="flex flex-col gap-1">Name<input className={inputClass} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="CPU high" /></label>
        <label className="flex flex-col gap-1">
          Applies to
          <select className={inputClass} value={f.target} onChange={(e) => setF({ ...f, target: e.target.value })}>
            <option value="">All servers</option>
            {groups.length > 0 && (
              <optgroup label="Server groups">
                {groups.map((g) => <option key={g.id} value={`g:${g.id}`}>{g.name} ({g.server_ids.length})</option>)}
              </optgroup>
            )}
            <optgroup label="Servers">
              {servers.map((s) => <option key={s.id} value={`s:${s.id}`}>{s.name}</option>)}
            </optgroup>
          </select>
        </label>
        <label className="flex flex-col gap-1">
          Condition
          <select className={inputClass} value={f.metric} onChange={(e) => {
            const m = e.target.value as AlertMetric;
            setF({ ...f, metric: m, threshold: m === "offline" ? 5 : m === "load" ? 4 : m === "backup" ? 0 : 90 });
          }}>
            {(Object.keys(metricInfo) as AlertMetric[]).map((m) => <option key={m} value={m}>{metricInfo[m].label}</option>)}
          </select>
          <span className="text-xs text-muted">{info.hint}</span>
        </label>
        {f.metric !== "backup" && (
          <div className="flex gap-3">
            <label className="flex flex-col gap-1">Threshold{info.unit && ` (${info.unit})`}<input type="number" className={inputClass + " w-28"} value={f.threshold} onChange={(e) => setF({ ...f, threshold: Number(e.target.value) })} /></label>
            {f.metric !== "offline" && (
              <label className="flex flex-col gap-1">For (minutes)<input type="number" min={1} className={inputClass + " w-28"} value={f.duration_minutes} onChange={(e) => setF({ ...f, duration_minutes: Number(e.target.value) })} /></label>
            )}
          </div>
        )}
        <fieldset className="flex flex-col gap-1">
          <legend className="mb-1">Notify</legend>
          {channels.length === 0 && <span className="text-xs text-muted">No channels yet: events are only recorded.</span>}
          {channels.map((c) => (
            <label key={c.id} className="flex items-center gap-2">
              <input type="checkbox" checked={f.channel_ids.includes(c.id)} onChange={(e) => setF({ ...f, channel_ids: e.target.checked ? [...f.channel_ids, c.id] : f.channel_ids.filter((x) => x !== c.id) })} />
              {c.name} <span className="text-xs text-muted">({c.kind})</span>
            </label>
          ))}
        </fieldset>
        <label className="flex items-center gap-2"><input type="checkbox" checked={f.enabled} onChange={(e) => setF({ ...f, enabled: e.target.checked })} /> Enabled</label>
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}

function ChannelForm({ onClose, onSaved }: { onClose: () => void; onSaved: (webhookSecret: string | null) => void }) {
  const [kind, setKind] = useState<ChannelKind>("webhook");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [mail, setMail] = useState({ host: "", port: 587, tls: "starttls", username: "", password: "", from: "", to: "" });
  const [plug, setPlug] = useState({ plugin: "", settings: "{}", secret: "" });
  const [notifiers, setNotifiers] = useState<PluginInfo[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api<{ plugins: PluginInfo[] }>("/plugins")
      .then((r) => {
        const usable = r.plugins.filter((p) => p.runnable && p.kinds.includes("notifier"));
        setNotifiers(usable);
        setPlug((x) => ({ ...x, plugin: x.plugin || usable[0]?.name || "" }));
      })
      .catch(() => setNotifiers([]));
  }, []);
  async function submit() {
    setBusy(true);
    setError(null);
    let settings: unknown;
    if (kind === "plugin") {
      try {
        settings = JSON.parse(plug.settings || "{}");
      } catch {
        setError("Plugin settings must be JSON.");
        setBusy(false);
        return;
      }
    }
    const body =
      kind === "webhook"
        ? { name, kind, enabled: true, config: { url } }
        : kind === "plugin"
        ? { name, kind, enabled: true, config: { plugin: plug.plugin, settings }, secret: plug.secret || null }
        : {
            name,
            kind,
            enabled: true,
            config: { host: mail.host, port: mail.port, tls: mail.tls, username: mail.username, from: mail.from, to: mail.to.split(",").map((s) => s.trim()).filter(Boolean) },
            secret: mail.password || null,
          };
    try {
      const r = await api<{ webhook_secret?: string }>("/alerts/channels", { method: "POST", body });
      onSaved(r.webhook_secret ?? null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const field = (label: string, el: React.ReactNode) => <label className="flex flex-col gap-1">{label}{el}</label>;
  return (
    <Modal open onClose={onClose} title="Add notification channel" footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button loading={busy} disabled={!name} onClick={() => void submit()}>Save</Button></>}>
      <div className="flex flex-col gap-3 text-sm">
        {field("Name", <input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} placeholder="Ops team" />)}
        {field("Type", (
          <select className={inputClass} value={kind} onChange={(e) => setKind(e.target.value as ChannelKind)}>
            <option value="webhook">Webhook (signed JSON POST)</option>
            <option value="email">E-mail (SMTP)</option>
            <option value="plugin">Plugin (approved notifier)</option>
          </select>
        ))}
        {kind === "webhook" ? (
          field("URL", <input className={inputClass} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://hooks.example.com/osriva" />)
        ) : kind === "plugin" ? (
          notifiers.length === 0 ? (
            <Alert tone="warning">No approved notifier plugin. Install a signed plugin and approve it under Settings → Plugins.</Alert>
          ) : (
            <>
              {field("Plugin", (
                <select className={inputClass} value={plug.plugin} onChange={(e) => setPlug({ ...plug, plugin: e.target.value })}>
                  {notifiers.map((p) => <option key={p.name} value={p.name}>{p.name} {p.version} ({p.publisher || "unknown publisher"})</option>)}
                </select>
              ))}
              {field("Settings (JSON, plugin-specific)", <textarea className="min-h-20 rounded-lg border border-line bg-surface p-2 font-mono text-xs" value={plug.settings} onChange={(e) => setPlug({ ...plug, settings: e.target.value })} />)}
              {field("Secret (optional, e.g. a webhook URL with a token)", <input type="password" autoComplete="off" className={inputClass} value={plug.secret} onChange={(e) => setPlug({ ...plug, secret: e.target.value })} />)}
              <p className="text-xs text-muted">The secret is stored encrypted and reaches the plugin only on its standard input.</p>
            </>
          )
        ) : (
          <>
            <div className="flex gap-3">
              {field("SMTP host", <input className={inputClass} value={mail.host} onChange={(e) => setMail({ ...mail, host: e.target.value })} placeholder="smtp.example.com" />)}
              {field("Port", <input type="number" className={inputClass + " w-24"} value={mail.port} onChange={(e) => setMail({ ...mail, port: Number(e.target.value) })} />)}
            </div>
            {field("Security", (
              <select className={inputClass} value={mail.tls} onChange={(e) => setMail({ ...mail, tls: e.target.value })}>
                <option value="starttls">STARTTLS (port 587)</option>
                <option value="tls">TLS (port 465)</option>
                <option value="none">None (local relay only; no password over plain text)</option>
              </select>
            ))}
            <div className="flex gap-3">
              {field("User name (optional)", <input className={inputClass} value={mail.username} onChange={(e) => setMail({ ...mail, username: e.target.value })} />)}
              {field("Password", <input type="password" autoComplete="new-password" className={inputClass} value={mail.password} onChange={(e) => setMail({ ...mail, password: e.target.value })} />)}
            </div>
            {field("From", <input className={inputClass} value={mail.from} onChange={(e) => setMail({ ...mail, from: e.target.value })} placeholder="osriva@example.com" />)}
            {field("To (comma-separated)", <input className={inputClass} value={mail.to} onChange={(e) => setMail({ ...mail, to: e.target.value })} placeholder="ops@example.com" />)}
            <p className="text-xs text-muted">The password is stored encrypted and never shown again.</p>
          </>
        )}
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}
