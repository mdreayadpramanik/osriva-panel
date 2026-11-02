"use client";

import { useEffect, useState } from "react";

type Pref = "system" | "light" | "dark";
const KEY = "osriva-theme";

function apply(pref: Pref) {
  const dark = pref === "dark" || (pref === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}

export function ThemeToggle() {
  const [pref, setPref] = useState<Pref>("system");

  useEffect(() => {
    let stored: string | null = null;
    try {
      stored = localStorage.getItem(KEY);
    } catch {}
    const initial: Pref = stored === "light" || stored === "dark" ? stored : "system";
    setPref(initial);
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => {
      let current: string | null = null;
      try {
        current = localStorage.getItem(KEY);
      } catch {}
      if (current !== "light" && current !== "dark") apply("system");
    };
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  function choose(next: Pref) {
    setPref(next);
    try {
      if (next === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, next);
    } catch {}
    apply(next);
  }

  return (
    <div role="radiogroup" aria-label="Theme" className="flex rounded-lg border border-line p-0.5 text-xs">
      {(["light", "system", "dark"] as const).map((p) => (
        <button
          key={p}
          role="radio"
          aria-checked={pref === p}
          onClick={() => choose(p)}
          className={
            "flex-1 rounded-md px-2 py-1 capitalize " + (pref === p ? "bg-surface-2 font-medium text-fg" : "text-muted hover:text-fg")
          }
        >
          {p}
        </button>
      ))}
    </div>
  );
}
