"use client";

import { useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { ApiError, errorMessage } from "@/lib/api";
import { capi, setCustomerCsrf, type CustomerMe, type CustomerVPS } from "@/lib/customer-api";
import { Alert, Badge, Button, Card, EmptyState, Input, LoadingBlock, PageHeader, Table, Td, Th } from "@/components/ui";
import { ThemeToggle } from "@/components/theme";

const stateTone = { creating: "accent", running: "success", stopped: "neutral", error: "danger", deleting: "warning" } as const;

export default function CustomerHome() {
  const router = useRouter();
  const [me, setMe] = useState<CustomerMe | null>(null);
  const [vps, setVps] = useState<CustomerVPS[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const m = await capi<CustomerMe>("/auth/me");
      setCustomerCsrf(m.csrf_token);
      setMe(m);
      setVps((await capi<{ vps: CustomerVPS[] }>("/vps")).vps);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        router.replace("/customer/login");
        return;
      }
      setError(errorMessage(e));
    }
  }, [router]);
  const pending = (vps ?? []).some((v) => v.pending);
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), pending ? 3000 : 30_000);
    return () => clearInterval(t);
  }, [load, pending]);

  async function act(v: CustomerVPS, action: string) {
    setError(null);
    setNotice(null);
    try {
      await capi(`/vps/${v.id}/actions`, { method: "POST", body: { action } });
      setNotice(`${action} requested for ${v.name}.`);
    } catch (e) {
      setError(errorMessage(e));
    }
    await load();
  }

  async function logout() {
    try {
      await capi("/auth/logout", { method: "POST" });
    } finally {
      setCustomerCsrf(null);
      router.replace("/customer/login");
    }
  }

  if (!me) return error ? <Alert>{error}</Alert> : <LoadingBlock />;
  return (
    <>
      <PageHeader
        title="Your servers"
        description={`Signed in as ${me.customer.email}`}
        actions={<div className="flex items-center gap-2"><ThemeToggle /><Button variant="secondary" onClick={() => void logout()}>Sign out</Button></div>}
      />
      {error && <div className="mb-4"><Alert>{error}</Alert></div>}
      {notice && <div className="mb-4"><Alert tone="success">{notice}</Alert></div>}
      <div className="flex flex-col gap-6">
        <Card>
          {vps === null ? <LoadingBlock /> : vps.length === 0 ? (
            <EmptyState title="No servers yet">Your provider has not assigned a server to your account.</EmptyState>
          ) : (
            <Table>
              <thead><tr><Th>Server</Th><Th>Address</Th><Th>Size</Th><Th>State</Th><Th /></tr></thead>
              <tbody>
                {vps.map((v) => (
                  <tr key={v.id}>
                    <Td className="font-medium">{v.name}</Td>
                    <Td className="font-mono text-xs">{v.ip_address}</Td>
                    <Td className="text-xs">{v.vcpus} vCPU · {v.memory_mb} MB · {v.disk_gb} GB</Td>
                    <Td>
                      <Badge tone={stateTone[v.state]}>{v.state}</Badge>
                      {v.pending && <span className="ml-1 text-xs text-muted">{v.pending}…</span>}
                      {v.last_operation_failed && <p className="text-[11px] text-danger">The last operation failed. Contact your provider if this persists.</p>}
                    </Td>
                    <Td className="whitespace-nowrap text-right">
                      {!v.pending && v.state === "stopped" && <Button size="sm" variant="ghost" onClick={() => void act(v, "start")}>Start</Button>}
                      {!v.pending && v.state === "running" && (
                        <>
                          <Button size="sm" variant="ghost" onClick={() => void act(v, "reboot")}>Reboot</Button>
                          <Button size="sm" variant="ghost" onClick={() => { if (confirm(`Shut down ${v.name}?`)) void act(v, "shutdown"); }}>Shut down</Button>
                        </>
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
          {vps && vps.length > 0 && <p className="mt-3 text-xs text-muted">Log in with SSH as the image&apos;s default user (e.g. ubuntu) with the key you gave your provider.</p>}
        </Card>
        <PasswordCard />
      </div>
    </>
  );
}

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      await capi("/auth/password", { method: "POST", body: { current_password: current, new_password: next } });
      setMsg({ ok: true, text: "Password changed. Other sessions were signed out." });
      setCurrent("");
      setNext("");
    } catch (err) {
      setMsg({ ok: false, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card title="Password">
      <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
        {msg && <div className="w-full"><Alert tone={msg.ok ? "success" : "danger"}>{msg.text}</Alert></div>}
        <Input label="Current password" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
        <Input label="New password" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />
        <Button type="submit" loading={busy} disabled={!current || !next}>Change</Button>
      </form>
    </Card>
  );
}
