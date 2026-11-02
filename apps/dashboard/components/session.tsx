"use client";

import { usePathname, useRouter } from "next/navigation";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, errorMessage, setCsrfToken, type Me } from "@/lib/api";
import { Alert, Button, Input, LoadingBlock, Modal } from "./ui";

type SessionCtx = {
  me: Me;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
  can: (permission: string) => boolean;
  /** Runs fn; if the API demands re-authentication, prompts for the password and retries once. */
  privileged: <T>(fn: () => Promise<T>) => Promise<T>;
};

const Ctx = createContext<SessionCtx | null>(null);

export function useSession(): SessionCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error("useSession must be used inside <SessionProvider>");
  return v;
}

export function SessionProvider({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [me, setMe] = useState<Me | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const m = await api<Me>("/auth/me");
      setCsrfToken(m.csrf_token);
      setMe(m);
      setLoadError(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setCsrfToken(null);
        router.replace(`/login?next=${encodeURIComponent(pathname || "/")}`);
        return;
      }
      setLoadError(errorMessage(e));
    }
  }, [router, pathname]);

  // Load once on mount; navigation inside the panel keeps the session.
  const loaded = useRef(false);
  useEffect(() => {
    if (loaded.current) return;
    loaded.current = true;
    void load();
  }, [load]);

  // Re-auth prompt state; the pending promise resolves when the user confirms.
  const [reauthOpen, setReauthOpen] = useState(false);
  const pending = useRef<{ resolve: () => void; reject: (e: unknown) => void } | null>(null);

  const privileged = useCallback(async <T,>(fn: () => Promise<T>): Promise<T> => {
    try {
      return await fn();
    } catch (e) {
      if (!(e instanceof ApiError && e.code === "reauth_required")) throw e;
      await new Promise<void>((resolve, reject) => {
        pending.current = { resolve, reject };
        setReauthOpen(true);
      });
      return fn();
    }
  }, []);

  const logout = useCallback(async () => {
    try {
      await api("/auth/logout", { method: "POST" });
    } finally {
      setCsrfToken(null);
      router.replace("/login");
    }
  }, [router]);

  // Stable identity so consumers can list these in effect dependencies.
  const value = useMemo<SessionCtx | null>(
    () => (me ? { me, refresh: load, logout, can: (p) => me.permissions.includes(p), privileged } : null),
    [me, load, logout, privileged],
  );

  if (loadError) {
    return (
      <div className="mx-auto max-w-lg p-8">
        <Alert title="Cannot load your session">{loadError}</Alert>
        <Button className="mt-4" variant="secondary" onClick={() => void load()}>
          Retry
        </Button>
      </div>
    );
  }
  if (!me || !value) return <LoadingBlock label="Loading OSRIVA Panel…" />;

  return (
    <Ctx.Provider value={value}>
      {children}
      <ReauthDialog
        open={reauthOpen}
        mfa={me.user.mfa_enabled}
        onDone={(ok) => {
          setReauthOpen(false);
          const p = pending.current;
          pending.current = null;
          if (ok) p?.resolve();
          else p?.reject(new ApiError(403, "reauth_cancelled", "Confirmation cancelled."));
        }}
      />
    </Ctx.Provider>
  );
}

function ReauthDialog({ open, mfa, onDone }: { open: boolean; mfa: boolean; onDone: (ok: boolean) => void }) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setPassword("");
      setCode("");
      setError(null);
    }
  }, [open]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api("/auth/reauth", { method: "POST", body: { password, totp_code: code } });
      onDone(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal open={open} onClose={() => onDone(false)} title="Confirm it's you">
      <form onSubmit={submit} className="flex flex-col gap-4">
        <p className="text-sm text-muted">This action is sensitive. Enter your password to continue.</p>
        {error && <Alert>{error}</Alert>}
        <Input label="Password" type="password" autoComplete="current-password" required autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
        {mfa && (
          <Input label="Authenticator code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} required value={code} onChange={(e) => setCode(e.target.value)} />
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => onDone(false)}>
            Cancel
          </Button>
          <Button type="submit" loading={busy}>
            Confirm
          </Button>
        </div>
      </form>
    </Modal>
  );
}
