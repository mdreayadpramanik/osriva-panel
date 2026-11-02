"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { errorMessage, runTask, type ServiceItem, type ServicesResult } from "@/lib/api";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, EmptyState, LoadingBlock, Table, Td, Th } from "@/components/ui";

const stateTone: Record<string, "success" | "danger" | "warning" | "neutral"> = {
  running: "success",
  failed: "danger",
  starting: "warning",
  stopping: "warning",
  stopped: "neutral",
};

type Action = "start" | "stop" | "restart" | "enable" | "disable";

export function ServicesPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const { can, privileged } = useSession();
  const [data, setData] = useState<ServicesResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [onlyRunning, setOnlyRunning] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setData(await runTask<ServicesResult>(serverId, "services.list"));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [serverId]);

  useEffect(() => {
    if (online) void load();
  }, [online, load]);

  async function act(s: ServiceItem, action: Action) {
    if ((action === "stop" || action === "disable") && !confirm(`${action === "stop" ? "Stop" : "Disable"} ${s.name}? If this is the SSH or network service, you may lose access to the server.`)) return;
    setBusy(`${s.name}:${action}`);
    setError(null);
    setNotice(null);
    try {
      const r = await privileged(() => runTask<{ state: string; startup: string }>(serverId, "service.action", { name: s.name, action }));
      setNotice(`${s.name}: ${action} done. Now ${r.state}, startup ${r.startup}.`);
      setData((d) => d && { ...d, services: d.services.map((x) => (x.name === s.name ? { ...x, state: r.state, startup: r.startup } : x)) });
    } catch (e) {
      setError(`${s.name}: ${errorMessage(e)}`);
    } finally {
      setBusy(null);
    }
  }

  const rows = useMemo(() => {
    const q = query.toLowerCase();
    return (data?.services ?? []).filter(
      (s) => (!onlyRunning || s.state === "running") && (!q || s.name.toLowerCase().includes(q) || (s.description ?? "").toLowerCase().includes(q)),
    );
  }, [data, query, onlyRunning]);

  if (!online && !data) {
    return <EmptyState title="Agent offline">Services can only be listed while the agent is online.</EmptyState>;
  }

  const canOperate = can("servers.operate");

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <input aria-label="Filter services" placeholder="Filter by name or description" value={query} onChange={(e) => setQuery(e.target.value)} className="h-9 w-64 rounded-lg border border-line bg-surface px-3 text-sm" />
          <label className="flex items-center gap-2 text-sm text-muted">
            <input type="checkbox" checked={onlyRunning} onChange={(e) => setOnlyRunning(e.target.checked)} /> Running only
          </label>
        </div>
        <div className="flex items-center gap-3 text-xs text-muted">
          {data && (
            <span>
              {rows.length} of {data.services.length} · {data.manager}
            </span>
          )}
          <Button size="sm" variant="secondary" loading={loading} onClick={() => void load()}>
            Refresh
          </Button>
        </div>
      </div>
      {!canOperate && <Alert tone="neutral">You can view services. Controlling them needs the servers.operate permission.</Alert>}
      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {!data ? (
        loading ? <LoadingBlock label="Asking the agent…" /> : null
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Service</Th>
              <Th>State</Th>
              <Th>Startup</Th>
              {canOperate && <Th className="text-right">Actions</Th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={s.name}>
                <Td>
                  <p className="font-mono text-xs">{s.name}</p>
                  {s.description && <p className="text-xs text-muted">{s.description}</p>}
                </Td>
                <Td>
                  <Badge tone={stateTone[s.state] ?? "neutral"}>{s.state}</Badge>
                  {s.detail && <p className="mt-0.5 text-[11px] text-muted">{s.detail}</p>}
                </Td>
                <Td className="text-muted">{s.startup}</Td>
                {canOperate && (
                  <Td className="text-right">
                    <div className="flex flex-wrap justify-end gap-1">
                      {s.state === "running" ? (
                        <>
                          <Button size="sm" variant="secondary" loading={busy === `${s.name}:restart`} onClick={() => void act(s, "restart")}>
                            Restart
                          </Button>
                          <Button size="sm" variant="ghost" loading={busy === `${s.name}:stop`} onClick={() => void act(s, "stop")}>
                            Stop
                          </Button>
                        </>
                      ) : (
                        <Button size="sm" variant="secondary" loading={busy === `${s.name}:start`} onClick={() => void act(s, "start")}>
                          Start
                        </Button>
                      )}
                      {s.startup === "enabled" ? (
                        <Button size="sm" variant="ghost" loading={busy === `${s.name}:disable`} onClick={() => void act(s, "disable")}>
                          Disable
                        </Button>
                      ) : s.startup === "disabled" || s.startup === "manual" ? (
                        <Button size="sm" variant="ghost" loading={busy === `${s.name}:enable`} onClick={() => void act(s, "enable")}>
                          Enable
                        </Button>
                      ) : null}
                    </div>
                  </Td>
                )}
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}
