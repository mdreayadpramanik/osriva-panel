"use client";

import { useEffect, useState } from "react";
import { api, errorMessage, type JobQueueStatus } from "@/lib/api";
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
