"use client";

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { api, ApiError, errorMessage, type Enrollment, type ServerDetail } from "@/lib/api";
import { formatBytes, formatDate, formatDuration, relativeTime } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, PageHeader, Table, Td, Th } from "@/components/ui";
import { CapabilityBadge, ConnectionBadge } from "../connection";
import { EnrollmentInstructions } from "../enrollment";
import { MetricsPanel } from "./metrics";
import { ActivityPanel, ProcessesPanel } from "./processes";
import { ServicesPanel } from "./services";
import { FilesPanel } from "./files";
import { LogsPanel } from "./logs";
import { SystemPanel } from "./system";
import { DockerPanel } from "./docker";
import { AppsPanel } from "./apps";
import { WebsitesPanel } from "./websites";

type Tab = "overview" | "metrics" | "services" | "processes" | "docker" | "apps" | "websites" | "logs" | "files" | "system" | "activity";
const tabs: { key: Tab; label: string }[] = [
  { key: "overview", label: "Overview" },
  { key: "metrics", label: "Live metrics" },
  { key: "services", label: "Services" },
  { key: "processes", label: "Processes" },
  { key: "docker", label: "Docker" },
  { key: "apps", label: "Apps" },
  { key: "websites", label: "Websites" },
  { key: "logs", label: "Logs" },
  { key: "files", label: "Files" },
  { key: "system", label: "System" },
  { key: "activity", label: "Activity" },
];

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 py-1.5 text-sm">
      <dt className="text-muted">{label}</dt>
      <dd className="text-right">{children}</dd>
    </div>
  );
}

function UsageBar({ used, total }: { used: number; total: number }) {
  const pct = total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0;
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-2" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
      <div className={pct >= 90 ? "h-full bg-danger" : pct >= 75 ? "h-full bg-warning" : "h-full bg-accent"} style={{ width: `${pct}%` }} />
    </div>
  );
}

export default function ServerDetailPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const { can, privileged } = useSession();
  const [detail, setDetail] = useState<ServerDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("overview");

  const load = useCallback(() => {
    api<ServerDetail>(`/servers/${id}`)
      .then((d) => {
        setDetail(d);
        setError(null);
      })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setNotFound(true);
        else setError(errorMessage(e));
      });
  }, [id]);

  useEffect(() => {
    load();
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, [load]);

  async function regenerate() {
    setBusy("token");
    setError(null);
    try {
      const r = await api<{ enrollment: Enrollment }>(`/servers/${id}/enrollment-token`, { method: "POST" });
      setEnrollment(r.enrollment);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  async function revoke() {
    if (!detail || !confirm(`Revoke ${detail.server.name}? Its agent will be locked out immediately and must be re-enrolled as a new server.`)) return;
    setBusy("revoke");
    setError(null);
    try {
      await privileged(() => api(`/servers/${id}/revoke`, { method: "POST" }));
      load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  if (notFound) {
    return (
      <EmptyState title="Server not found" action={<Button variant="secondary" onClick={() => router.push("/servers")}>Back to servers</Button>}>
        It may have been removed, or it belongs to another organization.
      </EmptyState>
    );
  }
  if (!detail) return error ? <Alert>{error}</Alert> : <LoadingBlock />;

  const { server: s, inventory: inv, capabilities } = detail;
  const canWrite = can("servers.write");

  return (
    <>
      <div className="mb-2 text-sm">
        <Link href="/servers" className="text-muted hover:text-fg">
          ← Servers
        </Link>
      </div>
      <PageHeader
        title={s.name}
        description={s.hostname ? `${s.hostname} · ${s.last_seen_ip || "no IP reported"}` : "Agent has not reported yet"}
        actions={
          <div className="flex items-center gap-2">
            <ConnectionBadge connection={s.connection} />
            {canWrite && s.status !== "revoked" && (
              <Button variant="danger" size="sm" loading={busy === "revoke"} onClick={() => void revoke()}>
                Revoke agent
              </Button>
            )}
          </div>
        }
      />
      {error && (
        <div className="mb-4">
          <Alert>{error}</Alert>
        </div>
      )}

      {s.status === "pending" && (
        <Card title="Waiting for the agent" description="This server has not enrolled yet." className="mb-6">
          {enrollment ? (
            <EnrollmentInstructions enrollment={enrollment} />
          ) : (
            <div className="flex flex-col gap-3 text-sm text-muted">
              <p>The original enrolment command is never shown again. If it was lost or has expired, create a new one. This invalidates the old token.</p>
              {canWrite && (
                <Button className="self-start" variant="secondary" loading={busy === "token"} onClick={() => void regenerate()}>
                  Create new enrolment command
                </Button>
              )}
            </div>
          )}
        </Card>
      )}

      {s.status === "revoked" && (
        <div className="mb-6">
          <Alert tone="neutral" title="Agent revoked">
            This server&apos;s certificates were revoked. Its agent can no longer report. To manage the machine again, add it as a new server.
          </Alert>
        </div>
      )}

      {s.connection === "offline" && (
        <div className="mb-6">
          <Alert tone="danger" title="Agent offline">
            No report since {formatDate(s.last_seen_at)}. The data below is the last snapshot the agent sent and may be out of date.
          </Alert>
        </div>
      )}

      {s.status === "active" && (
        <div role="tablist" aria-label="Server sections" className="mb-6 flex gap-1 overflow-x-auto border-b border-line">
          {tabs.filter((t) => t.key !== "files" || can("servers.files")).map((t) => (
            <button
              key={t.key}
              role="tab"
              aria-selected={tab === t.key}
              onClick={() => setTab(t.key)}
              className={"-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm " + (tab === t.key ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg")}
            >
              {t.label}
            </button>
          ))}
        </div>
      )}
      {tab === "metrics" && <MetricsPanel serverId={s.id} />}
      {tab === "services" && <ServicesPanel serverId={s.id} online={s.connection === "online"} />}
      {tab === "processes" && <ProcessesPanel serverId={s.id} online={s.connection === "online"} />}
      {tab === "docker" && <DockerPanel serverId={s.id} online={s.connection === "online"} />}
      {tab === "apps" && <AppsPanel serverId={s.id} online={s.connection === "online"} />}
      {tab === "websites" && <WebsitesPanel serverId={s.id} online={s.connection === "online"} />}
      {tab === "logs" && <LogsPanel serverId={s.id} online={s.connection === "online"} osFamily={s.os_family} />}
      {tab === "files" && <FilesPanel serverId={s.id} online={s.connection === "online"} osFamily={s.os_family} />}
      {tab === "system" && <SystemPanel serverId={s.id} online={s.connection === "online"} osFamily={s.os_family} />}
      {tab === "activity" && <ActivityPanel serverId={s.id} />}

      {tab === "overview" && (inv ? (
        <div className="grid gap-6 lg:grid-cols-3">
          <Card title="System">
            <dl className="divide-y divide-line">
              <Fact label="OS">{inv.os.name || inv.os.id}</Fact>
              <Fact label="Version">{inv.os.version || "—"}</Fact>
              <Fact label="Kernel">{inv.os.kernel || "—"}</Fact>
              <Fact label="Architecture">{inv.os.arch}</Fact>
              <Fact label="Uptime">{formatDuration(inv.uptime_seconds)}</Fact>
              <Fact label="Init system">{inv.init_system}</Fact>
              <Fact label="Package managers">{inv.package_managers?.join(", ") || "none found"}</Fact>
              {inv.virtualization && <Fact label="Virtualization">{inv.virtualization}</Fact>}
              <Fact label="Agent privileges">{inv.privileged ? <Badge tone="success">Administrative</Badge> : <Badge tone="warning">Unprivileged</Badge>}</Fact>
            </dl>
          </Card>

          <Card title="Hardware">
            <dl className="divide-y divide-line">
              <Fact label="CPU">{inv.cpu.model || "—"}</Fact>
              <Fact label="Logical cores">{inv.cpu.logical_cores}</Fact>
              <Fact label="Memory">{formatBytes(inv.memory.total_bytes)}</Fact>
              <Fact label="Memory available">{formatBytes(inv.memory.available_bytes)}</Fact>
            </dl>
            <div className="mt-3">
              <UsageBar used={inv.memory.total_bytes - inv.memory.available_bytes} total={inv.memory.total_bytes} />
            </div>
            <p className="mt-3 text-xs text-muted">
              Snapshot from {formatDate(inv.collected_at)}. Live CPU, memory and network metrics arrive in Phase 3.
            </p>
          </Card>

          <Card title="Network">
            {inv.network.length === 0 ? (
              <p className="text-sm text-muted">No interfaces reported.</p>
            ) : (
              <ul className="flex flex-col gap-3 text-sm">
                {inv.network.map((n) => (
                  <li key={n.name}>
                    <p className="font-medium">
                      {n.name} {n.up ? <Badge tone="success">up</Badge> : <Badge>down</Badge>}
                    </p>
                    {n.addresses.map((a) => (
                      <p key={a} className="font-mono text-xs text-muted">
                        {a}
                      </p>
                    ))}
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Storage" className="lg:col-span-3">
            {inv.disks.length === 0 ? (
              <p className="text-sm text-muted">No filesystems reported.</p>
            ) : (
              <Table>
                <thead>
                  <tr>
                    <Th>Mount</Th>
                    <Th>Filesystem</Th>
                    <Th>Size</Th>
                    <Th>Free</Th>
                    <Th className="w-1/3">Used</Th>
                  </tr>
                </thead>
                <tbody>
                  {inv.disks.map((d) => (
                    <tr key={d.mount}>
                      <Td className="font-mono text-xs">{d.mount}</Td>
                      <Td>{d.fs_type || "—"}</Td>
                      <Td>{formatBytes(d.total_bytes)}</Td>
                      <Td>{formatBytes(d.free_bytes)}</Td>
                      <Td>
                        <UsageBar used={d.total_bytes - d.free_bytes} total={d.total_bytes} />
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card
            title="Compatibility"
            description="What OSRIVA can do on this host. SUPPORTED means implemented and tested on this platform. Planned modules are never shown as supported."
            className="lg:col-span-3"
          >
            <ul className="flex flex-col divide-y divide-line">
              {capabilities.map((c) => (
                <li key={c.key} className="flex flex-col gap-1 py-3 sm:flex-row sm:items-start sm:justify-between sm:gap-6">
                  <div className="min-w-0">
                    <p className="font-medium">
                      {c.title} {c.phase > 0 && <span className="text-xs font-normal text-muted">· module planned for Phase {c.phase}</span>}
                    </p>
                    <p className="text-sm text-muted">{c.reason}</p>
                    {c.alternatives && c.alternatives.length > 0 && (
                      <p className="mt-1 text-xs text-muted">Alternatives: {c.alternatives.join(" · ")}</p>
                    )}
                  </div>
                  <div className="shrink-0">
                    <CapabilityBadge status={c.status} />
                  </div>
                </li>
              ))}
            </ul>
          </Card>

          {inv.warnings && inv.warnings.length > 0 && (
            <Card title="Collection warnings" className="lg:col-span-3">
              <ul className="list-disc space-y-1 pl-5 font-mono text-xs text-muted">
                {inv.warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      ) : (
        s.status !== "pending" && <EmptyState title="No inventory yet">The agent enrolled but has not sent a report.</EmptyState>
      ))}

      <p className="mt-6 text-xs text-muted">
        Added {formatDate(s.created_at)}
        {s.enrolled_at && <> · enrolled {formatDate(s.enrolled_at)}</>}
        {s.last_seen_at && <> · last report {relativeTime(s.last_seen_at)}</>}
        {s.agent_version && <> · agent {s.agent_version}</>}
      </p>
    </>
  );
}
