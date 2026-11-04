"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { errorMessage } from "@/lib/api";
import { capi, setCustomerCsrf, type CustomerMe } from "@/lib/customer-api";
import { Alert, Button, Card, Input } from "@/components/ui";

export default function CustomerLogin() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const me = await capi<CustomerMe>("/auth/login", { method: "POST", body: { email, password } });
      setCustomerCsrf(me.csrf_token);
      router.replace("/customer");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="mx-auto mt-16 w-full max-w-sm">
      <Card title="Customer Panel" description="Sign in to manage your servers.">
        <form onSubmit={submit} className="flex flex-col gap-4">
          {error && <Alert>{error}</Alert>}
          <Input label="Email" type="email" autoComplete="username" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
          <Input label="Password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
          <Button type="submit" loading={busy}>Sign in</Button>
          <p className="text-xs text-muted">No password yet or forgot it? Ask your provider for a setup link.</p>
        </form>
      </Card>
    </div>
  );
}
