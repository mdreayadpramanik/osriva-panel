"use client";

import { useCallback, useEffect, useState } from "react";
import {
  errorMessage,
  runTask,
  type DockerContainer,
  type DockerImage,
  type DockerInfo,
  type DockerLogLine,
  type DockerNetwork,
  type DockerVolume,
} from "@/lib/api";
import { formatBytes, formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, EmptyState, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

type Section = "containers" | "images" | "volumes" | "networks";

const stateTone: Record<string, "success" | "danger" | "warning" | "neutral"> = {
  running: "success",
  restarting: "warning",
  paused: "warning",
  dead: "danger",
  exited: "neutral",
  created: "neutral",
};

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";

export function DockerPanel({ serverId, online }: { serverId: string; online: boolean }) {
  const { can } = useSession();
  const [info, setInfo] = useState<DockerInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [section, setSection] = useState<Section>("containers");

  const loadInfo = useCallback(async () => {
    setError(null);
    try {
      setInfo(await runTask<DockerInfo>(serverId, "docker.info"));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId]);

  useEffect(() => {
    if (online) void loadInfo();
  }, [online, loadInfo]);

  if (!online && !info) {
    return <EmptyState title="Agent offline">Docker can only be managed while the agent is online.</EmptyState>;
  }
  if (error) return <Alert>{error}</Alert>;
  if (!info) return <LoadingBlock label="Asking the agent about Docker…" />;
  if (!info.available) {
    return (
      <EmptyState title="Docker is not available on this server">
        {info.reason}. OSRIVA does not install Docker for you; install Docker Engine on the server, then refresh.
      </EmptyState>
    );
  }

  const canOperate = can("servers.operate");
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3 text-sm text-muted">
        <span>
          Docker {info.version} · {info.containers_running} of {info.containers} containers running · {info.images} images
          {info.storage_driver && <> · {info.storage_driver}</>}
        </span>
        <div role="tablist" aria-label="Docker sections" className="flex gap-1">
          {(["containers", "images", "volumes", "networks"] as Section[]).map((s) => (
            <Button key={s} size="sm" variant={section === s ? "secondary" : "ghost"} aria-selected={section === s} role="tab" onClick={() => setSection(s)}>
              {s.charAt(0).toUpperCase() + s.slice(1)}
            </Button>
          ))}
        </div>
      </div>
      {!canOperate && <Alert tone="neutral">You can view Docker. Changing it needs the servers.operate permission.</Alert>}
      {section === "containers" && <Containers serverId={serverId} canOperate={canOperate} />}
      {section === "images" && <Images serverId={serverId} canOperate={canOperate} />}
      {section === "volumes" && <Volumes serverId={serverId} />}
      {section === "networks" && <Networks serverId={serverId} />}
    </div>
  );
}

function useList<T>(serverId: string, type: string) {
  const [items, setItems] = useState<T[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setItems(await runTask<T[]>(serverId, type));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [serverId, type]);
  useEffect(() => {
    void load();
  }, [load]);
  return { items, loading, error, load };
}

function portsText(c: DockerContainer) {
  return c.ports
    .map((p) => (p.public_port ? `${p.ip && p.ip !== "0.0.0.0" && p.ip !== "::" ? p.ip : "*"}:${p.public_port}→${p.private_port}/${p.type}` : `${p.private_port}/${p.type}`))
    .filter((v, i, a) => a.indexOf(v) === i)
    .join(", ");
}

function Containers({ serverId, canOperate }: { serverId: string; canOperate: boolean }) {
  const { privileged } = useSession();
  const { items, loading, error, load } = useList<DockerContainer>(serverId, "docker.containers");
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [actError, setActError] = useState<string | null>(null);
  const [logsFor, setLogsFor] = useState<DockerContainer | null>(null);
  const [runOpen, setRunOpen] = useState(false);

  async function act(c: DockerContainer, action: "start" | "stop" | "restart" | "remove") {
    if (action === "stop" && !confirm(`Stop container ${c.name}?`)) return;
    if (action === "remove" && !confirm(`Remove container ${c.name}? Its named volumes are kept, but anything stored only inside the container is lost.`)) return;
    setBusy(`${c.id}:${action}`);
    setNotice(null);
    setActError(null);
    try {
      const call = () => runTask<{ state?: string }>(serverId, "docker.container.action", { id: c.id, action });
      const r = action === "stop" || action === "remove" ? await privileged(call) : await call();
      setNotice(action === "remove" ? `${c.name} removed.` : `${c.name}: ${action} done. Now ${r.state}.`);
      await load();
    } catch (e) {
      setActError(`${c.name}: ${errorMessage(e)}`);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end gap-2">
        {canOperate && (
          <Button size="sm" onClick={() => setRunOpen(true)}>
            Run container
          </Button>
        )}
        <Button size="sm" variant="secondary" loading={loading} onClick={() => void load()}>
          Refresh
        </Button>
      </div>
      {(error || actError) && <Alert>{error || actError}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {!items ? (
        loading && <LoadingBlock label="Listing containers…" />
      ) : items.length === 0 ? (
        <EmptyState title="No containers">This Docker host has no containers yet.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Container</Th>
              <Th>Image</Th>
              <Th>State</Th>
              <Th>Ports</Th>
              <Th className="text-right">Actions</Th>
            </tr>
          </thead>
          <tbody>
            {items.map((c) => (
              <tr key={c.id}>
                <Td>
                  <p className="font-mono text-xs">{c.name}</p>
                  <p className="text-[11px] text-muted">
                    {c.id.slice(0, 12)}
                    {c.compose_project && <> · compose: {c.compose_project}</>}
                  </p>
                  {c.protected && <Badge tone="warning">OSRIVA control plane</Badge>}
                </Td>
                <Td className="font-mono text-xs">{c.image}</Td>
                <Td>
                  <Badge tone={stateTone[c.state] ?? "neutral"}>{c.state}</Badge>
                  <p className="mt-0.5 text-[11px] text-muted">{c.status}</p>
                </Td>
                <Td className="font-mono text-[11px]">{portsText(c) || "—"}</Td>
                <Td className="text-right">
                  <div className="flex flex-wrap justify-end gap-1">
                    <Button size="sm" variant="ghost" onClick={() => setLogsFor(c)}>
                      Logs
                    </Button>
                    {canOperate && !c.protected && (
                      <>
                        {c.state === "running" ? (
                          <>
                            <Button size="sm" variant="secondary" loading={busy === `${c.id}:restart`} onClick={() => void act(c, "restart")}>
                              Restart
                            </Button>
                            <Button size="sm" variant="ghost" loading={busy === `${c.id}:stop`} onClick={() => void act(c, "stop")}>
                              Stop
                            </Button>
                          </>
                        ) : (
                          <>
                            <Button size="sm" variant="secondary" loading={busy === `${c.id}:start`} onClick={() => void act(c, "start")}>
                              Start
                            </Button>
                            <Button size="sm" variant="ghost" loading={busy === `${c.id}:remove`} onClick={() => void act(c, "remove")}>
                              Remove
                            </Button>
                          </>
                        )}
                      </>
                    )}
                  </div>
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {logsFor && <LogsModal serverId={serverId} container={logsFor} onClose={() => setLogsFor(null)} />}
      {runOpen && (
        <RunModal
          serverId={serverId}
          onClose={() => setRunOpen(false)}
          onDone={(msg) => {
            setRunOpen(false);
            setNotice(msg);
            void load();
          }}
        />
      )}
    </div>
  );
}

function LogsModal({ serverId, container, onClose }: { serverId: string; container: DockerContainer; onClose: () => void }) {
  const [lines, setLines] = useState<DockerLogLine[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [count, setCount] = useState(200);
  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await runTask<{ lines: DockerLogLine[] }>(serverId, "docker.container.logs", { id: container.id, lines: count });
      setLines(r.lines);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [serverId, container.id, count]);
  useEffect(() => {
    void load();
  }, [load]);
  return (
    <Modal
      open
      onClose={onClose}
      title={`Logs: ${container.name}`}
      footer={
        <>
          <select aria-label="Number of lines" value={count} onChange={(e) => setCount(Number(e.target.value))} className={inputClass}>
            {[100, 200, 500, 1000].map((n) => (
              <option key={n} value={n}>
                Last {n}
              </option>
            ))}
          </select>
          <Button variant="secondary" onClick={() => void load()}>
            Refresh
          </Button>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </>
      }
    >
      {error && <Alert>{error}</Alert>}
      {!lines && !error && <LoadingBlock label="Reading logs…" />}
      {lines && lines.length === 0 && <p className="text-sm text-muted">No log output.</p>}
      {lines && lines.length > 0 && (
        <pre className="max-h-[60vh] overflow-auto rounded-lg bg-bg p-3 font-mono text-[11px] leading-relaxed">
          {lines.map((l, i) => (
            <div key={i} className={l.stream === "stderr" ? "text-danger" : undefined}>
              {l.time && <span className="text-muted">{l.time.slice(0, 19).replace("T", " ")} </span>}
              {l.text}
            </div>
          ))}
        </pre>
      )}
    </Modal>
  );
}

type PortRow = { host: string; container: string; protocol: "tcp" | "udp"; public: boolean };
type VolRow = { volume: string; target: string };

function RunModal({ serverId, onClose, onDone }: { serverId: string; onClose: () => void; onDone: (msg: string) => void }) {
  const { privileged } = useSession();
  const [image, setImage] = useState("");
  const [name, setName] = useState("");
  const [restart, setRestart] = useState("unless-stopped");
  const [env, setEnv] = useState("");
  const [ports, setPorts] = useState<PortRow[]>([]);
  const [vols, setVols] = useState<VolRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    setError(null);
    const envObj: Record<string, string> = {};
    for (const line of env.split("\n").map((l) => l.trim()).filter(Boolean)) {
      const i = line.indexOf("=");
      if (i < 1) return setError(`Environment line "${line}" must look like KEY=value.`);
      envObj[line.slice(0, i)] = line.slice(i + 1);
    }
    const params: Record<string, unknown> = { image: image.trim(), restart };
    if (name.trim()) params.name = name.trim();
    if (Object.keys(envObj).length) params.env = envObj;
    if (ports.length) params.ports = ports.map((p) => ({ host_port: Number(p.host), container_port: Number(p.container), protocol: p.protocol, public: p.public }));
    if (vols.length) params.volumes = vols.map((v) => ({ volume: v.volume.trim(), target: v.target.trim() }));
    setBusy(true);
    try {
      // Image downloads can take minutes: wait up to 5 minutes for the agent.
      const r = await privileged(() => runTask<{ id: string; name: string; state: string }>(serverId, "docker.container.run", params, 15));
      onDone(`${r.name || r.id.slice(0, 12)} started (${r.state}).`);
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
      title="Run a container"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={busy} disabled={!image.trim()} onClick={() => void submit()}>
            Pull and run
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <label className="flex flex-col gap-1">
          Image
          <input className={inputClass} placeholder="nginx:1.27" value={image} onChange={(e) => setImage(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1">
          Name (optional)
          <input className={inputClass} placeholder="web" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1">
          Restart policy
          <select className={inputClass} value={restart} onChange={(e) => setRestart(e.target.value)}>
            {["unless-stopped", "always", "on-failure", "no"].map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
        </label>
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between">
            <span>Ports</span>
            <Button size="sm" variant="ghost" onClick={() => setPorts([...ports, { host: "", container: "", protocol: "tcp", public: false }])}>
              Add port
            </Button>
          </div>
          {ports.map((p, i) => (
            <div key={i} className="flex flex-wrap items-center gap-2">
              <input aria-label="Host port" className={inputClass + " w-24"} placeholder="host" value={p.host} onChange={(e) => setPorts(ports.map((x, j) => (j === i ? { ...x, host: e.target.value } : x)))} />
              →
              <input aria-label="Container port" className={inputClass + " w-24"} placeholder="container" value={p.container} onChange={(e) => setPorts(ports.map((x, j) => (j === i ? { ...x, container: e.target.value } : x)))} />
              <select aria-label="Protocol" className={inputClass} value={p.protocol} onChange={(e) => setPorts(ports.map((x, j) => (j === i ? { ...x, protocol: e.target.value as "tcp" | "udp" } : x)))}>
                <option>tcp</option>
                <option>udp</option>
              </select>
              <label className="flex items-center gap-1 text-xs">
                <input type="checkbox" checked={p.public} onChange={(e) => setPorts(ports.map((x, j) => (j === i ? { ...x, public: e.target.checked } : x)))} /> Public
              </label>
              <Button size="sm" variant="ghost" onClick={() => setPorts(ports.filter((_, j) => j !== i))}>
                ✕
              </Button>
            </div>
          ))}
          <p className="text-xs text-muted">Ports are bound to 127.0.0.1 unless you tick Public. Public ports may be reachable from the internet, depending on your firewall.</p>
        </div>
        <div className="flex flex-col gap-1">
          <div className="flex items-center justify-between">
            <span>Named volumes</span>
            <Button size="sm" variant="ghost" onClick={() => setVols([...vols, { volume: "", target: "" }])}>
              Add volume
            </Button>
          </div>
          {vols.map((v, i) => (
            <div key={i} className="flex items-center gap-2">
              <input aria-label="Volume name" className={inputClass + " w-36"} placeholder="app-data" value={v.volume} onChange={(e) => setVols(vols.map((x, j) => (j === i ? { ...x, volume: e.target.value } : x)))} />
              →
              <input aria-label="Path in container" className={inputClass + " flex-1"} placeholder="/data" value={v.target} onChange={(e) => setVols(vols.map((x, j) => (j === i ? { ...x, target: e.target.value } : x)))} />
              <Button size="sm" variant="ghost" onClick={() => setVols(vols.filter((_, j) => j !== i))}>
                ✕
              </Button>
            </div>
          ))}
          <p className="text-xs text-muted">Host folders, privileged mode and host networking are not offered: they give a container control over the server.</p>
        </div>
        <label className="flex flex-col gap-1">
          Environment (KEY=value, one per line)
          <textarea className="min-h-20 rounded-lg border border-line bg-surface p-2 font-mono text-xs" value={env} onChange={(e) => setEnv(e.target.value)} />
          <span className="text-xs text-muted">Values are sent to the agent but are not shown in the activity list or audit log.</span>
        </label>
        {error && <Alert>{error}</Alert>}
      </div>
    </Modal>
  );
}

function Images({ serverId, canOperate }: { serverId: string; canOperate: boolean }) {
  const { privileged } = useSession();
  const { items, loading, error, load } = useList<DockerImage>(serverId, "docker.images");
  const [ref, setRef] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [actError, setActError] = useState<string | null>(null);

  async function wrap(key: string, fn: () => Promise<string>) {
    setBusy(key);
    setNotice(null);
    setActError(null);
    try {
      setNotice(await fn());
      await load();
    } catch (e) {
      setActError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  const pull = () =>
    wrap("pull", async () => {
      const r = await runTask<{ status: string }>(serverId, "docker.image.pull", { image: ref.trim() }, 15);
      setRef("");
      return r.status || "Pulled.";
    });
  const remove = (img: DockerImage) => {
    const name = img.tags[0] ?? img.id;
    if (!confirm(`Remove image ${name}?`)) return;
    void wrap(img.id, async () => {
      await privileged(() => runTask(serverId, "docker.image.remove", { image: img.tags[0] ?? img.id.replace(/^sha256:/, "") }));
      return `${name} removed.`;
    });
  };
  const prune = () => {
    if (!confirm("Remove all dangling images (untagged and unused by any container)?")) return;
    void wrap("prune", async () => {
      const r = await privileged(() => runTask<{ deleted: number; space_reclaimed: number }>(serverId, "docker.images.prune"));
      return `${r.deleted} images removed, ${formatBytes(r.space_reclaimed)} reclaimed.`;
    });
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        {canOperate ? (
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void pull();
            }}
          >
            <input aria-label="Image to pull" className={inputClass + " w-64"} placeholder="redis:7-alpine" value={ref} onChange={(e) => setRef(e.target.value)} />
            <Button size="sm" type="submit" loading={busy === "pull"} disabled={!ref.trim()}>
              Pull
            </Button>
          </form>
        ) : (
          <span />
        )}
        <div className="flex gap-2">
          {canOperate && (
            <Button size="sm" variant="ghost" loading={busy === "prune"} onClick={prune}>
              Remove dangling
            </Button>
          )}
          <Button size="sm" variant="secondary" loading={loading} onClick={() => void load()}>
            Refresh
          </Button>
        </div>
      </div>
      {(error || actError) && <Alert>{error || actError}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {!items ? (
        loading && <LoadingBlock label="Listing images…" />
      ) : items.length === 0 ? (
        <EmptyState title="No images" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Image</Th>
              <Th>Size</Th>
              <Th>Created</Th>
              {canOperate && <Th className="text-right">Actions</Th>}
            </tr>
          </thead>
          <tbody>
            {items.map((img) => (
              <tr key={img.id}>
                <Td>
                  <p className="font-mono text-xs">{img.tags.length ? img.tags.join(", ") : "<untagged>"}</p>
                  <p className="text-[11px] text-muted">
                    {img.id.replace(/^sha256:/, "").slice(0, 12)}
                    {img.in_use && <> · in use</>}
                  </p>
                </Td>
                <Td>{formatBytes(img.size)}</Td>
                <Td className="text-muted">{formatDate(img.created)}</Td>
                {canOperate && (
                  <Td className="text-right">
                    <Button size="sm" variant="ghost" disabled={img.in_use} loading={busy === img.id} onClick={() => remove(img)}>
                      Remove
                    </Button>
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

function Volumes({ serverId }: { serverId: string }) {
  const { items, loading, error, load } = useList<DockerVolume>(serverId, "docker.volumes");
  return (
    <SimpleList title="volumes" loading={loading} error={error} load={load} empty={items?.length === 0}>
      {items && (
        <Table>
          <thead>
            <tr>
              <Th>Volume</Th>
              <Th>Driver</Th>
              <Th>Mount point</Th>
            </tr>
          </thead>
          <tbody>
            {items.map((v) => (
              <tr key={v.name}>
                <Td className="font-mono text-xs">{v.name}</Td>
                <Td>{v.driver}</Td>
                <Td className="font-mono text-[11px] text-muted">{v.mountpoint}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </SimpleList>
  );
}

function Networks({ serverId }: { serverId: string }) {
  const { items, loading, error, load } = useList<DockerNetwork>(serverId, "docker.networks");
  return (
    <SimpleList title="networks" loading={loading} error={error} load={load} empty={items?.length === 0}>
      {items && (
        <Table>
          <thead>
            <tr>
              <Th>Network</Th>
              <Th>Driver</Th>
              <Th>Scope</Th>
            </tr>
          </thead>
          <tbody>
            {items.map((n) => (
              <tr key={n.id}>
                <Td>
                  <p className="font-mono text-xs">{n.name}</p>
                  <p className="text-[11px] text-muted">{n.id.slice(0, 12)}</p>
                </Td>
                <Td>{n.driver}</Td>
                <Td>{n.scope}</Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </SimpleList>
  );
}

function SimpleList({ title, loading, error, load, empty, children }: { title: string; loading: boolean; error: string | null; load: () => Promise<void>; empty: boolean; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <Button size="sm" variant="secondary" loading={loading} onClick={() => void load()}>
          Refresh
        </Button>
      </div>
      {error && <Alert>{error}</Alert>}
      {empty ? <EmptyState title={`No ${title}`} /> : children}
    </div>
  );
}
