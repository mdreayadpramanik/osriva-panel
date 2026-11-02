"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { api, ApiError, errorMessage, setCsrfToken, type Me } from "@/lib/api";
import { Alert, Button, Input } from "@/components/ui";
import { ThemeToggle } from "@/components/theme";

// Only allow same-site relative paths, preventing open redirects via ?next=.
function safeNext(raw: string | null): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\")) return "/";
  return raw;
}

function LoginForm() {
  const router = useRouter();
  const params = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [needsCode, setNeedsCode] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const me = await api<Me>("/auth/login", {
        method: "POST",
        body: { email, password, ...(needsCode ? { totp_code: code } : {}) },
      });
      setCsrfToken(me.csrf_token);
      router.replace(safeNext(params.get("next")));
    } catch (err) {
      if (err instanceof ApiError && err.code === "mfa_required") {
        setNeedsCode(true);
      } else {
        setError(errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate={false}>
      {error && <Alert>{error}</Alert>}
      {!needsCode ? (
        <>
          <Input label="Email" type="email" autoComplete="username" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
          <Input label="Password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </>
      ) : (
        <>
          <Alert tone="accent">Two-factor authentication is enabled for this account.</Alert>
          <Input
            label="Authenticator code"
            hint="Enter the 6-digit code from your authenticator app."
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="[0-9]{6}"
            maxLength={6}
            required
            autoFocus
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
          />
        </>
      )}
      <Button type="submit" loading={busy} className="mt-2 w-full">
        {needsCode ? "Verify and sign in" : "Sign in"}
      </Button>
      {needsCode && (
        <button
          type="button"
          className="text-sm text-muted hover:text-fg"
          onClick={() => {
            setNeedsCode(false);
            setCode("");
          }}
        >
          Use a different account
        </button>
      )}
    </form>
  );
}

export default function LoginPage() {
  return (
    <div className="grid min-h-dvh place-items-center px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div aria-hidden="true" className="mx-auto mb-4 grid size-12 place-items-center rounded-xl bg-accent text-lg font-bold text-accent-fg">
            O
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">Sign in to OSRIVA Panel</h1>
          <p className="mt-1 text-sm text-muted">One Platform. Every Server. Total Control.</p>
        </div>
        <div className="rounded-xl border border-line bg-surface p-6 shadow-sm">
          <Suspense>
            <LoginForm />
          </Suspense>
        </div>
        <p className="mt-6 text-center text-xs text-muted">
          No account yet? An operator with shell access creates the first owner with{" "}
          <code className="font-mono">osriva-api admin create</code>.
        </p>
        <div className="mx-auto mt-6 w-48">
          <ThemeToggle />
        </div>
      </div>
    </div>
  );
}
