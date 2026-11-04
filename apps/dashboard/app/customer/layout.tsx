import type { Metadata } from "next";

export const metadata: Metadata = { title: { default: "Customer Panel", template: "%s · Customer Panel" } };

// The Customer Panel has its own session and never loads the operator shell.
export default function CustomerLayout({ children }: { children: React.ReactNode }) {
  return <div className="mx-auto flex min-h-dvh w-full max-w-4xl flex-col px-4 py-8">{children}</div>;
}
