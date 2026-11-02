#!/usr/bin/env node
// Runs the Core API for local development with variables from .env.dev.local
// (falling back to the current environment). Cross-platform; needs Go on PATH
// or in its default install location.
import { spawn } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const envFile = join(root, ".env.dev.local");

const env = { ...process.env };
if (existsSync(envFile)) {
  for (const line of readFileSync(envFile, "utf8").split(/\r?\n/)) {
    const m = line.match(/^\s*([A-Z0-9_]+)\s*=\s*(.*)\s*$/);
    if (m && !line.trimStart().startsWith("#")) env[m[1]] = m[2].replace(/^"(.*)"$/, "$1");
  }
} else {
  console.warn(`[dev-api] ${envFile} not found; using the current environment only (see .env.example)`);
}

const candidates = [env.GO_BIN, "go", "C:\\Program Files\\Go\\bin\\go.exe", "/usr/local/go/bin/go"].filter(Boolean);
const go = candidates.find((c) => c === "go" || existsSync(c));

const child = spawn(go, ["run", "./services/api/cmd/osriva-api", "serve"], { cwd: root, env, stdio: "inherit" });
child.on("error", (err) => {
  console.error(`[dev-api] failed to start Go (${go}): ${err.message}. Set GO_BIN to your go binary.`);
  process.exit(1);
});
child.on("exit", (code) => process.exit(code ?? 1));
for (const sig of ["SIGINT", "SIGTERM"]) process.on(sig, () => child.kill(sig));
