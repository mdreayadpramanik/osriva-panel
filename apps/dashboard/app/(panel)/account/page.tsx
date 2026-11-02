"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type AuditEntry, type Session } from "@/lib/api";
import { describeUserAgent, formatDate, relativeTime } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, Input, LoadingBlock, PageHeader, Table, Td, Th } from "@/components/ui";
import { OutcomeBadge } from "../audit/outcome";

export default function AccountPage() {
  const { me } = useSession();
  return (
    <>
      <PageHeader title="My Account" description={`${me.user.display_name} · ${me.user.email}`} />
      <div className="grid gap-6 lg:grid-cols-2">
        <PasswordCard />
        <TwoFactorCard />
        <SessionsCard />
        <LoginHistoryCard />
      </div>
    </>
  );
}

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setDone(false);
    if (next !== confirm) {
      setError("The new passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      await api("/auth/password", { method: "POST", body: { current_password: current, new_password: next } });
      setDone(true);
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Password" description="Changing your password signs out all your other sessions.">
      <form onSubmit={submit} className="flex flex-col gap-4">
        {error && <Alert>{error}</Alert>}
        {done && <Alert tone="success">Password changed. Your other sessions were signed out.</Alert>}
        <Input label="Current password" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
        <Input label="New password" type="password" autoComplete="new-password" minLength={12} required hint="At least 12 characters." value={next} onChange={(e) => setNext(e.target.value)} />
        <Input label="Confirm new password" type="password" autoComplete="new-password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        <Button type="submit" loading={busy} className="self-start">
          Change password
        </Button>
      </form>
    </Card>
  );
}

function TwoFactorCard() {
  const { me, refresh } = useSession();
  const [enrollment, setEnrollment] = useState<{ secret: string; otpauth_uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function act(fn: () => Promise<void>) {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const enabled = me.user.mfa_enabled;

  return (
    <Card
      title="Two-factor authentication"
      description="Time-based one-time codes (TOTP) from an authenticator app."
      actions={enabled ? <Badge tone="success">Enabled</Badge> : <Badge tone="warning">Off</Badge>}
    >
      <div className="flex flex-col gap-4">
        {error && <Alert>{error}</Alert>}
        {enabled ? (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                await api("/auth/mfa/totp/disable", { method: "POST", body: { password, code } });
                setPassword("");
                setCode("");
                await refresh();
              });
            }}
          >
            <p className="text-sm text-muted">To turn off two-factor authentication, confirm your password and a current code.</p>
            <Input label="Password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            <Input label="Authenticator code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} required value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
            <Button type="submit" variant="danger" loading={busy} className="self-start">
              Disable two-factor
            </Button>
          </form>
        ) : enrollment ? (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                await api("/auth/mfa/totp/enable", { method: "POST", body: { code } });
                setEnrollment(null);
                setCode("");
                await refresh();
              });
            }}
          >
            <ol className="list-decimal space-y-2 pl-5 text-sm text-muted">
              <li>Open your authenticator app (for example Aegis, FreeOTP, 2FAS or Google Authenticator) and add an account manually.</li>
              <li>
                Enter this setup key:
                <code className="mt-1 block select-all break-all rounded-lg bg-surface-2 px-3 py-2 font-mono text-sm text-fg">{enrollment.secret}</code>
              </li>
              <li>Type the 6-digit code it shows to finish.</li>
            </ol>
            <details className="text-xs text-muted">
              <summary className="cursor-pointer">Show otpauth:// URI</summary>
              <code className="mt-1 block select-all break-all font-mono">{enrollment.otpauth_uri}</code>
            </details>
            <Input label="Code from your app" inputMode="numeric" autoComplete="one-time-code" maxLength={6} required value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
            <div className="flex gap-2">
              <Button type="submit" loading={busy}>
                Turn on
              </Button>
              <Button type="button" variant="secondary" onClick={() => setEnrollment(null)}>
                Cancel
              </Button>
            </div>
          </form>
        ) : (
          <>
            <p className="text-sm text-muted">Protect your account with a second factor. You will need your authenticator app every time you sign in.</p>
            <Button
              className="self-start"
              loading={busy}
              onClick={() => void act(async () => setEnrollment(await api("/auth/mfa/totp/setup", { method: "POST" })))}
            >
              Set up two-factor
            </Button>
          </>
        )}
      </div>
    </Card>
  );
}

function SessionsCard() {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(() => {
    api<{ sessions: Session[] }>("/auth/sessions")
      .then((r) => setSessions(r.sessions))
      .catch((e) => setError(errorMessage(e)));
  }, []);
  useEffect(load, [load]);

  async function revoke(id: string | "others") {
    setBusy(id);
    setError(null);
    try {
      if (id === "others") await api("/auth/sessions/revoke-others", { method: "POST" });
      else await api(`/auth/sessions/${id}`, { method: "DELETE" });
      load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(null);
    }
  }

  const others = sessions?.filter((s) => !s.current).length ?? 0;

  return (
    <Card
      title="Active sessions"
      description="Devices currently signed in to your account."
      actions={
        others > 0 && (
          <Button size="sm" variant="secondary" loading={busy === "others"} onClick={() => void revoke("others")}>
            Sign out others
          </Button>
        )
      }
    >
      {error && <Alert>{error}</Alert>}
      {sessions === null ? (
        <LoadingBlock />
      ) : (
        <ul className="flex flex-col divide-y divide-line">
          {sessions.map((s) => (
            <li key={s.id} className="flex items-center justify-between gap-3 py-3 text-sm">
              <div className="min-w-0">
                <p className="font-medium">
                  {describeUserAgent(s.user_agent)} {s.current && <Badge tone="accent">This device</Badge>}
                </p>
                <p className="text-xs text-muted">
                  <span className="font-mono">{s.ip || "unknown IP"}</span> · active {relativeTime(s.last_seen_at)} · signed in {formatDate(s.created_at)}
                </p>
              </div>
              {!s.current && (
                <Button size="sm" variant="ghost" loading={busy === s.id} onClick={() => void revoke(s.id)}>
                  Revoke
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function LoginHistoryCard() {
  const [events, setEvents] = useState<AuditEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api<{ events: AuditEntry[] }>("/auth/login-history?limit=20")
      .then((r) => setEvents(r.events))
      .catch((e) => setError(errorMessage(e)));
  }, []);
  return (
    <Card title="Security activity" description="Your recent sign-ins and account security events." className="lg:col-span-2">
      {error && <Alert>{error}</Alert>}
      {events === null ? (
        <LoadingBlock />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Time</Th>
              <Th>Event</Th>
              <Th>Outcome</Th>
              <Th>Source</Th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr key={e.id}>
                <Td className="whitespace-nowrap text-muted">{formatDate(e.created_at)}</Td>
                <Td>
                  <code className="font-mono text-xs">{e.action}</code>
                </Td>
                <Td>
                  <OutcomeBadge outcome={e.outcome} />
                </Td>
                <Td className="text-xs text-muted">
                  <span className="font-mono">{e.ip || "—"}</span> · {describeUserAgent(e.user_agent)}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </Card>
  );
}
