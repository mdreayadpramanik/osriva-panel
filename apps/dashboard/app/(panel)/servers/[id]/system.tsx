"use client";

import { useCallback, useEffect, useState } from "react";
import { errorMessage, runTask } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, Input, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

type TimeInfo = { hostname: string; timezone: string; local_time: string; ntp_synced?: boolean; can_set_hostname: boolean; can_set_timezone: boolean };
type User = { name: string; uid?: string; full_name?: string; home?: string; shell?: string; groups: string[]; system: boolean; enabled: boolean };
type Group = { name: string; gid?: string; members: string[] };
type CronEntry = { source: string; user?: string; schedule: string; command: string; enabled: boolean; next_run?: string };
type SSHKey = { type: string; fingerprint: string; comment?: string; has_options?: boolean };

function useTask<T>(serverId: string, type: string, online: boolean) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setData(await runTask<T>(serverId, type));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [serverId, type]);
  useEffect(() => {
    if (online) void load();
  }, [online, load]);
  return { data, setData, error, loading, load };
}

function TimeCard({ serverId, online }: { serverId: string; online: boolean }) {
  const { can, privileged } = useSession();
  const t = useTask<TimeInfo>(serverId, "system.time", online);
  const [edit, setEdit] = useState<"hostname" | "timezone" | null>(null);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const r = await privileged(() =>
        runTask<TimeInfo>(serverId, edit === "hostname" ? "system.hostname" : "system.timezone", edit === "hostname" ? { hostname: value.trim() } : { timezone: value.trim() }),
      );
      t.setData(r);
      setEdit(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const operate = can("servers.operate");
  return (
    <Card title="Host name and time" actions={<Button size="sm" variant="ghost" loading={t.loading} onClick={() => void t.load()}>Refresh</Button>}>
      {t.error && <Alert>{t.error}</Alert>}
      {!t.data ? (
        t.loading && <LoadingBlock />
      ) : (
        <dl className="divide-y divide-line text-sm">
          <div className="flex items-center justify-between gap-4 py-2">
            <dt className="text-muted">Host name</dt>
            <dd className="flex items-center gap-2 font-mono">
              {t.data.hostname}
              {operate && t.data.can_set_hostname && (
                <Button size="sm" variant="ghost" onClick={() => (setEdit("hostname"), setValue(t.data!.hostname), setError(null))}>
                  Change
                </Button>
              )}
            </dd>
          </div>
          <div className="flex items-center justify-between gap-4 py-2">
            <dt className="text-muted">Timezone</dt>
            <dd className="flex items-center gap-2 font-mono">
              {t.data.timezone || "—"}
              {operate && t.data.can_set_timezone && (
                <Button size="sm" variant="ghost" onClick={() => (setEdit("timezone"), setValue(t.data!.timezone), setError(null))}>
                  Change
                </Button>
              )}
            </dd>
          </div>
          <div className="flex justify-between gap-4 py-2">
            <dt className="text-muted">Server time</dt>
            <dd>{formatDate(t.data.local_time)}</dd>
          </div>
          {t.data.ntp_synced != null && (
            <div className="flex justify-between gap-4 py-2">
              <dt className="text-muted">Clock synchronised (NTP)</dt>
              <dd>{t.data.ntp_synced ? <Badge tone="success">yes</Badge> : <Badge tone="warning">no</Badge>}</dd>
            </div>
          )}
        </dl>
      )}
      <Modal open={!!edit} onClose={() => setEdit(null)} title={edit === "hostname" ? "Change host name" : "Change timezone"}>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          {error && <Alert>{error}</Alert>}
          <Input
            label={edit === "hostname" ? "Host name" : "Timezone"}
            hint={edit === "hostname" ? "Letters, digits and hyphens; dots separate labels. Services may need a restart afterwards." : "An IANA name such as Europe/Berlin (Linux) or a Windows ID such as W. Europe Standard Time."}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            required
            autoFocus
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setEdit(null)}>
              Cancel
            </Button>
            <Button type="submit" loading={busy}>
              Save
            </Button>
          </div>
        </form>
      </Modal>
    </Card>
  );
}

function SSHKeysModal({ serverId, user, onClose }: { serverId: string; user: string | null; onClose: () => void }) {
  const { can, privileged } = useSession();
  const [keys, setKeys] = useState<SSHKey[] | null>(null);
  const [path, setPath] = useState("");
  const [newKey, setNewKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!user) return;
    setError(null);
    try {
      const r = await runTask<{ path: string; keys: SSHKey[] }>(serverId, "sshkeys.list", { user });
      setKeys(r.keys);
      setPath(r.path);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId, user]);

  useEffect(() => {
    setKeys(null);
    setNewKey("");
    void load();
  }, [load]);

  async function add() {
    setBusy("add");
    setError(null);
    try {
      await privileged(() => runTask(serverId, "sshkeys.add", { user, key: newKey.trim() }));
      setNewKey("");
      await load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  async function remove(k: SSHKey) {
    if (!confirm(`Remove ${k.fingerprint}${k.comment ? ` (${k.comment})` : ""} from ${user}? If this is your own key, you may lose SSH access.`)) return;
    setBusy(k.fingerprint);
    setError(null);
    try {
      await privileged(() => runTask(serverId, "sshkeys.remove", { user, fingerprint: k.fingerprint }));
      await load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  const access = can("servers.access");
  return (
    <Modal open={!!user} onClose={onClose} title={`SSH keys of ${user ?? ""}`} footer={<Button variant="secondary" onClick={onClose}>Close</Button>}>
      <div className="flex flex-col gap-4 text-sm">
        {error && <Alert>{error}</Alert>}
        {path && <p className="font-mono text-xs text-muted">{path}</p>}
        {keys === null ? (
          !error && <LoadingBlock />
        ) : keys.length === 0 ? (
          <p className="text-muted">No keys.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-line">
            {keys.map((k) => (
              <li key={k.fingerprint} className="flex items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <p className="truncate font-mono text-xs">{k.fingerprint}</p>
                  <p className="text-xs text-muted">
                    {k.type}
                    {k.comment && ` · ${k.comment}`}
                    {k.has_options && " · has options"}
                  </p>
                </div>
                {access && (
                  <Button size="sm" variant="ghost" loading={busy === k.fingerprint} onClick={() => void remove(k)}>
                    Remove
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {access ? (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void add();
            }}
          >
            <label className="text-sm font-medium" htmlFor="newkey">
              Add a public key
            </label>
            <textarea id="newkey" value={newKey} onChange={(e) => setNewKey(e.target.value)} rows={3} placeholder="ssh-ed25519 AAAA… comment" className="rounded-lg border border-line bg-surface-2 p-2 font-mono text-xs" required />
            <p className="text-xs text-muted">One OpenSSH public key line. Options such as command= are refused; RSA needs at least 2048 bits.</p>
            <Button type="submit" className="self-start" loading={busy === "add"}>
              Add key
            </Button>
          </form>
        ) : (
          <p className="text-xs text-muted">Adding or removing keys needs the servers.access permission.</p>
        )}
      </div>
    </Modal>
  );
}

function UsersCard({ serverId, online, linux }: { serverId: string; online: boolean; linux: boolean }) {
  const t = useTask<{ users: User[]; groups: Group[] }>(serverId, "users.list", online);
  const [showSystem, setShowSystem] = useState(false);
  const [keysFor, setKeysFor] = useState<string | null>(null);
  const users = (t.data?.users ?? []).filter((u) => showSystem || !u.system);
  return (
    <Card
      title="Users"
      description="Local accounts on this server. Creating and deleting accounts is not offered yet."
      actions={
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-2 text-xs text-muted">
            <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} /> System accounts
          </label>
          <Button size="sm" variant="ghost" loading={t.loading} onClick={() => void t.load()}>
            Refresh
          </Button>
        </div>
      }
    >
      {t.error && <Alert>{t.error}</Alert>}
      {!t.data ? (
        t.loading && <LoadingBlock />
      ) : users.length === 0 ? (
        <EmptyState title="No accounts to show" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>User</Th>
              <Th>Groups</Th>
              <Th>{linux ? "Shell" : "Status"}</Th>
              {linux && <Th className="text-right">SSH</Th>}
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.name}>
                <Td>
                  <p className="font-mono text-xs font-medium">
                    {u.name} {u.system && <Badge>system</Badge>}
                  </p>
                  <p className="text-xs text-muted">{[u.full_name, u.home].filter(Boolean).join(" · ")}</p>
                </Td>
                <Td className="text-xs text-muted">{u.groups.join(", ") || "—"}</Td>
                <Td>{linux ? <code className="font-mono text-xs text-muted">{u.shell}</code> : u.enabled ? <Badge tone="success">enabled</Badge> : <Badge>disabled</Badge>}</Td>
                {linux && (
                  <Td className="text-right">
                    {u.enabled && (
                      <Button size="sm" variant="secondary" onClick={() => setKeysFor(u.name)}>
                        Keys
                      </Button>
                    )}
                  </Td>
                )}
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <SSHKeysModal serverId={serverId} user={keysFor} onClose={() => setKeysFor(null)} />
    </Card>
  );
}

function CronCard({ serverId, online }: { serverId: string; online: boolean }) {
  const t = useTask<{ backend: string; entries: CronEntry[] }>(serverId, "cron.list", online);
  return (
    <Card
      title="Scheduled tasks"
      description={t.data?.backend === "windows-task-scheduler" ? "Task Scheduler (built-in \\Microsoft\\ tasks hidden). View only." : "cron: /etc/crontab, /etc/cron.d and user crontabs. View only."}
      actions={<Button size="sm" variant="ghost" loading={t.loading} onClick={() => void t.load()}>Refresh</Button>}
    >
      {t.error && <Alert>{t.error}</Alert>}
      {!t.data ? (
        t.loading && <LoadingBlock />
      ) : t.data.entries.length === 0 ? (
        <EmptyState title="No scheduled tasks" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Schedule</Th>
              <Th>Runs as</Th>
              <Th>Command</Th>
              <Th>Source</Th>
            </tr>
          </thead>
          <tbody>
            {t.data.entries.map((c, i) => (
              <tr key={i}>
                <Td className="whitespace-nowrap font-mono text-xs">
                  {c.schedule || "—"} {!c.enabled && <Badge>disabled</Badge>}
                  {c.next_run && <p className="text-[11px] text-muted">next {formatDate(c.next_run)}</p>}
                </Td>
                <Td className="text-xs text-muted">{c.user || "—"}</Td>
                <Td className="max-w-md">
                  <code className="block truncate font-mono text-[11px]" title={c.command}>
                    {c.command}
                  </code>
                </Td>
                <Td className="max-w-[12rem] truncate font-mono text-[11px] text-muted" >
                  <span title={c.source}>{c.source}</span>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Card>
  );
}

export function SystemPanel({ serverId, online, osFamily }: { serverId: string; online: boolean; osFamily: string }) {
  if (!online) return <EmptyState title="Agent offline">System settings can only be read while the agent is online.</EmptyState>;
  return (
    <div className="flex flex-col gap-6">
      <TimeCard serverId={serverId} online={online} />
      <UsersCard serverId={serverId} online={online} linux={osFamily === "linux"} />
      <CronCard serverId={serverId} online={online} />
    </div>
  );
}
