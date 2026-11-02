"use client";

import { useState } from "react";
import type { Enrollment } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { Alert, Button, cx } from "@/components/ui";

type Tab = "unix" | "windows" | "manual";

function CopyBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  }
  return (
    <div className="flex flex-col gap-2">
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-surface-2 p-3 font-mono text-xs text-fg">{text}</pre>
      <Button size="sm" variant="secondary" className="self-start" onClick={() => void copy()}>
        {copied ? "Copied" : "Copy command"}
      </Button>
    </div>
  );
}

// Shows a one-time enrolment. The token is never retrievable again.
export function EnrollmentInstructions({ enrollment }: { enrollment: Enrollment }) {
  const tabs: { key: Tab; label: string; available: boolean }[] = [
    { key: "unix", label: "Linux / BSD", available: !!enrollment.install_unix },
    { key: "windows", label: "Windows", available: !!enrollment.install_windows },
    { key: "manual", label: "Manual", available: true },
  ];
  const [tab, setTab] = useState<Tab>(tabs.find((t) => t.available)?.key ?? "manual");

  return (
    <div className="flex flex-col gap-3 text-sm">
      <Alert tone="warning" title="Copy this now">
        The token is shown only once and expires {formatDate(enrollment.expires_at)}. Anyone with it can enrol a machine as this server.
      </Alert>

      <div role="tablist" aria-label="Server operating system" className="flex gap-1 rounded-lg border border-line p-0.5">
        {tabs
          .filter((t) => t.available)
          .map((t) => (
            <button
              key={t.key}
              role="tab"
              aria-selected={tab === t.key}
              onClick={() => setTab(t.key)}
              className={cx("flex-1 rounded-md px-3 py-1.5 text-xs", tab === t.key ? "bg-surface-2 font-medium text-fg" : "text-muted hover:text-fg")}
            >
              {t.label}
            </button>
          ))}
      </div>

      {tab === "unix" && (
        <>
          <p className="text-muted">Run on the server as a user with sudo. It downloads the agent from this panel, checks its SHA-256, enrols and installs a systemd/OpenRC/rc.d service:</p>
          <CopyBlock text={enrollment.install_unix} />
        </>
      )}
      {tab === "windows" && (
        <>
          <p className="text-muted">Run in an elevated PowerShell (Run as Administrator). It downloads the agent, checks its SHA-256, enrols and installs a Windows service:</p>
          <CopyBlock text={enrollment.install_windows} />
        </>
      )}
      {tab === "manual" && (
        <>
          <p className="text-muted">
            If you copied the <code className="font-mono">osriva-agent</code> binary yourself, run this as root / Administrator, then{" "}
            <code className="font-mono">osriva-agent service install</code>:
          </p>
          <CopyBlock text={enrollment.command} />
        </>
      )}

      <p className="text-xs text-muted">
        The installers change nothing else on the server (no firewall, SSH or network settings). The agent checks that the panel&apos;s CA matches{" "}
        <code className="break-all font-mono">{enrollment.ca_fingerprint}</code> before it sends the token.
      </p>
    </div>
  );
}
