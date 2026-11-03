import { Badge, Card, PageHeader } from "@/components/ui";

// What exists and what does not. Kept in sync with DEVELOPMENT_STATUS.md.
const phases: { phase: number; title: string; status: "available" | "in-progress" | "planned"; items: string[] }[] = [
  { phase: 1, title: "Foundation", status: "available", items: ["Authentication & sessions", "TOTP two-factor", "Role-based access control", "Audit log", "Panel settings"] },
  { phase: 2, title: "Agent & installer", status: "available", items: ["OSRIVA Agent with OS & hardware detection", "Compatibility engine", "Secure enrolment & mutual-TLS reporting", "Agent as a system service (systemd, OpenRC, rc.d, Windows)", "One-line agent installers", "Control-plane installer with upgrade & rollback"] },
  { phase: 3, title: "Server management", status: "available", items: ["Live metrics (CPU, memory, network, disks)", "Service management (systemd, Windows)", "Processes and logs", "File manager (confined by the agent)", "SSH keys, users, scheduled tasks, hostname & timezone"] },
  { phase: 4, title: "Applications", status: "in-progress", items: ["Docker manager", "Git deployments (Dockerfile, static, Node.js)", "Deployment history & rollback", "Upload and Compose deployments (planned)"] },
  { phase: 5, title: "Websites & domains", status: "in-progress", items: ["Websites & reverse proxy (Caddy)", "SSL: internal CA; Let's Encrypt configured, not yet verified on a public domain", "DNS provider adapters (planned)"] },
  { phase: 6, title: "CMS manager", status: "in-progress", items: ["WordPress install & management (WP-CLI)", "Joomla, Drupal, Ghost, PrestaShop … (planned)", "Staging, cloning & migration (planned)"] },
  { phase: 7, title: "Data", status: "in-progress", items: ["Encrypted, verified backups & restore", "Backup schedules with retention", "Database manager and remote storage (planned)"] },
  { phase: 8, title: "Monitoring & automation", status: "in-progress", items: ["Metrics, rollups & alerts", "Signed webhook and e-mail notifications", "Audit retention", "Automation and silences (planned)"] },
  { phase: 9, title: "Fleet", status: "in-progress", items: ["Server groups", "One task on many servers (batches)", "Durable job queue with retries", "Shared rate limits through Redis (optional)"] },
  { phase: 10, title: "Hosting Provider Mode", status: "planned", items: ["Data centers, sites, racks, nodes", "Resource & IP pools", "Proxmox / libvirt / OpenStack / CloudStack / vSphere adapters", "VPS management"] },
  { phase: 11, title: "Provisioning", status: "planned", items: ["Web hosting backend adapters", "Technical resource templates", "Signed provisioning API & webhooks for external billing systems"] },
  { phase: 12, title: "Customer Panel", status: "planned", items: ["Separate customer dashboard", "Strict tenant isolation", "Service controls"] },
  { phase: 13, title: "Ecosystem", status: "planned", items: ["Plugin SDK", "Third-party adapters", "Developer documentation"] },
  { phase: 14, title: "Release", status: "planned", items: ["Security audit", "Performance & compatibility testing", "Signed releases"] },
];

export default function RoadmapPage() {
  return (
    <>
      <PageHeader
        title="Roadmap"
        description="OSRIVA Panel is built in phases. Planned modules are not shown in the navigation until they actually work."
      />
      <div className="grid gap-4 md:grid-cols-2">
        {phases.map((p) => (
          <Card
            key={p.phase}
            title={
              <span>
                <span className="text-muted">Phase {p.phase} · </span>
                {p.title}
              </span>
            }
            actions={
              p.status === "available" ? <Badge tone="success">Available</Badge> : p.status === "in-progress" ? <Badge tone="accent">In progress</Badge> : <Badge>Planned</Badge>
            }
          >
            <ul className="list-disc space-y-1 pl-5 text-sm text-muted">
              {p.items.map((i) => (
                <li key={i}>{i}</li>
              ))}
            </ul>
          </Card>
        ))}
      </div>
      <p className="mt-6 text-sm text-muted">
        OSRIVA Panel is free and open source (AGPL-3.0). It has no billing or payment features and never will. External billing systems
        integrate through the provisioning API.
      </p>
    </>
  );
}
