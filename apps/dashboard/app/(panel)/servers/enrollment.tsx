"use client";

import { useState } from "react";
import type { Enrollment } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { Alert, Button } from "@/components/ui";

// Shows a one-time enrolment command. The token is never retrievable again.
export function EnrollmentInstructions({ enrollment }: { enrollment: Enrollment }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(enrollment.command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="flex flex-col gap-3 text-sm">
      <Alert tone="warning" title="Copy this command now">
        The token is shown only once and expires {formatDate(enrollment.expires_at)}. Anyone with it can enrol a machine as this server.
      </Alert>
      <ol className="list-decimal space-y-2 pl-5 text-muted">
        <li>Copy the <code className="font-mono">osriva-agent</code> binary for the server&apos;s OS to the server (the Phase 2b installer will automate this).</li>
        <li>Run this command on the server as root / Administrator:</li>
      </ol>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-surface-2 p-3 font-mono text-xs text-fg">{enrollment.command}</pre>
      <div className="flex items-center gap-3">
        <Button size="sm" variant="secondary" onClick={() => void copy()}>
          {copied ? "Copied" : "Copy command"}
        </Button>
        <span className="text-xs text-muted">
          Then start it with <code className="font-mono">osriva-agent run</code>.
        </span>
      </div>
      <p className="text-xs text-muted">
        The agent checks that the panel&apos;s CA matches <code className="break-all font-mono">{enrollment.ca_fingerprint}</code> before it sends the
        token.
      </p>
    </div>
  );
}
