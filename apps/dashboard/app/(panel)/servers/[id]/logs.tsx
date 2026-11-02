"use client";

import { useCallback, useEffect, useState } from "react";
import { errorMessage, runTask } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { Alert, Badge, Button, EmptyState, LoadingBlock } from "@/components/ui";

type LogEntry = { ts: string; level: string; source?: string; message: string };
type LogsResult = { backend: string; entries: LogEntry[] };

const levelTone: Record<string, "danger" | "warning" | "neutral"> = { error: "danger", warning: "warning" };

export function LogsPanel({ serverId, online, osFamily }: { serverId: string; online: boolean; osFamily: string }) {
  const windows = osFamily === "windows";
  const [source, setSource] = useState("system");
  const [service, setService] = useState("");
  const [minLevel, setMinLevel] = useState("info");
  const [lines, setLines] = useState(200);
  const [data, setData] = useState<LogsResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const params: Record<string, unknown> = { source, lines, min_level: minLevel };
      if (source === "service") params.service = service.trim();
      setData(await runTask<LogsResult>(serverId, "logs.read", params));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [serverId, source, service, minLevel, lines]);

  useEffect(() => {
    if (online && source !== "service") void load();
    // Reload automatically only for the log sources that need no input.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [online, source, minLevel, lines]);

  if (!online && !data) return <EmptyState title="Agent offline">Logs can only be read while the agent is online.</EmptyState>;

  const select = "h-9 rounded-lg border border-line bg-surface px-2 text-sm";
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Log source" className={select} value={source} onChange={(e) => setSource(e.target.value)}>
          <option value="system">{windows ? "System log" : "System journal"}</option>
          {windows ? <option value="application">Application log</option> : <option value="service">One service</option>}
        </select>
        {source === "service" && (
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void load();
            }}
          >
            <input aria-label="Service name" placeholder="e.g. nginx.service" value={service} onChange={(e) => setService(e.target.value)} className="h-9 w-56 rounded-lg border border-line bg-surface px-3 font-mono text-sm" required />
            <Button size="sm" type="submit" loading={loading}>
              Show
            </Button>
          </form>
        )}
        <select aria-label="Minimum level" className={select} value={minLevel} onChange={(e) => setMinLevel(e.target.value)}>
          <option value="error">Errors only</option>
          <option value="warning">Warnings and errors</option>
          <option value="info">Info and above</option>
          <option value="debug">Everything (debug)</option>
        </select>
        <select aria-label="Number of entries" className={select} value={lines} onChange={(e) => setLines(Number(e.target.value))}>
          {[100, 200, 500, 1000].map((n) => (
            <option key={n} value={n}>
              Last {n}
            </option>
          ))}
        </select>
        <Button size="sm" variant="secondary" loading={loading} onClick={() => void load()} disabled={source === "service" && !service.trim()}>
          Refresh
        </Button>
        {data && <span className="text-xs text-muted">{data.entries.length} entries · {data.backend}</span>}
      </div>
      {error && <Alert>{error}</Alert>}
      {loading && !data && <LoadingBlock label="Reading logs…" />}
      {data && data.entries.length === 0 && <EmptyState title="No matching entries" />}
      {data && data.entries.length > 0 && (
        <div className="max-h-[36rem] overflow-auto rounded-lg border border-line bg-surface-2 font-mono text-xs">
          {[...data.entries].reverse().map((e, i) => (
            <div key={i} className="flex gap-3 border-b border-line px-3 py-1.5 last:border-b-0">
              <span className="shrink-0 whitespace-nowrap text-muted">{formatDate(e.ts)}</span>
              <span className="w-16 shrink-0">
                <Badge tone={levelTone[e.level] ?? "neutral"}>{e.level}</Badge>
              </span>
              {e.source && <span className="w-40 shrink-0 truncate text-muted" title={e.source}>{e.source}</span>}
              <span className="min-w-0 whitespace-pre-wrap break-words">{e.message}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
