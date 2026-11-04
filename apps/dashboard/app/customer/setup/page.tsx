"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { errorMessage } from "@/lib/api";
import { capi } from "@/lib/customer-api";
import { Alert, Button, Card, Input } from "@/components/ui";

// The one-time token is in the URL fragment, which the browser never sends to a server.
export default function CustomerSetup() {
  const [token, setToken] = useState("");
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    // Effects may run twice (React strict mode): only a non-empty fragment sets the token.
    const t = window.location.hash.replace(/^#/, "");
    if (t) {
      setToken(t);
      history.replaceState(null, "", window.location.pathname); // keep the token out of history
    }
  }, []);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (password !== repeat) {
      setError("The passwords do not match.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await capi("/auth/setup", { method: "POST", body: { token, password } });
      setDone(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="mx-auto mt-16 w-full max-w-sm">
      <Card title="Set your password" description="This link works once.">
        {done ? (
          <div className="flex flex-col gap-3 text-sm">
            <Alert tone="success">Your password is set.</Alert>
            <Link className="text-accent hover:underline" href="/customer/login">Sign in</Link>
          </div>
        ) : !token ? (
          <Alert>This page needs the setup link from your provider.</Alert>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            {error && <Alert>{error}</Alert>}
            <Input label="New password" type="password" autoComplete="new-password" required value={password} onChange={(e) => setPassword(e.target.value)} hint="At least 12 characters." />
            <Input label="Repeat it" type="password" autoComplete="new-password" required value={repeat} onChange={(e) => setRepeat(e.target.value)} />
            <Button type="submit" loading={busy}>Set password</Button>
          </form>
        )}
      </Card>
    </div>
  );
}
