"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type AuditEntry } from "@/lib/api";
import { describeUserAgent, formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Button, Card, EmptyState, LoadingBlock, PageHeader, Table, Td, Th } from "@/components/ui";
import { OutcomeBadge } from "./outcome";

type Page = { entries: AuditEntry[]; next_before: number | null };

const actionFilters = [
  { value: "", label: "All actions" },
  { value: "auth.", label: "Authentication" },
  { value: "users.", label: "User management" },
  { value: "authz.", label: "Authorization denials" },
  { value: "settings.", label: "Settings" },
];

export default function AuditPage() {
  const { can } = useSession();
  const [action, setAction] = useState("");
  const [outcome, setOutcome] = useState("");
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [next, setNext] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const pageSize = 50;

  const fetchPage = useCallback(
    (before?: number) => {
      const q = new URLSearchParams({ limit: String(pageSize) });
      if (action) q.set("action", action);
      if (outcome) q.set("outcome", outcome);
      if (before) q.set("before", String(before));
      return api<Page>(`/audit-logs?${q}`);
    },
    [action, outcome],
  );

  useEffect(() => {
    setEntries(null);
    fetchPage()
      .then((p) => {
        setEntries(p.entries);
        setNext(p.entries.length === pageSize ? p.next_before : null);
        setError(null);
      })
      .catch((e) => setError(errorMessage(e)));
  }, [fetchPage]);

  async function loadMore() {
    if (!next) return;
    setLoadingMore(true);
    try {
      const p = await fetchPage(next);
      setEntries((cur) => [...(cur ?? []), ...p.entries]);
      setNext(p.entries.length === pageSize ? p.next_before : null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoadingMore(false);
    }
  }

  if (!can("audit.read")) return <Alert>You do not have permission to view the audit log.</Alert>;

  const select = "h-9 rounded-lg border border-line bg-surface px-2 text-sm";

  return (
    <>
      <PageHeader title="Audit Log" description="Every security-relevant action, newest first. Entries cannot be edited or deleted from the panel." />
      {error && (
        <div className="mb-4">
          <Alert>{error}</Alert>
        </div>
      )}
      <Card
        actions={
          <div className="flex flex-wrap gap-2">
            <select aria-label="Filter by action" className={select} value={action} onChange={(e) => setAction(e.target.value)}>
              {actionFilters.map((f) => (
                <option key={f.value} value={f.value}>
                  {f.label}
                </option>
              ))}
            </select>
            <select aria-label="Filter by outcome" className={select} value={outcome} onChange={(e) => setOutcome(e.target.value)}>
              <option value="">All outcomes</option>
              <option value="success">Success</option>
              <option value="failure">Failure</option>
              <option value="denied">Denied</option>
            </select>
          </div>
        }
        title="Events"
      >
        {entries === null ? (
          <LoadingBlock />
        ) : entries.length === 0 ? (
          <EmptyState title="No matching events" />
        ) : (
          <>
            <Table>
              <thead>
                <tr>
                  <Th>Time</Th>
                  <Th>Action</Th>
                  <Th>Actor</Th>
                  <Th>Outcome</Th>
                  <Th>Source</Th>
                  <Th>Details</Th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <tr key={e.id}>
                    <Td className="whitespace-nowrap text-muted">{formatDate(e.created_at)}</Td>
                    <Td>
                      <code className="font-mono text-xs">{e.action}</code>
                    </Td>
                    <Td>{e.actor_label || <span className="text-muted">system</span>}</Td>
                    <Td>
                      <OutcomeBadge outcome={e.outcome} />
                    </Td>
                    <Td className="text-xs text-muted">
                      <p className="font-mono">{e.ip || "—"}</p>
                      <p>{describeUserAgent(e.user_agent)}</p>
                    </Td>
                    <Td className="max-w-xs">
                      {Object.keys(e.metadata).length > 0 && (
                        <code className="block truncate font-mono text-[11px] text-muted" title={JSON.stringify(e.metadata)}>
                          {JSON.stringify(e.metadata)}
                        </code>
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
            {next && (
              <div className="mt-4 flex justify-center">
                <Button variant="secondary" loading={loadingMore} onClick={() => void loadMore()}>
                  Load older events
                </Button>
              </div>
            )}
          </>
        )}
      </Card>
    </>
  );
}
