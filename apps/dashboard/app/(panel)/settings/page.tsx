"use client";

import { useEffect, useState } from "react";
import { api, errorMessage } from "@/lib/api";
import { useSession } from "@/components/session";
import { Alert, Button, Card, LoadingBlock, PageHeader, cx } from "@/components/ui";

type Mode = "personal" | "provider";

const modes: { value: Mode; title: string; body: string }[] = [
  {
    value: "personal",
    title: "Personal Server Mode",
    body: "For one person or team managing their own VPS or dedicated servers: websites, apps, CMS, databases, Docker and backups.",
  },
  {
    value: "provider",
    title: "Hosting Provider Mode",
    body: "For hosting companies: data centers, nodes, virtualization, VPS and hosting provisioning, plus a separate customer panel. Provider modules arrive in Phases 10–12.",
  },
];

export default function SettingsPage() {
  const { can, privileged, refresh } = useSession();
  const [mode, setMode] = useState<Mode | null>(null);
  const [selected, setSelected] = useState<Mode | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api<{ panel_mode: Mode }>("/settings")
      .then((s) => {
        setMode(s.panel_mode);
        setSelected(s.panel_mode);
      })
      .catch((e) => setError(errorMessage(e)));
  }, []);

  async function save() {
    if (!selected) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const s = await privileged(() => api<{ panel_mode: Mode }>("/settings/panel-mode", { method: "PUT", body: { mode: selected } }));
      setMode(s.panel_mode);
      setSaved(true);
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const writable = can("settings.write");

  return (
    <>
      <PageHeader title="Settings" description="Panel-wide configuration." />
      <Card title="Operating mode" description={writable ? "Changing the mode requires confirming your password." : "Only owners can change the operating mode."}>
        {error && (
          <div className="mb-4">
            <Alert>{error}</Alert>
          </div>
        )}
        {saved && (
          <div className="mb-4">
            <Alert tone="success">Operating mode updated.</Alert>
          </div>
        )}
        {mode === null ? (
          !error && <LoadingBlock />
        ) : (
          <div className="flex flex-col gap-4">
            <div role="radiogroup" aria-label="Operating mode" className="grid gap-3 md:grid-cols-2">
              {modes.map((m) => (
                <button
                  key={m.value}
                  role="radio"
                  aria-checked={selected === m.value}
                  disabled={!writable}
                  onClick={() => setSelected(m.value)}
                  className={cx(
                    "rounded-xl border p-4 text-left transition-colors disabled:cursor-default",
                    selected === m.value ? "border-accent bg-accent-soft" : "border-line hover:bg-surface-2",
                  )}
                >
                  <p className="font-medium">
                    {m.title} {mode === m.value && <span className="text-xs text-muted">(current)</span>}
                  </p>
                  <p className="mt-1 text-sm text-muted">{m.body}</p>
                </button>
              ))}
            </div>
            {writable && (
              <Button className="self-start" loading={busy} disabled={selected === mode} onClick={() => void save()}>
                Save mode
              </Button>
            )}
          </div>
        )}
      </Card>
    </>
  );
}
