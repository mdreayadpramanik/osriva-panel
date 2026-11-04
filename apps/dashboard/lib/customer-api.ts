// Customer Panel API client. Customers have their own session cookie
// (sent only to /api/v1/customer) and their own CSRF token, separate from operators.
import { api } from "./api";

export type CustomerMe = {
  customer: { id: string; email: string; name: string; status: string; last_login_at: string | null };
  csrf_token: string;
};

export type CustomerVPS = {
  id: string;
  name: string;
  ip_address: string;
  vcpus: number;
  memory_mb: number;
  disk_gb: number;
  ssh_keys: string[];
  state: "creating" | "running" | "stopped" | "error" | "deleting";
  pending: string;
  last_operation_failed: boolean;
  created_at: string;
};

let customerCsrf: string | null = null;

export function setCustomerCsrf(t: string | null) {
  customerCsrf = t;
}

type Opts = { method?: "GET" | "POST"; body?: unknown };

export function capi<T>(path: string, opts: Opts = {}): Promise<T> {
  return api<T>(`/customer${path}`, { ...opts, csrf: customerCsrf });
}
