"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type CustomerAccount, type HVNode, type IPPool, type VMTemplate, type VPS } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, Modal, PageHeader, Table, Td, Th } from "@/components/ui";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";
const stateTone = { creating: "accent", running: "success", stopped: "neutral", error: "danger", deleting: "warning" } as const;

export default function VPSPage() {
  const { can, me, privileged } = useSession();
  const [list, setList] = useState<VPS[] | null>(null);
  const [nodes, setNodes] = useState<HVNode[]>([]);
  const [pools, setPools] = useState<IPPool[]>([]);
  const [templates, setTemplates] = useState<VMTemplate[]>([]);
  const [customers, setCustomers] = useState<CustomerAccount[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<VPS | null>(null);

  const load = useCallback(async () => {
    try {
      const [v, n, p, t, c] = await Promise.all([
        api<{ vps: VPS[] }>("/provider/vps"),
        api<{ nodes: HVNode[] }>("/provider/nodes"),
        api<{ pools: IPPool[] }>("/provider/ip-pools"),
        api<{ templates: VMTemplate[] }>("/provider/templates"),
        api<{ customers: CustomerAccount[] }>("/customers").catch(() => ({ customers: [] as CustomerAccount[] })),
      ]);
      setList(v.vps);
      setNodes(n.nodes);
      setPools(p.pools);
      setTemplates(t.templates);
      setCustomers(c.customers);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);
  const pending = (list ?? []).some((v) => v.pending_task_id);
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), pending ? 3000 : 30_000);
    return () => clearInterval(t);
  }, [load, pending]);

  const writable = can("provider.write") && me.panel_mode === "provider";
  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.server_name ?? "?";

  async function assign(v: VPS, customerId: string) {
    setError(null);
    setNotice(null);
    try {
      await api(`/provider/vps/${v.id}/customer`, { method: "PUT", body: { customer_id: customerId || null } });
      setNotice(customerId ? `${v.name} assigned to its customer.` : `${v.name} no longer has a customer.`);
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }

  async function act(v: VPS, action: string) {
    if (action === "stop" && !confirm(`Force off ${v.name}? Unsaved data in the guest is lost.`)) return;
    setError(null);
    setNotice(null);
    try {
      await privileged(() => api(`/provider/vps/${v.id}/actions`, { method: "POST", body: { action } }));
      setNotice(`${action} queued for ${v.name}.`);
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }

  return (
    <>
      <PageHeader
        title="VPS"
        description="Virtual machines on libvirt/KVM nodes, created from cloud images with a static address from an IP pool. Login is by SSH key only."
        actions={writable && <Button onClick={() => setCreating(true)} disabled={nodes.length === 0 || pools.length === 0 || templates.length === 0}>Create VPS</Button>}
      />
      {me.panel_mode !== "provider" && <div className="mb-4"><Alert tone="warning">The panel is in Personal Server Mode. Switch to Hosting Provider Mode in Settings to make changes.</Alert></div>}
      {writable && (nodes.length === 0 || pools.length === 0 || templates.length === 0) && (
        <div className="mb-4"><Alert tone="warning">Add a node, an IP pool and a template under Infrastructure first.</Alert></div>
      )}
      {error && <div className="mb-4"><Alert>{error}</Alert></div>}
      {notice && <div className="mb-4"><Alert tone="success">{notice}</Alert></div>}
      <Card>
        {list === null ? (
          !error && <LoadingBlock />
        ) : list.length === 0 ? (
          <EmptyState title="No VPS yet">A VPS is created in about a minute once its image is cached on the node.</EmptyState>
        ) : (
          <Table>
            <thead><tr><Th>VPS</Th><Th>Address</Th><Th>Size</Th><Th>Node</Th><Th>Customer</Th><Th>State</Th><Th className="text-right">{writable ? "Actions" : ""}</Th></tr></thead>
            <tbody>
              {list.map((v) => (
                <tr key={v.id}>
                  <Td><p className="font-medium">{v.name}</p><p className="text-xs text-muted">{formatDate(v.created_at)}</p></Td>
                  <Td className="font-mono text-xs">{v.ip_address}</Td>
                  <Td className="text-xs">{v.vcpus} vCPU · {v.memory_mb} MB · {v.disk_gb} GB</Td>
                  <Td className="text-xs">{nodeName(v.node_id)}</Td>
                  <Td className="text-xs">
                    {writable && can("customers.write") ? (
                      <select aria-label={`Customer of ${v.name}`} className="h-8 rounded-lg border border-line bg-surface px-2 text-xs" value={v.customer_id ?? ""} onChange={(e) => void assign(v, e.target.value)}>
                        <option value="">—</option>
                        {customers.map((c) => <option key={c.id} value={c.id}>{c.email}</option>)}
                      </select>
                    ) : (customers.find((c) => c.id === v.customer_id)?.email ?? "—")}
                  </Td>
                  <Td>
                    <Badge tone={stateTone[v.state]}>{v.state}</Badge>
                    {v.pending_op && v.state !== "creating" && v.state !== "deleting" && <span className="ml-1 text-xs text-muted">{v.pending_op}…</span>}
                    {v.last_error && <p className="max-w-xs text-[11px] text-danger">{v.last_error}</p>}
                  </Td>
                  <Td className="whitespace-nowrap text-right">
                    {writable && !v.pending_task_id && (
                      <>
                        {v.state === "stopped" && <Button size="sm" variant="ghost" onClick={() => void act(v, "start")}>Start</Button>}
                        {v.state === "running" && (
                          <>
                            <Button size="sm" variant="ghost" onClick={() => void act(v, "reboot")}>Reboot</Button>
                            <Button size="sm" variant="ghost" onClick={() => void act(v, "shutdown")}>Shut down</Button>
                            <Button size="sm" variant="ghost" onClick={() => void act(v, "stop")}>Force off</Button>
                          </>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => setDeleting(v)}>Delete</Button>
                      </>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
      {creating && (
        <CreateVPS nodes={nodes} pools={pools} templates={templates} onClose={() => setCreating(false)}
          onCreated={(v) => { setCreating(false); setNotice(`${v.name} is being created with address ${v.ip_address}.`); void load(); }} />
      )}
      {deleting && (
        <DeleteVPS vps={deleting} onClose={() => setDeleting(null)}
          onDeleted={() => { setNotice(`${deleting.name} is being deleted.`); setDeleting(null); void load(); }} />
      )}
    </>
  );
}

function CreateVPS({ nodes, pools, templates, onClose, onCreated }: { nodes: HVNode[]; pools: IPPool[]; templates: VMTemplate[]; onClose: () => void; onCreated: (v: VPS) => void }) {
  const [f, setF] = useState({ name: "", node_id: nodes[0]?.id ?? "", template_id: templates[0]?.id ?? "", pool_id: pools[0]?.id ?? "",
    vcpus: 1, memory_mb: 1024, disk_gb: Math.max(10, templates[0]?.min_disk_gb ?? 10), keys: "" });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const node = nodes.find((n) => n.id === f.node_id);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      const { keys, ...rest } = f;
      const v = await api<VPS>("/provider/vps", { method: "POST", body: { ...rest, ssh_keys: keys.split("\n").map((k) => k.trim()).filter(Boolean) } });
      onCreated(v);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const num = (k: "vcpus" | "memory_mb" | "disk_gb") => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: Number(e.target.value) });
  return (
    <Modal open onClose={onClose} title="Create VPS" footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button loading={busy} disabled={!f.name || !f.keys.trim()} onClick={() => void submit()}>Create</Button></>}>
      <div className="flex flex-col gap-3 text-sm">
        {error && <Alert>{error}</Alert>}
        <label className="flex flex-col gap-1">Name (also the host name)<input className={inputClass} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value.toLowerCase() })} placeholder="web-1" /></label>
        <label className="flex flex-col gap-1">Node
          <select className={inputClass} value={f.node_id} onChange={(e) => setF({ ...f, node_id: e.target.value })}>
            {nodes.map((n) => <option key={n.id} value={n.id}>{n.server_name} ({n.vcpus - n.used_vcpus} vCPU, {n.memory_mb - n.used_memory_mb} MB free)</option>)}
          </select>
        </label>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1">Template
            <select className={inputClass} value={f.template_id} onChange={(e) => setF({ ...f, template_id: e.target.value })}>
              {templates.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
            </select>
          </label>
          <label className="flex flex-col gap-1">IP pool
            <select className={inputClass} value={f.pool_id} onChange={(e) => setF({ ...f, pool_id: e.target.value })}>
              {pools.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.size - p.allocated.length} free)</option>)}
            </select>
          </label>
        </div>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1">vCPUs<input type="number" min={1} max={64} className={inputClass + " w-24"} value={f.vcpus} onChange={num("vcpus")} /></label>
          <label className="flex flex-col gap-1">Memory (MB)<input type="number" min={256} step={256} className={inputClass + " w-32"} value={f.memory_mb} onChange={num("memory_mb")} /></label>
          <label className="flex flex-col gap-1">Disk (GB)<input type="number" min={1} className={inputClass + " w-24"} value={f.disk_gb} onChange={num("disk_gb")} /></label>
        </div>
        {node && node.connection !== "online" && <Alert tone="warning">The node&apos;s agent is {node.connection || "unknown"}; the VPS is created when it comes back within 2 minutes, otherwise the task expires.</Alert>}
        <label className="flex flex-col gap-1">SSH public keys (one per line; password login is disabled)
          <textarea className="min-h-24 rounded-lg border border-line bg-surface p-2 font-mono text-xs" value={f.keys} onChange={(e) => setF({ ...f, keys: e.target.value })} placeholder="ssh-ed25519 AAAA… you@laptop" />
        </label>
      </div>
    </Modal>
  );
}

function DeleteVPS({ vps, onClose, onDeleted }: { vps: VPS; onClose: () => void; onDeleted: () => void }) {
  const { privileged } = useSession();
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      await privileged(() => api(`/provider/vps/${vps.id}`, { method: "DELETE", body: { confirm: typed } }));
      onDeleted();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title={`Delete ${vps.name}`} footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="danger" loading={busy} disabled={typed !== vps.name} onClick={() => void submit()}>Delete VPS and disk</Button></>}>
      <div className="flex flex-col gap-3 text-sm">
        {error && <Alert>{error}</Alert>}
        <Alert tone="warning">The virtual machine is powered off and its disk is destroyed. This cannot be undone. The address {vps.ip_address} returns to its pool.</Alert>
        <label className="flex flex-col gap-1">Type <span className="font-mono">{vps.name}</span> to confirm<input className={inputClass} value={typed} onChange={(e) => setTyped(e.target.value)} /></label>
      </div>
    </Modal>
  );
}
