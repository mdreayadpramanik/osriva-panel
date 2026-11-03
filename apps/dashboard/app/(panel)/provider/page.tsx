"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, runTask, type Datacenter, type HVNode, type IPPool, type Rack, type Server, type VMHostInfo, type VMTemplate } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, Modal, PageHeader, Table, Td, Th } from "@/components/ui";
import { ConnectionBadge } from "../servers/connection";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";

type Field = { key: string; label: string; placeholder?: string; type?: "number" | "text"; options?: { value: string; label: string }[]; wide?: boolean };

/** A small create form: fields become a JSON body; empty optional fields are left out. */
function CreateModal({ title, fields, initial, onClose, onSubmit }: {
  title: string;
  fields: Field[];
  initial?: Record<string, string>;
  onClose: () => void;
  onSubmit: (body: Record<string, unknown>) => Promise<void>;
}) {
  const [v, setV] = useState<Record<string, string>>(initial ?? {});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit() {
    setBusy(true);
    setError(null);
    const body: Record<string, unknown> = {};
    for (const f of fields) {
      const raw = (v[f.key] ?? "").trim();
      if (raw === "") continue;
      body[f.key] = f.type === "number" ? Number(raw) : f.key === "dns" ? raw.split(/[\s,]+/).filter(Boolean) : raw;
    }
    try {
      await onSubmit(body);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title={title} footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button loading={busy} onClick={() => void submit()}>Save</Button></>}>
      <div className="flex flex-col gap-3 text-sm">
        {error && <Alert>{error}</Alert>}
        {fields.map((f) => (
          <label key={f.key} className="flex flex-col gap-1">
            {f.label}
            {f.options ? (
              <select className={inputClass} value={v[f.key] ?? ""} onChange={(e) => setV({ ...v, [f.key]: e.target.value })}>
                {f.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            ) : (
              <input className={inputClass + (f.wide ? " w-full font-mono text-xs" : "")} type={f.type ?? "text"} placeholder={f.placeholder}
                value={v[f.key] ?? ""} onChange={(e) => setV({ ...v, [f.key]: e.target.value })} />
            )}
          </label>
        ))}
      </div>
    </Modal>
  );
}

type Creating = "datacenter" | "rack" | "node" | "pool" | "template" | null;

export default function InfrastructurePage() {
  const { can, me } = useSession();
  const [dcs, setDcs] = useState<Datacenter[] | null>(null);
  const [racks, setRacks] = useState<Rack[]>([]);
  const [nodes, setNodes] = useState<HVNode[]>([]);
  const [pools, setPools] = useState<IPPool[]>([]);
  const [templates, setTemplates] = useState<VMTemplate[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [creating, setCreating] = useState<Creating>(null);
  const [host, setHost] = useState<{ node: HVNode; info: VMHostInfo | null; error?: string } | null>(null);

  const load = useCallback(async () => {
    try {
      const [d, n, p, t, s] = await Promise.all([
        api<{ datacenters: Datacenter[] }>("/provider/datacenters"),
        api<{ nodes: HVNode[] }>("/provider/nodes"),
        api<{ pools: IPPool[] }>("/provider/ip-pools"),
        api<{ templates: VMTemplate[] }>("/provider/templates"),
        api<{ servers: Server[] }>("/servers").catch(() => ({ servers: [] as Server[] })),
      ]);
      const r = await Promise.all(d.datacenters.map((x) => api<{ racks: Rack[] }>(`/provider/datacenters/${x.id}/racks`)));
      setDcs(d.datacenters);
      setRacks(r.flatMap((x) => x.racks));
      setNodes(n.nodes);
      setPools(p.pools);
      setTemplates(t.templates);
      setServers(s.servers);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);
  useEffect(() => void load(), [load]);

  const writable = can("provider.write") && me.panel_mode === "provider";
  const dcName = (id: string | null) => dcs?.find((d) => d.id === id)?.name ?? "—";

  async function remove(path: string, what: string) {
    if (!confirm(`Delete ${what}?`)) return;
    setError(null);
    try {
      await api(path, { method: "DELETE" });
      setNotice(`${what} deleted.`);
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }

  async function checkHost(n: HVNode) {
    setHost({ node: n, info: null });
    try {
      setHost({ node: n, info: await runTask<VMHostInfo>(n.server_id, "vm.host") });
    } catch (e) {
      setHost({ node: n, info: null, error: errorMessage(e) });
    }
  }

  const created = async (msg: string) => {
    setCreating(null);
    setNotice(msg);
    await load();
  };
  const dcOptions = (dcs ?? []).map((d) => ({ value: d.id, label: d.name }));
  const del = (onClick: () => void) => writable && <Button size="sm" variant="ghost" onClick={onClick}>Delete</Button>;

  return (
    <>
      <PageHeader
        title="Infrastructure"
        description="Data centers, racks, libvirt/KVM nodes, IPv4 pools and VM templates for Hosting Provider Mode. Nodes are enrolled servers; OSRIVA never changes their networking."
      />
      {me.panel_mode !== "provider" && <div className="mb-4"><Alert tone="warning">The panel is in Personal Server Mode. Switch to Hosting Provider Mode in Settings to make changes.</Alert></div>}
      {error && <div className="mb-4"><Alert>{error}</Alert></div>}
      {notice && <div className="mb-4"><Alert tone="success">{notice}</Alert></div>}
      {dcs === null ? (
        !error && <LoadingBlock />
      ) : (
        <div className="flex flex-col gap-6">
          <Card title="Data centers and racks" actions={writable && <div className="flex gap-2">
            <Button size="sm" onClick={() => setCreating("datacenter")}>Add data center</Button>
            {dcs.length > 0 && <Button size="sm" variant="ghost" onClick={() => setCreating("rack")}>Add rack</Button>}
          </div>}>
            {dcs.length === 0 ? <EmptyState title="No data centers yet">Start with the location your nodes are in.</EmptyState> : (
              <Table>
                <thead><tr><Th>Data center</Th><Th>Racks</Th><Th /></tr></thead>
                <tbody>
                  {dcs.map((d) => (
                    <tr key={d.id}>
                      <Td><p className="font-medium">{d.name}</p><p className="text-xs text-muted">{d.location}</p></Td>
                      <Td className="text-xs">
                        {racks.filter((r) => r.datacenter_id === d.id).map((r) => (
                          <span key={r.id} className="mr-3 inline-flex items-center gap-1">{r.name} ({r.units}U){writable && <button type="button" className="text-danger" aria-label={`Delete rack ${r.name}`} onClick={() => void remove(`/provider/racks/${r.id}`, `rack ${r.name}`)}>×</button>}</span>
                        ))}
                      </Td>
                      <Td className="text-right">{del(() => void remove(`/provider/datacenters/${d.id}`, `data center ${d.name}`))}</Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card title="Nodes" description="Capacity is the node's reported hardware times its overcommit ratio." actions={writable && dcs.length > 0 && <Button size="sm" onClick={() => setCreating("node")}>Add node</Button>}>
            {nodes.length === 0 ? <p className="text-sm text-muted">No nodes yet. A node is an enrolled Linux server with libvirt, qemu-kvm and genisoimage installed.</p> : (
              <Table>
                <thead><tr><Th>Node</Th><Th>Location</Th><Th>vCPUs</Th><Th>Memory</Th><Th>VPS</Th><Th /></tr></thead>
                <tbody>
                  {nodes.map((n) => (
                    <tr key={n.id}>
                      <Td><p className="font-medium">{n.server_name}</p><p className="text-xs text-muted">libvirt · network {n.network}</p>{n.connection && <ConnectionBadge connection={n.connection} />}</Td>
                      <Td className="text-xs">{dcName(n.datacenter_id)}{n.rack_id && ` · ${racks.find((r) => r.id === n.rack_id)?.name ?? ""}`}</Td>
                      <Td className="text-xs">{n.used_vcpus} / {n.vcpus} <span className="text-muted">({n.cpu_overcommit}:1)</span></Td>
                      <Td className="text-xs">{n.used_memory_mb} / {n.memory_mb} MB</Td>
                      <Td className="text-xs">{n.vps_count}</Td>
                      <Td className="whitespace-nowrap text-right">
                        <Button size="sm" variant="ghost" onClick={() => void checkHost(n)}>Check host</Button>
                        {del(() => void remove(`/provider/nodes/${n.id}`, `node ${n.server_name}`))}
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card title="IP pools" actions={writable && <Button size="sm" onClick={() => setCreating("pool")}>Add pool</Button>}>
            {pools.length === 0 ? <p className="text-sm text-muted">No pools yet. Each VPS gets the lowest free address of its pool as a static address.</p> : (
              <Table>
                <thead><tr><Th>Pool</Th><Th>Network</Th><Th>Range</Th><Th>Used</Th><Th /></tr></thead>
                <tbody>
                  {pools.map((p) => (
                    <tr key={p.id}>
                      <Td><p className="font-medium">{p.name}</p><p className="text-xs text-muted">{dcName(p.datacenter_id)}</p></Td>
                      <Td className="font-mono text-xs">{p.cidr} via {p.gateway}{p.dns.length > 0 && <span className="text-muted"> · DNS {p.dns.join(", ")}</span>}</Td>
                      <Td className="font-mono text-xs">{p.first} – {p.last}</Td>
                      <Td className="text-xs">{p.allocated.length} / {p.size}</Td>
                      <Td className="text-right">{del(() => void remove(`/provider/ip-pools/${p.id}`, `pool ${p.name}`))}</Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card title="Templates" description="Cloud images (qcow2 or raw) with NoCloud cloud-init. Every download is checked against the SHA-256 before use." actions={writable && <Button size="sm" onClick={() => setCreating("template")}>Add template</Button>}>
            {templates.length === 0 ? <p className="text-sm text-muted">No templates yet.</p> : (
              <Table>
                <thead><tr><Th>Template</Th><Th>Image</Th><Th>Min. disk</Th><Th /></tr></thead>
                <tbody>
                  {templates.map((t) => (
                    <tr key={t.id}>
                      <Td><p className="font-medium">{t.name}</p><p className="text-xs text-muted">user {t.default_user}</p></Td>
                      <Td className="max-w-md text-xs"><p className="truncate">{t.image_url}</p><p className="truncate font-mono text-[11px] text-muted">sha256 {t.image_sha256}</p></Td>
                      <Td className="text-xs">{t.min_disk_gb} GB</Td>
                      <Td className="text-right">{del(() => void remove(`/provider/templates/${t.id}`, `template ${t.name}`))}</Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </div>
      )}

      {creating === "datacenter" && (
        <CreateModal title="Add data center" onClose={() => setCreating(null)}
          fields={[{ key: "name", label: "Name", placeholder: "fra1" }, { key: "location", label: "Location", placeholder: "Frankfurt, DE" }]}
          onSubmit={async (b) => { await api("/provider/datacenters", { method: "POST", body: b }); await created("Data center added."); }} />
      )}
      {creating === "rack" && (
        <CreateModal title="Add rack" onClose={() => setCreating(null)} initial={{ datacenter_id: dcOptions[0]?.value ?? "", units: "42" }}
          fields={[{ key: "datacenter_id", label: "Data center", options: dcOptions }, { key: "name", label: "Name", placeholder: "A01" }, { key: "units", label: "Height (U)", type: "number" }]}
          onSubmit={async (b) => { await api("/provider/racks", { method: "POST", body: b }); await created("Rack added."); }} />
      )}
      {creating === "node" && (
        <CreateModal title="Add node" onClose={() => setCreating(null)}
          initial={{ server_id: servers.find((s) => s.status === "active" && !nodes.some((n) => n.server_id === s.id))?.id ?? "", datacenter_id: dcOptions[0]?.value ?? "", network: "default", cpu_overcommit: "4", memory_overcommit: "1" }}
          fields={[
            { key: "server_id", label: "Server", options: servers.filter((s) => s.status === "active" && !nodes.some((n) => n.server_id === s.id)).map((s) => ({ value: s.id, label: s.name })) },
            { key: "datacenter_id", label: "Data center", options: dcOptions },
            { key: "rack_id", label: "Rack (optional)", options: [{ value: "", label: "—" }, ...racks.map((r) => ({ value: r.id, label: `${dcName(r.datacenter_id)} / ${r.name}` }))] },
            { key: "network", label: "Existing libvirt network for VMs", placeholder: "default" },
            { key: "cpu_overcommit", label: "vCPU overcommit (1-16)", type: "number" },
            { key: "memory_overcommit", label: "Memory overcommit (1-4)", type: "number" },
          ]}
          onSubmit={async (b) => { await api("/provider/nodes", { method: "POST", body: b }); await created("Node added."); }} />
      )}
      {creating === "pool" && (
        <CreateModal title="Add IP pool" onClose={() => setCreating(null)}
          initial={{ datacenter_id: "" }}
          fields={[
            { key: "name", label: "Name", placeholder: "public-1" },
            { key: "datacenter_id", label: "Data center (optional)", options: [{ value: "", label: "—" }, ...dcOptions] },
            { key: "cidr", label: "Network (IPv4 CIDR)", placeholder: "203.0.113.0/24" },
            { key: "gateway", label: "Gateway", placeholder: "203.0.113.1" },
            { key: "first", label: "First address", placeholder: "203.0.113.10" },
            { key: "last", label: "Last address", placeholder: "203.0.113.200" },
            { key: "dns", label: "DNS servers (comma separated, max. 3)", placeholder: "9.9.9.9, 1.1.1.1" },
          ]}
          onSubmit={async (b) => { await api("/provider/ip-pools", { method: "POST", body: b }); await created("Pool added."); }} />
      )}
      {creating === "template" && (
        <CreateModal title="Add template" onClose={() => setCreating(null)} initial={{ min_disk_gb: "5", default_user: "ubuntu" }}
          fields={[
            { key: "name", label: "Name", placeholder: "ubuntu-24.04" },
            { key: "image_url", label: "Image URL (https)", placeholder: "https://cloud-images.ubuntu.com/…/ubuntu-24.04-minimal-cloudimg-amd64.img", wide: true },
            { key: "image_sha256", label: "SHA-256 (from the publisher's checksum list)", wide: true },
            { key: "min_disk_gb", label: "Minimum disk (GB)", type: "number" },
            { key: "default_user", label: "Default user of the image", placeholder: "ubuntu" },
          ]}
          onSubmit={async (b) => { await api("/provider/templates", { method: "POST", body: b }); await created("Template added."); }} />
      )}
      {host && (
        <Modal open onClose={() => setHost(null)} title={`Host check: ${host.node.server_name}`}>
          {host.error ? <Alert>{host.error}</Alert> : !host.info ? <LoadingBlock label="Asking the agent…" /> : (
            <div className="flex flex-col gap-2 text-sm">
              {host.info.available ? <Alert tone="success">Ready for virtual machines.</Alert> : <Alert tone="warning">{host.info.reason}</Alert>}
              <p>KVM: {host.info.kvm ? "yes" : "no"} · libvirt {host.info.libvirt_version || "—"} · ISO tool {host.info.iso_tool || "—"}</p>
              <p>{host.info.cpus} CPUs · {formatBytes(host.info.memory_bytes)} memory · {formatBytes(host.info.free_disk_bytes)} free for images</p>
              <p className="flex flex-wrap gap-1">Networks: {host.info.networks.length === 0 ? "none" : host.info.networks.map((n) => (
                <Badge key={n.name} tone={n.active ? "success" : "neutral"}>{n.name}{n.active ? "" : " (inactive)"}</Badge>
              ))}</p>
              {!host.info.networks.some((n) => n.name === host.node.network && n.active) && host.info.available && (
                <Alert tone="warning">The node uses network &quot;{host.node.network}&quot;, which is not active on this host.</Alert>
              )}
            </div>
          )}
        </Modal>
      )}
    </>
  );
}
