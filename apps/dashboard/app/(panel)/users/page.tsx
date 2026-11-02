"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, type Role, type User } from "@/lib/api";
import { formatDate, relativeTime } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, Card, EmptyState, Input, LoadingBlock, Modal, PageHeader, Table, Td, Th } from "@/components/ui";

export default function UsersPage() {
  const { can, me } = useSession();
  const [users, setUsers] = useState<User[] | null>(null);
  const [total, setTotal] = useState(0);
  const [roles, setRoles] = useState<Role[]>([]);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<User | null>(null);

  const load = useCallback(async (q: string) => {
    try {
      const r = await api<{ users: User[]; total: number }>(`/users?limit=200&q=${encodeURIComponent(q)}`);
      setUsers(r.users);
      setTotal(r.total);
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, []);

  useEffect(() => {
    const t = setTimeout(() => void load(query), 250);
    return () => clearTimeout(t);
  }, [query, load]);

  useEffect(() => {
    if (can("roles.read")) api<{ roles: Role[] }>("/roles").then((r) => setRoles(r.roles)).catch(() => {});
  }, [can]);

  const canWrite = can("users.write");

  return (
    <>
      <PageHeader
        title="Users & Permissions"
        description="People who can sign in to this panel and what they are allowed to do."
        actions={canWrite && <Button onClick={() => setCreating(true)}>New user</Button>}
      />
      {error && (
        <div className="mb-4">
          <Alert>{error}</Alert>
        </div>
      )}

      <Card
        title={`${total} ${total === 1 ? "user" : "users"}`}
        actions={<input aria-label="Search users" placeholder="Search name or email" value={query} onChange={(e) => setQuery(e.target.value)} className="h-9 w-56 rounded-lg border border-line bg-surface px-3 text-sm" />}
      >
        {users === null ? (
          <LoadingBlock />
        ) : users.length === 0 ? (
          <EmptyState title="No users match your search" />
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>User</Th>
                <Th>Roles</Th>
                <Th>Status</Th>
                <Th>2FA</Th>
                <Th>Last sign-in</Th>
                <Th className="text-right">
                  <span className="sr-only">Actions</span>
                </Th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <Td>
                    <p className="font-medium">
                      {u.display_name} {u.id === me.user.id && <span className="text-xs text-muted">(you)</span>}
                    </p>
                    <p className="text-xs text-muted">{u.email}</p>
                  </Td>
                  <Td>
                    <div className="flex flex-wrap gap-1">
                      {u.roles.map((r) => (
                        <Badge key={r.key} tone={r.key === "owner" ? "accent" : "neutral"}>
                          {r.name}
                        </Badge>
                      ))}
                    </div>
                  </Td>
                  <Td>{u.status === "active" ? <Badge tone="success">Active</Badge> : <Badge tone="danger">Disabled</Badge>}</Td>
                  <Td>{u.mfa_enabled ? <Badge tone="success">On</Badge> : <Badge>Off</Badge>}</Td>
                  <Td className="text-muted" >
                    <span title={formatDate(u.last_login_at)}>{relativeTime(u.last_login_at)}</span>
                  </Td>
                  <Td className="text-right">
                    {canWrite && (
                      <Button size="sm" variant="secondary" onClick={() => setEditing(u)}>
                        Manage
                      </Button>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      {roles.length > 0 && (
        <Card title="Roles" description="Built-in roles. Each role grants the listed permissions." className="mt-6">
          <div className="grid gap-4 md:grid-cols-2">
            {roles.map((r) => (
              <div key={r.key} className="rounded-lg border border-line p-4">
                <p className="font-medium">{r.name}</p>
                <p className="mt-0.5 text-sm text-muted">{r.description}</p>
                <div className="mt-3 flex flex-wrap gap-1">
                  {r.permissions.length === 0 ? (
                    <span className="text-xs text-muted">Own account only</span>
                  ) : (
                    r.permissions.map((p) => (
                      <code key={p} className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px]">
                        {p}
                      </code>
                    ))
                  )}
                </div>
              </div>
            ))}
          </div>
        </Card>
      )}

      <CreateUserModal open={creating} roles={roles} onClose={() => setCreating(false)} onCreated={() => void load(query)} />
      <ManageUserModal user={editing} roles={roles} onClose={() => setEditing(null)} onSaved={() => void load(query)} />
    </>
  );
}

// Roles the current user may grant: those whose permissions they fully hold.
function useGrantableRoles(roles: Role[]) {
  const { me } = useSession();
  return roles.filter((r) => r.permissions.every((p) => me.permissions.includes(p)));
}

function RolePicker({ roles, value, onChange }: { roles: Role[]; value: string[]; onChange: (v: string[]) => void }) {
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1 text-sm font-medium">Roles</legend>
      {roles.map((r) => (
        <label key={r.key} className="flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            className="mt-1"
            checked={value.includes(r.key)}
            onChange={(e) => onChange(e.target.checked ? [...value, r.key] : value.filter((k) => k !== r.key))}
          />
          <span>
            <span className="font-medium">{r.name}</span>
            <span className="block text-xs text-muted">{r.description}</span>
          </span>
        </label>
      ))}
    </fieldset>
  );
}

function CreateUserModal({ open, roles, onClose, onCreated }: { open: boolean; roles: Role[]; onClose: () => void; onCreated: () => void }) {
  const grantable = useGrantableRoles(roles);
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [selected, setSelected] = useState<string[]>(["member"]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setEmail("");
      setName("");
      setPassword("");
      setSelected(["member"]);
      setError(null);
    }
  }, [open]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api("/users", { method: "POST", body: { email, display_name: name, password, roles: selected } });
      onCreated();
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New user">
      <form onSubmit={submit} className="flex flex-col gap-4">
        {error && <Alert>{error}</Alert>}
        <Input label="Email" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="off" />
        <Input label="Display name" required value={name} onChange={(e) => setName(e.target.value)} />
        <Input
          label="Initial password"
          type="password"
          required
          minLength={12}
          hint="At least 12 characters. Share it securely; the user should change it after signing in."
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
        />
        {grantable.length > 0 && <RolePicker roles={grantable} value={selected} onChange={setSelected} />}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={selected.length === 0}>
            Create user
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function ManageUserModal({ user, roles, onClose, onSaved }: { user: User | null; roles: Role[]; onClose: () => void; onSaved: () => void }) {
  const { me, can, privileged } = useSession();
  const grantable = useGrantableRoles(roles);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    if (user) {
      setName(user.display_name);
      setSelected(user.roles.map((r) => r.key));
      setError(null);
      setNotice(null);
    }
  }, [user]);

  if (!user) return <Modal open={false} onClose={onClose} title="Manage user">{null}</Modal>;
  const self = user.id === me.user.id;
  const rolesChanged = [...selected].sort().join() !== user.roles.map((r) => r.key).sort().join();

  async function run(label: string, fn: () => Promise<unknown>, done: string) {
    setBusy(label);
    setError(null);
    setNotice(null);
    try {
      await privileged(fn);
      setNotice(done);
      onSaved();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  const id = user.id;
  const patch = (body: object) => api(`/users/${id}`, { method: "PATCH", body });

  return (
    <Modal
      open
      onClose={onClose}
      title={`Manage ${user.display_name}`}
      footer={
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex flex-col gap-5">
        <p className="text-sm text-muted">{user.email}</p>
        {error && <Alert>{error}</Alert>}
        {notice && <Alert tone="success">{notice}</Alert>}

        <form
          className="flex items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void run("name", () => patch({ display_name: name }), "Display name updated.");
          }}
        >
          <Input label="Display name" className="flex-1" value={name} onChange={(e) => setName(e.target.value)} required />
          <Button type="submit" variant="secondary" loading={busy === "name"} disabled={name === user.display_name}>
            Save
          </Button>
        </form>

        {grantable.length > 0 && (
          <div className="flex flex-col gap-3">
            <RolePicker roles={grantable} value={selected} onChange={setSelected} />
            <Button
              variant="secondary"
              className="self-start"
              loading={busy === "roles"}
              disabled={!rolesChanged || selected.length === 0}
              onClick={() => void run("roles", () => patch({ roles: selected }), "Roles updated.")}
            >
              Save roles
            </Button>
          </div>
        )}

        <div className="flex flex-wrap gap-2 border-t border-line pt-4">
          {!self &&
            (user.status === "active" ? (
              <Button
                variant="danger"
                loading={busy === "status"}
                onClick={() => {
                  if (confirm(`Disable ${user.email}? They will be signed out everywhere immediately.`))
                    void run("status", () => patch({ status: "disabled" }), "User disabled and signed out.");
                }}
              >
                Disable user
              </Button>
            ) : (
              <Button variant="secondary" loading={busy === "status"} onClick={() => void run("status", () => patch({ status: "active" }), "User re-enabled.")}>
                Re-enable user
              </Button>
            ))}
          {can("sessions.manage_all") && !self && (
            <Button
              variant="secondary"
              loading={busy === "sessions"}
              onClick={() =>
                void run("sessions", () => api(`/users/${id}/sessions/revoke`, { method: "POST" }), "All of this user's sessions were revoked.")
              }
            >
              Sign out everywhere
            </Button>
          )}
        </div>
        <p className="text-xs text-muted">Created {formatDate(user.created_at)}</p>
      </div>
    </Modal>
  );
}
