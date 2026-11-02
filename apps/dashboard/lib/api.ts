// Typed client for the OSRIVA Core API (same-origin under /api/v1).

export type RoleRef = { key: string; name: string };

export type User = {
  id: string;
  email: string;
  display_name: string;
  status: "active" | "disabled";
  mfa_enabled: boolean;
  roles: RoleRef[];
  last_login_at: string | null;
  created_at: string;
};

export type Me = {
  user: User;
  permissions: string[];
  csrf_token: string;
  panel_mode: "personal" | "provider";
  session: { id: string; expires_at: string; reauthenticated_at: string | null };
};

export type Session = {
  id: string;
  current: boolean;
  ip: string;
  user_agent: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
};

export type AuditEntry = {
  id: number;
  actor_user_id: string | null;
  actor_label: string;
  action: string;
  target_type: string;
  target_id: string;
  outcome: "success" | "failure" | "denied";
  ip: string;
  user_agent: string;
  request_id: string;
  metadata: Record<string, unknown>;
  created_at: string;
};

export type Role = {
  key: string;
  name: string;
  description: string;
  built_in: boolean;
  permissions: string[];
};

export type Meta = { name: string; version: string; api_version: string; license: string };

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public field?: string,
    public requestId?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

let csrfToken: string | null = null;

export function setCsrfToken(token: string | null) {
  csrfToken = token;
}

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export async function api<T>(path: string, opts: { method?: Method; body?: unknown; signal?: AbortSignal } = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrfToken) headers["X-CSRF-Token"] = csrfToken;

  let res: Response;
  try {
    res = await fetch(`/api/v1${path}`, {
      method,
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
      credentials: "same-origin",
      cache: "no-store",
      signal: opts.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError(0, "network_error", "Cannot reach the OSRIVA API. Check that the API service is running.");
  }
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let data: unknown;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    throw new ApiError(res.status, "api_unreachable", `The API returned an unexpected response (HTTP ${res.status}).`);
  }
  if (!res.ok) {
    const body = data as { error?: { code: string; message: string; field?: string }; request_id?: string } | undefined;
    throw new ApiError(
      res.status,
      body?.error?.code ?? "http_error",
      body?.error?.message ?? `Request failed (HTTP ${res.status}).`,
      body?.error?.field,
      body?.request_id,
    );
  }
  return data as T;
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return "Something went wrong.";
}

// ---- Servers (Phase 2) ----

export type Connection = "pending" | "online" | "offline" | "revoked";

export type Server = {
  id: string;
  name: string;
  status: "pending" | "active" | "revoked";
  connection: Connection;
  hostname: string;
  os_family: string;
  os_name: string;
  os_version: string;
  kernel: string;
  arch: string;
  agent_version: string;
  last_seen_at: string | null;
  last_seen_ip: string;
  enrolled_at: string | null;
  created_at: string;
};

export type Enrollment = {
  token: string;
  expires_at: string;
  panel_url: string;
  ca_fingerprint: string;
  command: string;
  install_unix: string;
  install_windows: string;
};

export type CapabilityStatus =
  | "SUPPORTED"
  | "PARTIALLY_SUPPORTED"
  | "UNSUPPORTED"
  | "NOT_TESTED"
  | "DEPENDENCY_MISSING"
  | "REQUIRES_CONFIGURATION";

export type Capability = {
  key: string;
  title: string;
  status: CapabilityStatus;
  reason: string;
  alternatives?: string[];
  phase: number;
};

export type Inventory = {
  schema_version: number;
  collected_at: string;
  hostname: string;
  os: { family: string; id: string; name: string; version: string; kernel: string; arch: string; immutable?: boolean; build?: string };
  cpu: { model: string; logical_cores: number };
  memory: { total_bytes: number; available_bytes: number };
  disks: { mount: string; fs_type: string; device?: string; total_bytes: number; free_bytes: number }[];
  network: { name: string; mac?: string; addresses: string[]; up: boolean }[];
  uptime_seconds: number;
  privileged: boolean;
  init_system: string;
  package_managers: string[] | null;
  tools: { docker: boolean; podman: boolean; git: boolean; systemd: boolean };
  virtualization?: string;
  warnings?: string[];
};

export type ServerDetail = {
  server: Server;
  inventory: Inventory | null;
  capabilities: Capability[];
  inventory_received_at: string | null;
};

export type AgentEndpointInfo = { configured: boolean; agent_endpoint: string; ca_fingerprint: string };

// ---- Tasks and metrics (Phase 3) ----

export type MetricSample = {
  ts: string;
  cpu_percent?: number;
  mem_used_bytes?: number;
  mem_total_bytes?: number;
  swap_used_bytes?: number;
  swap_total_bytes?: number;
  load1?: number;
  load5?: number;
  load15?: number;
  net_rx_bps?: number;
  net_tx_bps?: number;
  disks?: { mount: string; used_bytes: number; total_bytes: number }[];
};

export type TaskStatus = "queued" | "running" | "succeeded" | "failed" | "expired";

export type Task<R = unknown> = {
  id: string;
  type: string;
  params: Record<string, unknown>;
  status: TaskStatus;
  requested_by: string | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  result: R | null;
  error: string;
};

export type ServiceItem = { name: string; description?: string; state: string; detail?: string; startup: string };
export type ServicesResult = { manager: string; services: ServiceItem[] };
export type ProcessItem = { pid: number; ppid: number; name: string; user?: string; rss_bytes?: number; threads?: number; command?: string };
export type ProcessesResult = { processes: ProcessItem[]; truncated?: boolean };

/** Queues a typed task and waits until the agent reports a result. Throws with the agent's error on failure. */
export async function runTask<R>(serverId: string, type: string, params: Record<string, unknown> = {}, rounds = 6): Promise<R> {
  let task = await api<Task<R>>(`/servers/${serverId}/tasks`, { method: "POST", body: { type, params } });
  for (let i = 0; i < rounds && (task.status === "queued" || task.status === "running"); i++) {
    task = await api<Task<R>>(`/servers/${serverId}/tasks/${task.id}?wait=20`);
  }
  if (task.status === "succeeded") return task.result as R;
  if (task.status === "queued" || task.status === "running") {
    throw new ApiError(504, "task_pending", "The agent has not answered yet. Is it online?");
  }
  throw new ApiError(502, `task_${task.status}`, task.error || `Task ${task.status}.`);
}

export type DockerInfo = {
  available: boolean;
  reason?: string;
  version?: string;
  api_version?: string;
  os?: string;
  storage_driver?: string;
  containers: number;
  containers_running: number;
  images: number;
  cpus?: number;
  memory_bytes?: number;
};
export type DockerPort = { ip?: string; private_port: number; public_port?: number; type: string };
export type DockerContainer = {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  created: string;
  ports: DockerPort[];
  compose_project?: string;
  protected?: boolean;
};
export type DockerImage = { id: string; tags: string[]; size: number; created: string; in_use: boolean };
export type DockerVolume = { name: string; driver: string; mountpoint?: string };
export type DockerNetwork = { id: string; name: string; driver: string; scope: string };
export type DockerLogLine = { stream: string; time?: string; text: string };
