"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, errorMessage, runTask, type ProcessesResult, type Task } from "@/lib/api";
import { formatBytes, formatDate, relativeTime } from "@/lib/format";
import { Alert, Badge, Button, EmptyState, LoadingBlock, Table, Td, Th } from "@/components/ui";

export function ProcessesPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const [data, setData] = useState<ProcessesResult | null>(null);
  const [fetchedAt, setFetchedAt] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setData(await runTask<ProcessesResult>(serverId, "processes.list"));
      setFetchedAt(new Date().toISOString());
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [serverId]);

  useEffect(() => {
    if (online) void load();
  }, [online, load]);

  const rows = useMemo(() => {
    const q = query.toLowerCase();
    return (data?.processes ?? []).filter((p) => !q || p.name.toLowerCase().includes(q) || (p.command ?? "").toLowerCase().includes(q) || String(p.pid) === q);
  }, [data, query]);

  if (!online && !data) return <EmptyState title="Agent offline">Processes can only be listed while the agent is online.</EmptyState>;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <input aria-label="Filter processes" placeholder="Filter by name, command or PID" value={query} onChange={(e) => setQuery(e.target.value)} className="h-9 w-64 rounded-lg border border-line bg-surface px-3 text-sm" />
        <div className="flex items-center gap-3 text-xs text-muted">
          {data && (
            <span>
              {rows.length} of {data.processes.length}
              {data.truncated && " (list truncated)"} · snapshot {fetchedAt && relativeTime(fetchedAt)}
            </span>
          )}
          <Button size="sm" variant="secondary" loading={loading} onClick={() => void load()}>
            Refresh
          </Button>
        </div>
      </div>
      {error && <Alert>{error}</Alert>}
      {!data ? (
        loading ? <LoadingBlock label="Asking the agent…" /> : null
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>PID</Th>
              <Th>Name</Th>
              <Th>User</Th>
              <Th>Memory (RSS)</Th>
              <Th>Threads</Th>
              <Th>Command</Th>
            </tr>
          </thead>
          <tbody>
            {rows.slice(0, 500).map((p) => (
              <tr key={p.pid}>
                <Td className="font-mono text-xs">{p.pid}</Td>
                <Td className="font-medium">{p.name}</Td>
                <Td className="text-muted">{p.user ?? "—"}</Td>
                <Td className="tabular-nums">{p.rss_bytes ? formatBytes(p.rss_bytes) : "—"}</Td>
                <Td className="tabular-nums text-muted">{p.threads ?? "—"}</Td>
                <Td className="max-w-md">
                  <code className="block truncate font-mono text-[11px] text-muted" title={p.command}>
                    {p.command || "—"}
                  </code>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {rows.length > 500 && <p className="text-xs text-muted">Showing the 500 largest by memory. Use the filter to narrow down.</p>}
    </div>
  );
}

const statusTone: Record<string, "success" | "danger" | "warning" | "neutral"> = {
  succeeded: "success",
  failed: "danger",
  expired: "warning",
  running: "warning",
  queued: "neutral",
};

export function ActivityPanel({ serverId }: { serverId: string }) {
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const load = () =>
      api<{ tasks: Task[] }>(`/servers/${serverId}/tasks?limit=50`)
        .then((r) => setTasks(r.tasks))
        .catch((e) => setError(errorMessage(e)));
    void load();
    const t = setInterval(load, 10_000);
    return () => clearInterval(t);
  }, [serverId]);
  if (error) return <Alert>{error}</Alert>;
  if (!tasks) return <LoadingBlock />;
  if (tasks.length === 0) return <EmptyState title="No tasks yet">Tasks you run on this server (listing services, restarting one…) appear here.</EmptyState>;
  return (
    <Table>
      <thead>
        <tr>
          <Th>Time</Th>
          <Th>Task</Th>
          <Th>Parameters</Th>
          <Th>Status</Th>
          <Th>Error</Th>
        </tr>
      </thead>
      <tbody>
        {tasks.map((t) => (
          <tr key={t.id}>
            <Td className="whitespace-nowrap text-muted">{formatDate(t.created_at)}</Td>
            <Td>
              <code className="font-mono text-xs">{t.type}</code>
            </Td>
            <Td>
              <code className="font-mono text-[11px] text-muted">{Object.keys(t.params ?? {}).length ? JSON.stringify(t.params) : "—"}</code>
            </Td>
            <Td>
              <Badge tone={statusTone[t.status] ?? "neutral"}>{t.status}</Badge>
            </Td>
            <Td className="max-w-xs truncate text-xs text-danger" >
              <span title={t.error}>{t.error}</span>
            </Td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
