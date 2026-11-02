"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { useSession } from "./session";
import { ThemeToggle } from "./theme";
import { Badge, cx } from "./ui";

type NavItem = { href: string; label: string; permission?: string };

// Only modules with a working backend are listed. Everything else is on the Roadmap page.
const nav: NavItem[] = [
  { href: "/", label: "Overview" },
  { href: "/servers", label: "Servers", permission: "servers.read" },
  { href: "/users", label: "Users & Permissions", permission: "users.read" },
  { href: "/audit", label: "Audit Log", permission: "audit.read" },
  { href: "/settings", label: "Settings", permission: "settings.read" },
  { href: "/account", label: "My Account" },
  { href: "/roadmap", label: "Roadmap" },
];

function Logo() {
  return (
    <Link href="/" className="flex items-center gap-2.5 px-2">
      <span aria-hidden="true" className="grid size-8 place-items-center rounded-lg bg-accent text-sm font-bold text-accent-fg">
        O
      </span>
      <span className="leading-tight">
        <span className="block text-sm font-semibold tracking-tight">OSRIVA Panel</span>
        <span className="block text-[11px] text-muted">Total Control</span>
      </span>
    </Link>
  );
}

export function Shell({ children }: { children: React.ReactNode }) {
  const { me, can, logout } = useSession();
  const pathname = usePathname();
  const [open, setOpen] = useState(false);

  useEffect(() => setOpen(false), [pathname]);

  const items = nav.filter((i) => !i.permission || can(i.permission));
  const isActive = (href: string) => (href === "/" ? pathname === "/" : pathname.startsWith(href));

  const sidebar = (
    <div className="flex h-full flex-col gap-6 p-4">
      <Logo />
      <div className="px-2">
        <Badge tone="accent">{me.panel_mode === "provider" ? "Hosting Provider Mode" : "Personal Server Mode"}</Badge>
      </div>
      <nav aria-label="Main" className="flex flex-1 flex-col gap-0.5">
        {items.map((i) => (
          <Link
            key={i.href}
            href={i.href}
            aria-current={isActive(i.href) ? "page" : undefined}
            className={cx(
              "rounded-lg px-3 py-2 text-sm",
              isActive(i.href) ? "bg-accent-soft font-medium text-accent" : "text-muted hover:bg-surface-2 hover:text-fg",
            )}
          >
            {i.label}
          </Link>
        ))}
      </nav>
      <div className="flex flex-col gap-3 border-t border-line pt-4">
        <ThemeToggle />
        <div className="px-1">
          <p className="truncate text-sm font-medium">{me.user.display_name}</p>
          <p className="truncate text-xs text-muted">{me.user.email}</p>
        </div>
        <button onClick={() => void logout()} className="rounded-lg px-3 py-2 text-left text-sm text-muted hover:bg-surface-2 hover:text-fg">
          Sign out
        </button>
      </div>
    </div>
  );

  return (
    <div className="min-h-dvh lg:grid lg:grid-cols-[16rem_1fr]">
      <aside className="sticky top-0 hidden h-dvh border-r border-line bg-surface lg:block">{sidebar}</aside>

      <header className="sticky top-0 z-20 flex items-center justify-between border-b border-line bg-surface px-4 py-3 lg:hidden">
        <Logo />
        <button
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          aria-controls="mobile-nav"
          className="rounded-lg border border-line px-3 py-1.5 text-sm"
        >
          {open ? "Close" : "Menu"}
        </button>
      </header>
      {open && (
        <div id="mobile-nav" className="fixed inset-x-0 bottom-0 top-[57px] z-10 overflow-y-auto bg-surface lg:hidden">
          {sidebar}
        </div>
      )}

      <main className="min-w-0 px-4 py-6 sm:px-6 lg:px-10 lg:py-8">
        <div className="mx-auto max-w-6xl">{children}</div>
      </main>
    </div>
  );
}
