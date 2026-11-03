"use client";

import { useCallback, useEffect, useState } from "react";
import {
  api,
  errorMessage,
  runTask,
  waitTask,
  type Backup,
  type BackupKind,
  type BackupPolicy,
  type BackupRestoreResult,
  type BackupVerifyResult,
  type CMSInstance,
  type DockerVolume,
  type Task,
} from "@/lib/api";
import { formatBytes, formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
const tone: Record<string, "success" | "danger" | "warning" | "neutral"> = { succeeded: "success", failed: "danger", expired: "danger", queued: "warning", running: "warning" };
const label: Record<string, string> = { succeeded: "complete", failed: "failed", expired: "expired", queued: "queued", running: "running" };

type Target = { kind: BackupKind; target: string; label: string };

export function BackupsPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const { can, privileged } = useSession();
  const [data, setData] = useState<{ backups: Backup[]; policies: BackupPolicy[] } | null>(null);
  const [targets, setTargets] = useState<Target[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [choice, setChoice] = useState("");
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [scheduling, setScheduling] = useState(false);
  const [recoveryKey, setRecoveryKey] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api(`/servers/${serverId}/backups`));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId]);
  const loadTargets = useCallback(async () => {
    const out: Target[] = [];
    try {
      const cms = (await api<{ instances: CMSInstance[] }>(`/servers/${serverId}/cms`)).instances;
      cms.forEach((c) => out.push({ kind: "wordpress", target: c.name, label: `WordPress: ${c.title} (database + files)` }));
    } catch {}
    if (online) {
      try {
        const vols = await runTask<DockerVolume[]>(serverId, "docker.volumes");
        vols
          .filter((v) => !v.name.startsWith("osriva_") && !/^[0-9a-f]{64}$/.test(v.name))
          .forEach((v) => out.push({ kind: "volume", target: v.name, label: `Volume: ${v.name}` }));
      } catch {}
    }
    setTargets(out);
    setChoice((c) => c || (out[0] ? `${out[0].kind}:${out[0].target}` : ""));
  }, [serverId, online]);
  useEffect(() => {
    void load();
    void loadTargets();
  }, [load, loadTargets]);
  const active = data?.backups.some((b) => b.status === "queued" || b.status === "running") ?? false;
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => void load(), 4000);
    return () => clearInterval(t);
  }, [active, load]);

  async function act(key: string, fn: () => Promise<string>) {
    setBusy(key);
    setError(null);
    setNotice(null);
    try {
      setNotice(await fn());
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
      await load();
    }
  }

  const backupNow = () =>
    act("create", async () => {
      const [kind, ...rest] = choice.split(":");
      await api(`/servers/${serverId}/backups`, { method: "POST", body: { kind, target: rest.join(":") } });
      return "Backup started.";
    });
  const verify = (b: Backup) =>
    act(`verify:${b.id}`, async () => {
      const t = await api<Task<BackupVerifyResult>>(`/servers/${serverId}/backups/${b.id}/verify`, { method: "POST" });
      const r = await waitTask(serverId, t, 180);
      if (!r.ok) throw new Error(`Verification failed: ${r.problem}`);
      return `Verified: decrypted and read all ${r.files} entries; checksums match.`;
    });
  const remove = (b: Backup) => {
    if (!confirm(`Delete the backup of ${b.target} from ${formatDate(b.created_at)}? This cannot be undone.`)) return;
    void act(`delete:${b.id}`, async () => {
      await privileged(() => api(`/servers/${serverId}/backups/${b.id}`, { method: "DELETE" }));
      return "Backup deleted.";
    });
  };

  if (!data) return error ? <Alert>{error}</Alert> : <LoadingBlock label="Loading backups…" />;
  const canOperate = can("servers.operate");

  return (
    <div className="flex flex-col gap-4">
      <Card
        title="Encrypted backups"
        description="Archives are encrypted on the server with a public key (age). Only the panel holds the private key; it is sent to the server only to verify or restore."
        actions={
          canOperate && (
            <Button
              size="sm"
              variant="secondary"
              onClick={() =>
                void act("key", async () => {
                  const r = await privileged(() => api<{ recovery_key: string }>(`/servers/${serverId}/backups/recovery-key`, { method: "POST" }));
                  setRecoveryKey(r.recovery_key);
                  return "Recovery key shown below. Store it somewhere safe outside OSRIVA.";
                })
              }
            >
              Show recovery key
            </Button>
          )
        }
      >
        <p className="text-xs text-muted">
          Backups are stored on the server itself (<span className="font-mono">/var/lib/osriva-agent/backups</span>). They protect against mistakes and bad updates, not against losing the server. Remote storage follows in a later release.
        </p>
        {recoveryKey && (
          <div className="mt-3 rounded-lg border border-warning bg-warning-soft p-3 text-xs">
            <p className="font-medium">Recovery key: anyone with it and a backup file can read the backup.</p>
            <p className="mt-1 select-all break-all font-mono">{recoveryKey}</p>
            <p className="mt-1">Without it, backups cannot be restored if the panel database is lost. Decrypt manually with: age -d -i key.txt backup.osb | tar -xz</p>
          </div>
        )}
      </Card>

      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}

      {canOperate && (
        <div className="flex flex-wrap items-center gap-2">
          <select aria-label="What to back up" className={inputClass + " min-w-64"} value={choice} onChange={(e) => setChoice(e.target.value)}>
            {targets.length === 0 && <option value="">No WordPress sites or volumes found</option>}
            {targets.map((t) => (
              <option key={`${t.kind}:${t.target}`} value={`${t.kind}:${t.target}`}>
                {t.label}
              </option>
            ))}
          </select>
          <Button size="sm" loading={busy === "create"} disabled={!choice || !online} onClick={() => void backupNow()}>Back up now</Button>
          <Button size="sm" variant="secondary" disabled={!choice} onClick={() => setScheduling(true)}>Schedule…</Button>
          <span className="text-xs text-muted">Volume backups copy files while containers run; for databases prefer the WordPress backup (consistent dump) or stop the container first.</span>
        </div>
      )}

      {data.policies.length > 0 && (
        <div>
          <h3 className="mb-2 text-sm font-semibold">Schedules</h3>
          <Table>
            <thead>
              <tr>
                <Th>Target</Th>
                <Th>Every</Th>
                <Th>Keeps</Th>
                <Th>Last run</Th>
                {canOperate && <Th className="text-right">Actions</Th>}
              </tr>
            </thead>
            <tbody>
              {data.policies.map((p) => (
                <tr key={p.id}>
                  <Td className="font-mono text-xs">{p.kind}: {p.target}</Td>
                  <Td className="text-xs">{p.interval_hours} h</Td>
                  <Td className="text-xs">{p.keep} newest</Td>
                  <Td className="text-xs text-muted">{p.last_run_at ? formatDate(p.last_run_at) : "not yet"}</Td>
                  {canOperate && (
                    <Td className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => void act(`pol:${p.id}`, async () => {
                        await api(`/servers/${serverId}/backup-policies/${p.id}`, { method: "DELETE" });
                        return "Schedule removed. Existing backups are kept.";
                      })}>Remove</Button>
                    </Td>
                  )}
                </tr>
              ))}
            </tbody>
          </Table>
        </div>
      )}

      {data.backups.length === 0 ? (
        <EmptyState title="No backups yet">Back up a WordPress site or a Docker volume, or set a schedule.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Backup</Th>
              <Th>Status</Th>
              <Th>Size</Th>
              <Th>Checksum (SHA-256)</Th>
              {canOperate && <Th className="text-right">Actions</Th>}
            </tr>
          </thead>
          <tbody>
            {data.backups.map((b) => (
              <tr key={b.id}>
                <Td>
                  <p className="font-mono text-xs">{b.kind}: {b.target}</p>
                  <p className="text-[11px] text-muted">{formatDate(b.created_at)}{b.policy_id && " · scheduled"}</p>
                </Td>
                <Td>
                  <Badge tone={tone[b.status]}>{label[b.status]}</Badge>
                  {b.error && <p className="mt-0.5 max-w-xs text-[11px] text-danger">{b.error}</p>}
                </Td>
                <Td className="text-xs">{b.size != null ? `${formatBytes(b.size)} · ${b.files} files` : "—"}</Td>
                <Td className="font-mono text-[11px] text-muted"><span title={b.sha256 ?? ""}>{b.sha256 ? `${b.sha256.slice(0, 16)}…` : "—"}</span></Td>
                {canOperate && (
                  <Td className="text-right">
                    <div className="flex justify-end gap-1">
                      {b.status === "succeeded" && (
                        <>
                          <Button size="sm" variant="ghost" loading={busy === `verify:${b.id}`} onClick={() => void verify(b)}>Verify</Button>
                          <Button size="sm" variant="secondary" disabled={!online} onClick={() => setRestoring(b)}>Restore</Button>
                        </>
                      )}
                      <Button size="sm" variant="ghost" loading={busy === `delete:${b.id}`} onClick={() => remove(b)}>Delete</Button>
                    </div>
                  </Td>
                )}
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      {restoring && <RestoreDialog serverId={serverId} backup={restoring} onClose={() => setRestoring(null)} onDone={(msg) => { setRestoring(null); setNotice(msg); }} />}
      {scheduling && (
        <ScheduleDialog
          serverId={serverId}
          target={targets.find((t) => `${t.kind}:${t.target}` === choice)!}
          onClose={() => setScheduling(false)}
          onDone={() => {
            setScheduling(false);
            void load();
          }}
        />
      )}
    </div>
  );
}

function RestoreDialog({ serverId, backup: b, onClose, onDone }: { serverId: string; backup: Backup; onClose: () => void; onDone: (msg: string) => void }) {
  const { privileged } = useSession();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      const t = await privileged(() => api<Task<BackupRestoreResult>>(`/servers/${serverId}/backups/${b.id}/restore`, { method: "POST" }));
      const r = await waitTask(serverId, t, 180);
      onDone(`Restored ${r.files} files${r.database ? " and the database" : ""}; ${r.stopped.length} container(s) were stopped during the restore and started again.`);
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
      title="Restore backup"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={busy} disabled={typed !== b.target} onClick={() => void submit()}>Restore</Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <p>
          The current data of <span className="font-mono">{b.target}</span> is <strong>replaced</strong> by the backup from {formatDate(b.created_at)}.
          Containers that use it are stopped during the restore. The archive is fully decrypted and checked first; if anything is wrong, nothing is changed.
        </p>
        <p className="text-xs text-muted">Tip: back up the current state first if you might want it back.</p>
        <label className="flex flex-col gap-1">
          Type {b.target} to confirm
          <input className={inputClass} value={typed} onChange={(e) => setTyped(e.target.value)} />
        </label>
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}

function ScheduleDialog({ serverId, target, onClose, onDone }: { serverId: string; target: Target; onClose: () => void; onDone: () => void }) {
  const [interval, setInterval_] = useState(24);
  const [keep, setKeep] = useState(7);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      await api(`/servers/${serverId}/backup-policies`, { method: "POST", body: { kind: target.kind, target: target.target, interval_hours: interval, keep } });
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
      title="Schedule backups"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button loading={busy} onClick={() => void submit()}>Save</Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <p className="font-mono text-xs">{target.label}</p>
        <div className="flex gap-3">
          <label className="flex flex-col gap-1">
            Every (hours)
            <input type="number" min={1} max={720} className={inputClass + " w-28"} value={interval} onChange={(e) => setInterval_(Number(e.target.value))} />
          </label>
          <label className="flex flex-col gap-1">
            Keep newest
            <input type="number" min={1} max={100} className={inputClass + " w-28"} value={keep} onChange={(e) => setKeep(Number(e.target.value))} />
          </label>
        </div>
        <p className="text-xs text-muted">The panel starts scheduled backups; the server must be online. Older scheduled backups beyond the limit are deleted; manual backups are never deleted automatically.</p>
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}
