import { Badge } from "@/components/ui";
import type { CapabilityStatus, Connection } from "@/lib/api";

export function ConnectionBadge({ connection }: { connection: Connection }) {
  switch (connection) {
    case "online":
      return <Badge tone="success">Online</Badge>;
    case "offline":
      return <Badge tone="danger">Offline</Badge>;
    case "pending":
      return <Badge tone="warning">Awaiting agent</Badge>;
    case "revoked":
      return <Badge>Revoked</Badge>;
  }
}

const capabilityTone: Record<CapabilityStatus, "success" | "accent" | "warning" | "danger" | "neutral"> = {
  SUPPORTED: "success",
  PARTIALLY_SUPPORTED: "accent",
  NOT_TESTED: "neutral",
  REQUIRES_CONFIGURATION: "warning",
  DEPENDENCY_MISSING: "warning",
  UNSUPPORTED: "danger",
};

export function CapabilityBadge({ status }: { status: CapabilityStatus }) {
  return <Badge tone={capabilityTone[status]}>{status.replaceAll("_", " ")}</Badge>;
}
