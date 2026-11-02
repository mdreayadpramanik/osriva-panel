"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type AgentEndpointInfo, type Enrollment, type Server } from "@/lib/api";
import { formatDate, relativeTime } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Button, Card, EmptyState, Input, LoadingBlock, Modal, PageHeader, Table, Td, Th } from "@/components/ui";
import { ConnectionBadge } from "./connection";
import { EnrollmentInstructions } from "./enrollment";

export default function ServersPage() {
  const { can } = useSession();
  const [servers, setServers] = useState<Server[] | null>(null);
  const [endpoint, setEndpoint] = useState<AgentEndpointInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const load = useCallback(() => {
    api<{ servers: Server[] }>("/servers")
      .then((r) => {
        setServers(r.servers);
        setError(null);
      })
      .catch((e) => setError(errorMessage(e)));
  }, []);

  useEffect(() => {
    load();
    api<AgentEndpointInfo>("/servers/agent-endpoint").then(setEndpoint).catch(() => {});
    // Connection state is derived from agent reports; refresh it periodically.
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, [load]);

  const canWrite = can("servers.write");

  return (
    <>
      <PageHeader
        title="Servers"
        description="Machines running the OSRIVA Agent. Everything shown here was reported by the agent itself."
        actions={
          canWrite && (
            <Button onClick={() => setAdding(true)} disabled={endpoint?.configured === false}>
              Add server
            </Button>
          )
        }
      />
      {error && (
        <div className="mb-4">
          <Alert>{error}</Alert>
        </div>
      )}
      {endpoint && !endpoint.configured && (
        <div className="mb-4">
          <Alert tone="warning" title="Agent endpoint not configured">
            Servers cannot enrol until the control plane is started with <code className="font-mono">OSRIVA_PUBLIC_URL</code>,{" "}
            <code className="font-mono">OSRIVA_AGENT_ADDR</code> and <code className="font-mono">OSRIVA_AGENT_PUBLIC_URL</code>. Agents also need
            network access to the agent port. Open it in your firewall yourself.
          </Alert>
        </div>
      )}

      <Card>
        {servers === null ? (
          !error && <LoadingBlock />
        ) : servers.length === 0 ? (
          <EmptyState
            title="No servers yet"
            action={canWrite && endpoint?.configured && <Button onClick={() => setAdding(true)}>Add your first server</Button>}
          >
            Add a server to get a one-time enrolment command for its agent.
          </EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Server</Th>
                <Th>Status</Th>
                <Th>Operating system</Th>
                <Th>Agent</Th>
                <Th>Last report</Th>
              </tr>
            </thead>
            <tbody>
              {servers.map((s) => (
                <tr key={s.id}>
                  <Td>
                    <Link href={`/servers/${s.id}`} className="font-medium text-accent hover:underline">
                      {s.name}
                    </Link>
                    <p className="text-xs text-muted">{s.hostname || "—"}</p>
                  </Td>
                  <Td>
                    <ConnectionBadge connection={s.connection} />
                  </Td>
                  <Td>
                    {s.os_name ? (
                      <>
                        <p>{s.os_name}</p>
                        <p className="text-xs text-muted">
                          {s.kernel} · {s.arch}
                        </p>
                      </>
                    ) : (
                      <span className="text-muted">Not reported yet</span>
                    )}
                  </Td>
                  <Td className="font-mono text-xs text-muted">{s.agent_version || "—"}</Td>
                  <Td className="text-muted">
                    <span title={formatDate(s.last_seen_at)}>{s.last_seen_at ? relativeTime(s.last_seen_at) : "never"}</span>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <AddServerModal open={adding} onClose={() => setAdding(false)} onCreated={load} />
    </>
  );
}

function AddServerModal({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setName("");
      setEnrollment(null);
      setError(null);
    }
  }, [open]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await api<{ enrollment: Enrollment }>("/servers", { method: "POST", body: { name } });
      setEnrollment(r.enrollment);
      onCreated();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={enrollment ? `Enrol ${name}` : "Add server"}>
      {enrollment ? (
        <div className="flex flex-col gap-4">
          <EnrollmentInstructions enrollment={enrollment} />
          <Button className="self-end" onClick={onClose}>
            Done
          </Button>
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-4">
          {error && <Alert>{error}</Alert>}
          <Input
            label="Server name"
            hint="A label for this server in the panel, e.g. web-1. Letters, digits, dot, dash and underscore."
            required
            maxLength={100}
            pattern="[A-Za-z0-9][A-Za-z0-9._\-]*"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={busy}>
              Create enrolment command
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
