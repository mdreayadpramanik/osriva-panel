"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { api, errorMessage, type AuditEntry, type Meta, type Server } from "@/lib/api";
import { relativeTime } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Card, EmptyState, LoadingBlock, PageHeader } from "@/components/ui";
import { OutcomeBadge } from "./audit/outcome";

export default function OverviewPage() {
  const { me, can } = useSession();
  const [meta, setMeta] = useState<Meta | null>(null);
  const [activity, setActivity] = useState<AuditEntry[] | null>(null);
  const [servers, setServers] = useState<Server[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api<Meta>("/meta").then(setMeta).catch((e) => setError(errorMessage(e)));
    if (can("servers.read")) {
      api<{ servers: Server[] }>("/servers")
        .then((r) => setServers(r.servers))
        .catch((e) => setError(errorMessage(e)));
    }
    if (can("audit.read")) {
      api<{ entries: AuditEntry[] }>("/audit-logs?limit=8")
        .then((r) => setActivity(r.entries))
        .catch((e) => setError(errorMessage(e)));
    }
  }, [can]);

  const firstName = me.user.display_name.split(" ")[0];

  return (
    <>
      <PageHeader
        title={`Welcome, ${firstName}`}
        description={me.panel_mode === "provider" ? "Hosting Provider Mode" : "Personal Server Mode"}
      />
      {error && (
        <div className="mb-6">
          <Alert>{error}</Alert>
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-3">
        <Card
          title="Servers"
          description="Machines managed by this panel"
          className="lg:col-span-2"
          actions={
            can("servers.read") && (
              <Link href="/servers" className="text-sm font-medium text-accent hover:underline">
                Manage
              </Link>
            )
          }
        >
          {!can("servers.read") ? (
            <p className="text-sm text-muted">You do not have permission to view servers.</p>
          ) : servers === null ? (
            <LoadingBlock />
          ) : servers.length === 0 ? (
            <EmptyState title="No servers connected">
              Add a server and run its enrolment command to connect the OSRIVA Agent. This panel never shows placeholder statistics.
            </EmptyState>
          ) : (
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              {(["online", "offline", "pending", "revoked"] as const).map((c) => (
                <div key={c} className="rounded-lg border border-line p-4">
                  <p className="text-2xl font-semibold tabular-nums">{servers.filter((s) => s.connection === c).length}</p>
                  <p className="text-sm capitalize text-muted">{c === "pending" ? "awaiting agent" : c}</p>
                </div>
              ))}
            </div>
          )}
        </Card>

        <Card title="Your account">
          <dl className="flex flex-col gap-3 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Roles</dt>
              <dd className="flex flex-wrap justify-end gap-1">
                {me.user.roles.map((r) => (
                  <Badge key={r.key}>{r.name}</Badge>
                ))}
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Two-factor</dt>
              <dd>{me.user.mfa_enabled ? <Badge tone="success">Enabled</Badge> : <Badge tone="warning">Not enabled</Badge>}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Session expires</dt>
              <dd>{relativeTime(me.session.expires_at)}</dd>
            </div>
          </dl>
          {!me.user.mfa_enabled && (
            <Link href="/account" className="mt-4 block text-sm font-medium text-accent hover:underline">
              Enable two-factor authentication →
            </Link>
          )}
        </Card>

        {can("audit.read") && (
          <Card
            title="Recent activity"
            className="lg:col-span-2"
            actions={
              <Link href="/audit" className="text-sm font-medium text-accent hover:underline">
                View all
              </Link>
            }
          >
            {activity === null ? (
              <LoadingBlock />
            ) : activity.length === 0 ? (
              <EmptyState title="No activity yet" />
            ) : (
              <ol className="flex flex-col divide-y divide-line">
                {activity.map((e) => (
                  <li key={e.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
                    <span className="min-w-0">
                      <span className="font-mono text-xs">{e.action}</span>
                      <span className="text-muted"> · {e.actor_label || "system"}</span>
                    </span>
                    <span className="flex items-center gap-2 text-xs text-muted">
                      <OutcomeBadge outcome={e.outcome} />
                      {relativeTime(e.created_at)}
                    </span>
                  </li>
                ))}
              </ol>
            )}
          </Card>
        )}

        <Card title="Control plane">
          {meta ? (
            <dl className="flex flex-col gap-3 text-sm">
              <div className="flex justify-between gap-4">
                <dt className="text-muted">Version</dt>
                <dd className="font-mono">{meta.version}</dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt className="text-muted">API</dt>
                <dd className="font-mono">{meta.api_version}</dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt className="text-muted">License</dt>
                <dd>{meta.license}</dd>
              </div>
            </dl>
          ) : (
            <LoadingBlock />
          )}
        </Card>
      </div>
    </>
  );
}
