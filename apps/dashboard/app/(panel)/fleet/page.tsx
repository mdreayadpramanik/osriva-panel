"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, errorMessage, type Server, type ServerGroup, type Task, type TaskBatch } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, Modal, PageHeader, Table, Td, Th } from "@/components/ui";
import { ConnectionBadge } from "../servers/connection";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";

// Labels for the task types the API allows in batches (GET /fleet/task-types).
const typeLabels: Record<string, string> = {
  "service.action": "Service: start / stop / restart",
  "sshkeys.add": "SSH key: add",
  "sshkeys.remove": "SSH key: remove",
  "system.timezone": "Set time zone",
  "docker.image.pull": "Docker: pull image",
  "docker.images.prune": "Docker: prune unused images",
  "services.list": "List services",
  "processes.list": "List processes",
  "users.list": "List users",
  "sshkeys.list": "List SSH keys",
  "cron.list": "List scheduled tasks",
  "system.time": "Show time and time zone",
  "logs.read": "Read logs",
  "docker.info": "Docker: engine info",
  "docker.containers": "Docker: list containers",
  "docker.images": "Docker: list images",
  "docker.volumes": "Docker: list volumes",
  "docker.networks": "Docker: list networks",
  "proxy.status": "Reverse proxy status",
};

type ParamField = { key: string; label: string; placeholder?: string; options?: string[]; number?: boolean };
const paramFields: Record<string, ParamField[]> = {
  "service.action": [
    { key: "name", label: "Service", placeholder: "nginx.service" },
    { key: "action", label: "Action", options: ["restart", "start", "stop", "enable", "disable"] },
  ],
  "sshkeys.add": [
    { key: "user", label: "User", placeholder: "deploy" },
    { key: "key", label: "Public key", placeholder: "ssh-ed25519 AAAA… comment" },
  ],
  "sshkeys.remove": [
    { key: "user", label: "User", placeholder: "deploy" },
    { key: "fingerprint", label: "Fingerprint", placeholder: "SHA256:…" },
  ],
  "system.timezone": [{ key: "timezone", label: "Time zone", placeholder: "Europe/Berlin" }],
  "docker.image.pull": [{ key: "image", label: "Image", placeholder: "nginx:1.27-alpine" }],
  "logs.read": [
    { key: "source", label: "Source", options: ["system", "service", "application"] },
    { key: "service", label: "Service (source = service)", placeholder: "nginx.service" },
    { key: "min_level", label: "Minimum level", options: ["info", "warning", "error", "debug"] },
    { key: "lines", label: "Lines", number: true, placeholder: "200" },
  ],
};

const statusTone = { queued: "neutral", running: "accent", succeeded: "success", failed: "danger", expired: "warning" } as const;

export default function FleetPage() {
  const { can, privileged } = useSession();
  const [servers, setServers] = useState<Server[] | null>(null);
  const [groups, setGroups] = useState<ServerGroup[]>([]);
  const [batches, setBatches] = useState<TaskBatch[]>([]);
  const [types, setTypes] = useState<string[]>([]);
  const [maxServers, setMaxServers] = useState(200);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [editing, setEditing] = useState<ServerGroup | "new" | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [result, setResult] = useState<{ server: string; task: Task } | null>(null);

  const load = useCallback(async () => {
    try {
      const [s, g, b] = await Promise.all([
        api<{ servers: Server[] }>("/servers"),
        api<{ groups: ServerGroup[] }>("/server-groups"),
        api<{ batches: TaskBatch[] }>("/fleet/batches?limit=25"),
      ]);
      setServers(s.servers);
      setGroups(g.groups);
      setBatches(b.batches);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);
  useEffect(() => {
    void load();
    api<{ types: string[]; max_servers: number }>("/fleet/task-types")
      .then((r) => {
        setTypes(r.types);
        setMaxServers(r.max_servers);
      })
      .catch(() => {});
  }, [load]);
  // Refresh quickly while a batch is still running.
  const active = batches.some((b) => (b.counts.queued ?? 0) + (b.counts.running ?? 0) > 0);
  useEffect(() => {
    const t = setInterval(() => void load(), active ? 3000 : 30_000);
    return () => clearInterval(t);
  }, [load, active]);

  const name = (id: string) => servers?.find((s) => s.id === id)?.name ?? "unknown server";
  const writable = can("servers.write");

  async function removeGroup(g: ServerGroup) {
    if (!confirm(`Delete the group "${g.name}"? Its servers are not affected.`)) return;
    setError(null);
    try {
      await api(`/server-groups/${g.id}`, { method: "DELETE" });
      setNotice(`Group ${g.name} deleted.`);
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }

  async function showResult(t: TaskBatch["tasks"][number]) {
    try {
      setResult({ server: t.server_name, task: await api<Task>(`/servers/${t.server_id}/tasks/${t.task_id}`) });
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  return (
    <>
      <PageHeader
        title="Fleet"
        description="Group servers and run one task on many of them at once. Each server gets an ordinary task, with the same permissions and audit as on its own page."
      />
      {error && <div className="mb-4"><Alert>{error}</Alert></div>}
      {notice && <div className="mb-4"><Alert tone="success">{notice}</Alert></div>}
      {servers === null ? (
        !error && <LoadingBlock />
      ) : (
        <div className="flex flex-col gap-6">
          <Card title="Server groups" actions={writable && <Button size="sm" onClick={() => setEditing("new")}>New group</Button>}>
            {groups.length === 0 ? (
              <EmptyState title="No groups yet">Groups let you target alert rules and fleet tasks at a set of servers.</EmptyState>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Group</Th>
                    <Th>Servers</Th>
                    <Th className="text-right">{writable ? "Actions" : ""}</Th>
                  </tr>
                </thead>
                <tbody>
                  {groups.map((g) => (
                    <tr key={g.id}>
                      <Td>
                        <p className="font-medium">{g.name}</p>
                        {g.description && <p className="text-xs text-muted">{g.description}</p>}
                      </Td>
                      <Td className="text-xs">
                        {g.server_ids.length === 0 ? <span className="text-muted">empty</span> : g.server_ids.map(name).join(", ")}
                      </Td>
                      <Td className="whitespace-nowrap text-right">
                        {writable && (
                          <>
                            <Button size="sm" variant="ghost" onClick={() => setEditing(g)}>Edit</Button>
                            <Button size="sm" variant="ghost" onClick={() => void removeGroup(g)}>Delete</Button>
                          </>
                        )}
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <RunCard
            servers={servers}
            groups={groups}
            types={types}
            maxServers={maxServers}
            onRun={async (body) => {
              setError(null);
              setNotice(null);
              try {
                const b = await privileged(() => api<TaskBatch>("/fleet/batches", { method: "POST", body }));
                setNotice(`Queued on ${b.tasks.length} server(s)${b.skipped.length ? `, ${b.skipped.length} skipped` : ""}.`);
                setOpen(b.id);
              } catch (e) {
                setError(errorMessage(e));
              }
              await load();
            }}
          />

          <Card title="Recent batches" description="Tasks on offline agents are not queued; they are listed as skipped.">
            {batches.length === 0 ? (
              <p className="text-sm text-muted">No fleet tasks have run yet.</p>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>When</Th>
                    <Th>Task</Th>
                    <Th>Target</Th>
                    <Th>Result</Th>
                  </tr>
                </thead>
                <tbody>
                  {batches.map((b) => (
                    <BatchRows key={b.id} b={b} open={open === b.id} onToggle={() => setOpen(open === b.id ? null : b.id)} onResult={showResult} />
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </div>
      )}
      {editing && servers && (
        <GroupForm
          group={editing === "new" ? null : editing}
          servers={servers}
          onClose={() => setEditing(null)}
          onSaved={(msg) => {
            setEditing(null);
            setNotice(msg);
            void load();
          }}
        />
      )}
      {result && (
        <Modal open onClose={() => setResult(null)} title={`${typeLabels[result.task.type] ?? result.task.type} on ${result.server}`}>
          {result.task.error && <div className="mb-3"><Alert>{result.task.error}</Alert></div>}
          <pre className="max-h-[60vh] overflow-auto rounded-lg bg-surface-2 p-3 text-xs">
            {result.task.result === null ? "No result." : JSON.stringify(result.task.result, null, 2).slice(0, 50_000)}
          </pre>
        </Modal>
      )}
    </>
  );
}

function BatchRows({ b, open, onToggle, onResult }: { b: TaskBatch; open: boolean; onToggle: () => void; onResult: (t: TaskBatch["tasks"][number]) => void }) {
  const params = Object.entries(b.params ?? {}).map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`).join(" ");
  return (
    <>
      <tr className="cursor-pointer hover:bg-surface-2" onClick={onToggle}>
        <Td className="whitespace-nowrap text-xs text-muted">{formatDate(b.created_at)}</Td>
        <Td>
          <p className="text-sm">{typeLabels[b.type] ?? b.type}</p>
          {params && <p className="max-w-xs truncate font-mono text-[11px] text-muted">{params}</p>}
        </Td>
        <Td className="text-xs">{b.target}</Td>
        <Td className="flex flex-wrap gap-1">
          {(Object.keys(statusTone) as (keyof typeof statusTone)[]).map((s) =>
            b.counts[s] ? <Badge key={s} tone={statusTone[s]}>{b.counts[s]} {s}</Badge> : null,
          )}
          {b.skipped.length > 0 && <Badge>{b.skipped.length} skipped</Badge>}
        </Td>
      </tr>
      {open && (
        <tr>
          <Td className="bg-surface-2" />
          <Td className="bg-surface-2" colSpan={3}>
            <ul className="flex flex-col gap-1 text-xs">
              {b.tasks.map((t) => (
                <li key={t.task_id} className="flex flex-wrap items-center gap-2">
                  <Badge tone={statusTone[t.status]}>{t.status}</Badge>
                  <Link className="font-medium hover:underline" href={`/servers/${t.server_id}`}>{t.server_name}</Link>
                  {t.error && <span className="text-danger">{t.error}</span>}
                  {(t.status === "succeeded" || t.status === "failed") && (
                    <button type="button" className="text-accent hover:underline" onClick={() => onResult(t)}>view result</button>
                  )}
                </li>
              ))}
              {b.skipped.map((s) => (
                <li key={s.server_id} className="flex items-center gap-2 text-muted">
                  <Badge>skipped</Badge>
                  {s.name}: {s.reason}
                </li>
              ))}
            </ul>
          </Td>
        </tr>
      )}
    </>
  );
}

function RunCard({ servers, groups, types, maxServers, onRun }: {
  servers: Server[];
  groups: ServerGroup[];
  types: string[];
  maxServers: number;
  onRun: (body: { type: string; params: Record<string, unknown>; group_id?: string; server_ids?: string[] }) => Promise<void>;
}) {
  const [target, setTarget] = useState<string>(groups[0] ? `g:${groups[0].id}` : "pick");
  const [picked, setPicked] = useState<string[]>([]);
  // When the first group appears, target it once; a later manual choice of
  // "Selected servers…" is left alone.
  const hadGroups = useRef(groups.length > 0);
  useEffect(() => {
    if (!hadGroups.current && groups[0]) {
      hadGroups.current = true;
      if (picked.length === 0) setTarget(`g:${groups[0].id}`);
    }
  }, [groups, picked.length]);
  const [type, setType] = useState("services.list");
  const [params, setParams] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const fields = paramFields[type] ?? [];
  const active = servers.filter((s) => s.status === "active");

  async function run() {
    const p: Record<string, unknown> = {};
    for (const f of fields) {
      const v = params[f.key] ?? f.options?.[0] ?? "";
      if (v === "") continue;
      p[f.key] = f.number ? Number(v) : v;
    }
    const body = target.startsWith("g:") ? { type, params: p, group_id: target.slice(2) } : { type, params: p, server_ids: picked };
    const count = target.startsWith("g:") ? groups.find((g) => `g:${g.id}` === target)?.server_ids.length ?? 0 : picked.length;
    if (!confirm(`Run "${typeLabels[type] ?? type}" on ${count} server(s)? Offline servers are skipped.`)) return;
    setBusy(true);
    try {
      await onRun(body);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Run a task on many servers" description={`Up to ${maxServers} servers per batch. Only tasks that make sense everywhere are offered; file, app, site and backup tasks stay per server.`}>
      <div className="flex flex-col gap-3 text-sm">
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1">
            Target
            <select className={inputClass} value={target} onChange={(e) => setTarget(e.target.value)}>
              {groups.map((g) => <option key={g.id} value={`g:${g.id}`}>Group {g.name} ({g.server_ids.length})</option>)}
              <option value="pick">Selected servers…</option>
            </select>
          </label>
          <label className="flex flex-col gap-1">
            Task
            <select className={inputClass} value={type} onChange={(e) => { setType(e.target.value); setParams({}); }}>
              {[...types].sort((a, b) => (typeLabels[a] ?? a).localeCompare(typeLabels[b] ?? b)).map((t) => <option key={t} value={t}>{typeLabels[t] ?? t}</option>)}
            </select>
          </label>
          {fields.map((f) => (
            <label key={f.key} className="flex flex-col gap-1">
              {f.label}
              {f.options ? (
                <select className={inputClass} value={params[f.key] ?? f.options[0]} onChange={(e) => setParams({ ...params, [f.key]: e.target.value })}>
                  {f.options.map((o) => <option key={o}>{o}</option>)}
                </select>
              ) : (
                <input className={inputClass + (f.key === "key" ? " w-96" : "")} type={f.number ? "number" : "text"} placeholder={f.placeholder}
                  value={params[f.key] ?? ""} onChange={(e) => setParams({ ...params, [f.key]: e.target.value })} />
              )}
            </label>
          ))}
        </div>
        {target === "pick" && (
          <div className="flex max-h-48 flex-col gap-1 overflow-auto rounded-lg border border-line p-2">
            {active.length === 0 && <p className="text-xs text-muted">No enrolled servers.</p>}
            {active.map((s) => (
              <label key={s.id} className="flex items-center gap-2 text-xs">
                <input type="checkbox" aria-label={s.name} checked={picked.includes(s.id)}
                  onChange={(e) => setPicked(e.target.checked ? [...picked, s.id] : picked.filter((x) => x !== s.id))} />
                {s.name} <ConnectionBadge connection={s.connection} />
              </label>
            ))}
          </div>
        )}
        <div>
          <Button loading={busy} disabled={target === "pick" ? picked.length === 0 : !target.startsWith("g:")} onClick={() => void run()}>Run</Button>
        </div>
      </div>
    </Card>
  );
}

function GroupForm({ group, servers, onClose, onSaved }: { group: ServerGroup | null; servers: Server[]; onClose: () => void; onSaved: (msg: string) => void }) {
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [members, setMembers] = useState<string[]>(group?.server_ids ?? []);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      const body = { name, description };
      const g = group
        ? await api<ServerGroup>(`/server-groups/${group.id}`, { method: "PUT", body })
        : await api<ServerGroup>("/server-groups", { method: "POST", body });
      await api(`/server-groups/${g.id}/members`, { method: "PUT", body: { server_ids: members } });
      onSaved(`Group ${g.name} saved with ${members.length} server(s).`);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title={group ? "Edit group" : "New group"}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button loading={busy} disabled={!name.trim()} onClick={() => void submit()}>Save</Button></>}>
      <div className="flex flex-col gap-3 text-sm">
        {error && <Alert>{error}</Alert>}
        <label className="flex flex-col gap-1">Name<input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} placeholder="web" /></label>
        <label className="flex flex-col gap-1">Description<input className={inputClass} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Front-end web servers" /></label>
        <div className="flex flex-col gap-1">
          Servers
          <div className="flex max-h-60 flex-col gap-1 overflow-auto rounded-lg border border-line p-2">
            {servers.length === 0 && <p className="text-xs text-muted">No servers yet.</p>}
            {servers.map((s) => (
              <label key={s.id} className="flex items-center gap-2 text-xs">
                <input type="checkbox" aria-label={s.name} checked={members.includes(s.id)}
                  onChange={(e) => setMembers(e.target.checked ? [...members, s.id] : members.filter((x) => x !== s.id))} />
                {s.name} <ConnectionBadge connection={s.connection} />
              </label>
            ))}
          </div>
        </div>
      </div>
    </Modal>
  );
}
