"use client";

import { useEffect, useState } from "react";
import { api, errorMessage, type MetricSample } from "@/lib/api";
import { formatBytes, formatRate, relativeTime } from "@/lib/format";
import { Alert, Card, EmptyState, LoadingBlock } from "@/components/ui";

type Point = { t: number; v: number };

// Minimal line chart: real samples only, gaps where a value is missing.
function Sparkline({ points, max, label, gapMs = 45_000 }: { points: Point[]; max?: number; label: string; gapMs?: number }) {
  const w = 600;
  const h = 120;
  if (points.length < 2) {
    return <div className="grid h-[120px] place-items-center text-xs text-muted">Collecting data…</div>;
  }
  const t0 = points[0]!.t;
  const t1 = points[points.length - 1]!.t;
  const top = max ?? Math.max(...points.map((p) => p.v), 1);
  const x = (t: number) => (t1 === t0 ? 0 : ((t - t0) / (t1 - t0)) * w);
  const y = (v: number) => h - (Math.min(v, top) / top) * (h - 6) - 3;
  // Break the line where consecutive samples are more than 45 s apart (agent offline).
  let d = "";
  points.forEach((p, i) => {
    const gap = i > 0 && p.t - points[i - 1]!.t > gapMs;
    d += `${i === 0 || gap ? "M" : "L"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)} `;
  });
  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className="h-[120px] w-full" role="img" aria-label={label}>
      <line x1="0" x2={w} y1={h - 3} y2={h - 3} stroke="var(--line)" strokeWidth="1" />
      <path d={d} fill="none" stroke="var(--accent)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

function series(samples: MetricSample[], pick: (s: MetricSample) => number | undefined): Point[] {
  const out: Point[] = [];
  for (const s of samples) {
    const v = pick(s);
    if (v != null && Number.isFinite(v)) out.push({ t: new Date(s.ts).getTime(), v });
  }
  return out;
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-lg border border-line p-4">
      <p className="text-xs text-muted">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value}</p>
      {sub && <p className="mt-0.5 text-xs text-muted">{sub}</p>}
    </div>
  );
}

const windows = [
  { minutes: 15, label: "15 min" },
  { minutes: 60, label: "1 hour" },
  { minutes: 360, label: "6 hours" },
  { minutes: 1440, label: "24 hours" },
  { minutes: 10080, label: "7 days" },
  { minutes: 43200, label: "30 days" },
];

export function MetricsPanel({ serverId }: { serverId: string }) {
  const [minutes, setMinutes] = useState(60);
  const [samples, setSamples] = useState<MetricSample[] | null>(null);
  const [hourly, setHourly] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () =>
      api<{ samples: MetricSample[]; resolution?: string }>(`/servers/${serverId}/metrics?minutes=${minutes}`)
        .then((r) => alive && (setSamples(r.samples), setHourly(r.resolution === "hour"), setError(null)))
        .catch((e) => alive && setError(errorMessage(e)));
    void load();
    const t = setInterval(load, 10_000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [serverId, minutes]);

  if (error) return <Alert>{error}</Alert>;
  if (!samples) return <LoadingBlock />;
  if (samples.length === 0 && minutes > 1440) {
    return <EmptyState title="No hourly data yet">Hourly averages are written after each complete hour of samples and kept for 90 days.</EmptyState>;
  }
  if (samples.length === 0) {
    return (
      <EmptyState title="No metrics yet">
        The agent sends samples every 30 seconds. Agents older than version 0.3 do not send metrics: reinstall the agent with the one-line installer.
      </EmptyState>
    );
  }

  const last = samples[samples.length - 1]!;
  const memPct = (s: MetricSample) => (s.mem_used_bytes != null && s.mem_total_bytes ? (s.mem_used_bytes / s.mem_total_bytes) * 100 : undefined);
  const cpu = series(samples, (s) => s.cpu_percent);
  const mem = series(samples, memPct);
  const rx = series(samples, (s) => s.net_rx_bps);
  const tx = series(samples, (s) => s.net_tx_bps);
  const lastCpu = [...samples].reverse().find((s) => s.cpu_percent != null)?.cpu_percent;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">
          {hourly ? <>Hourly averages (kept 90 days) · {samples.length} hours</> : <>Last sample {relativeTime(last.ts)} · {samples.length} samples · refreshes every 10 s</>}
        </p>
        <div role="radiogroup" aria-label="Time window" className="flex rounded-lg border border-line p-0.5 text-xs">
          {windows.map((w) => (
            <button
              key={w.minutes}
              role="radio"
              aria-checked={minutes === w.minutes}
              onClick={() => setMinutes(w.minutes)}
              className={"rounded-md px-2.5 py-1 " + (minutes === w.minutes ? "bg-surface-2 font-medium" : "text-muted hover:text-fg")}
            >
              {w.label}
            </button>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label="CPU" value={lastCpu != null ? `${lastCpu.toFixed(1)} %` : "—"} sub={last.load1 != null ? `load ${last.load1.toFixed(2)} / ${last.load5?.toFixed(2)} / ${last.load15?.toFixed(2)}` : undefined} />
        <Stat
          label="Memory"
          value={memPct(last) != null ? `${memPct(last)!.toFixed(1)} %` : "—"}
          sub={last.mem_total_bytes ? `${formatBytes(last.mem_used_bytes)} of ${formatBytes(last.mem_total_bytes)}` : undefined}
        />
        <Stat label="Network in" value={formatRate(last.net_rx_bps)} />
        <Stat label="Network out" value={formatRate(last.net_tx_bps)} />
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card title="CPU %">{cpu.length ? <Sparkline gapMs={hourly ? 2 * 3600_000 : 45_000} points={cpu} max={100} label="CPU usage over time" /> : <p className="text-sm text-muted">Not reported by this platform.</p>}</Card>
        <Card title="Memory %">{mem.length ? <Sparkline gapMs={hourly ? 2 * 3600_000 : 45_000} points={mem} max={100} label="Memory usage over time" /> : <p className="text-sm text-muted">Not reported by this platform.</p>}</Card>
        <Card title="Network in">{rx.length ? <Sparkline gapMs={hourly ? 2 * 3600_000 : 45_000} points={rx} label="Inbound network throughput" /> : <p className="text-sm text-muted">Not reported by this platform.</p>}</Card>
        <Card title="Network out">{tx.length ? <Sparkline gapMs={hourly ? 2 * 3600_000 : 45_000} points={tx} label="Outbound network throughput" /> : <p className="text-sm text-muted">Not reported by this platform.</p>}</Card>
      </div>

      {last.disks && last.disks.length > 0 && (
        <Card title="Disk usage (latest sample)">
          <ul className="flex flex-col gap-3 text-sm">
            {last.disks.map((d) => {
              const pct = d.total_bytes ? Math.round((d.used_bytes / d.total_bytes) * 100) : 0;
              return (
                <li key={d.mount}>
                  <div className="flex justify-between">
                    <span className="font-mono text-xs">{d.mount}</span>
                    <span className="text-muted">
                      {formatBytes(d.used_bytes)} of {formatBytes(d.total_bytes)} ({pct} %)
                    </span>
                  </div>
                  <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-surface-2">
                    <div className={pct >= 90 ? "h-full bg-danger" : pct >= 75 ? "h-full bg-warning" : "h-full bg-accent"} style={{ width: `${pct}%` }} />
                  </div>
                </li>
              );
            })}
          </ul>
        </Card>
      )}
    </div>
  );
}
