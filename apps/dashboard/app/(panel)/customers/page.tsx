"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type CustomerAccount, type VPS } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, LoadingBlock, PageHeader, Table, Td, Th } from "@/components/ui";

const inputClass = "h-9 rounded-lg border border-line bg-surface px-3 text-sm";

export default function CustomersPage() {
  const { can, privileged } = useSession();
  const [list, setList] = useState<CustomerAccount[] | null>(null);
  const [vps, setVps] = useState<VPS[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [link, setLink] = useState<{ email: string; url: string } | null>(null);
  const [form, setForm] = useState({ email: "", name: "" });
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [c, v] = await Promise.all([
        api<{ customers: CustomerAccount[] }>("/customers"),
        api<{ vps: VPS[] }>("/provider/vps").catch(() => ({ vps: [] as VPS[] })),
      ]);
      setList(c.customers);
      setVps(v.vps);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);
  useEffect(() => void load(), [load]);
  const writable = can("customers.write");
  const showLink = (email: string, path: string) => setLink({ email, url: `${window.location.origin}${path}` });

  async function create() {
    setBusy(true);
    setError(null);
    try {
      const r = await api<{ customer: CustomerAccount; setup_path: string }>("/customers", { method: "POST", body: form });
      showLink(r.customer.email, r.setup_path);
      setForm({ email: "", name: "" });
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
    await load();
  }
  async function newLink(c: CustomerAccount) {
    if (!confirm(`Issue a new setup link for ${c.email}? Their current sessions are signed out.`)) return;
    setError(null);
    try {
      const r = await privileged(() => api<{ customer: CustomerAccount; setup_path: string }>(`/customers/${c.id}/setup-link`, { method: "POST" }));
      showLink(c.email, r.setup_path);
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }
  async function toggle(c: CustomerAccount) {
    setError(null);
    try {
      await api(`/customers/${c.id}`, { method: "PATCH", body: { enabled: c.status !== "active" } });
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }

  return (
    <>
      <PageHeader title="Customers" description="Customer Panel accounts. A customer signs in at /customer and sees only the VPS assigned to them. Assign VPS on the VPS page." />
      {error && <div className="mb-4"><Alert>{error}</Alert></div>}
      {link && (
        <div className="mb-4">
          <Alert tone="warning" title={`One-time setup link for ${link.email} (valid 72 hours)`}>
            <p className="select-all break-all font-mono text-xs">{link.url}</p>
            <p className="mt-1 text-xs">Send it to the customer over a channel you trust. It is shown once; the customer chooses their own password with it.</p>
          </Alert>
        </div>
      )}
      <Card>
        {list === null ? (
          !error && <LoadingBlock />
        ) : list.length === 0 ? (
          <EmptyState title="No customers yet">Create an account, then send the setup link to the customer.</EmptyState>
        ) : (
          <Table>
            <thead><tr><Th>Customer</Th><Th>VPS</Th><Th>Last login</Th><Th /></tr></thead>
            <tbody>
              {list.map((c) => (
                <tr key={c.id}>
                  <Td>
                    <p className="font-medium">{c.name} {c.status !== "active" && <Badge tone="warning">disabled</Badge>}</p>
                    <p className="text-xs text-muted">{c.email}{!c.password_set && " · password not set yet"}</p>
                  </Td>
                  <Td className="text-xs">{(c.vps_ids ?? []).map((id) => vps.find((v) => v.id === id)?.name ?? id.slice(0, 8)).join(", ") || <span className="text-muted">none</span>}</Td>
                  <Td className="text-xs text-muted">{c.last_login_at ? formatDate(c.last_login_at) : "never"}</Td>
                  <Td className="whitespace-nowrap text-right">
                    {writable && (
                      <>
                        <Button size="sm" variant="ghost" onClick={() => void newLink(c)}>New setup link</Button>
                        <Button size="sm" variant="ghost" onClick={() => void toggle(c)}>{c.status === "active" ? "Disable" : "Enable"}</Button>
                      </>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        {writable && (
          <div className="mt-4 flex flex-wrap items-end gap-3 border-t border-line pt-4 text-sm">
            <label className="flex flex-col gap-1">E-mail<input className={inputClass} type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} placeholder="customer@example.com" /></label>
            <label className="flex flex-col gap-1">Name<input className={inputClass} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Example Ltd." /></label>
            <Button loading={busy} disabled={!form.email || !form.name} onClick={() => void create()}>Create customer</Button>
          </div>
        )}
      </Card>
    </>
  );
}
