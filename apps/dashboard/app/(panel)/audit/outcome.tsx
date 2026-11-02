import { Badge } from "@/components/ui";
import type { AuditEntry } from "@/lib/api";

export function OutcomeBadge({ outcome }: { outcome: AuditEntry["outcome"] }) {
  const tone = outcome === "success" ? "success" : outcome === "denied" ? "warning" : "danger";
  return <Badge tone={tone}>{outcome}</Badge>;
}
