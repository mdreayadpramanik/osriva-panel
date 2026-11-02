"use client";

import { useCallback, useEffect, useState } from "react";
import { errorMessage, runTask } from "@/lib/api";
import { formatBytes, formatDate } from "@/lib/format";
import { useSession } from "@/components/session";
import { Alert, Badge, Button, EmptyState, Input, LoadingBlock, Modal, Table, Td, Th } from "@/components/ui";

type Entry = { name: string; type: string; size: number; mode: string; mod_time: string; denied?: boolean };
type Listing = { path: string; parent?: string; entries: Entry[]; roots: string[] };
type FileContent = { path: string; size: number; content: string; binary: boolean; truncated: boolean; mod_time: string };

const MAX_EDIT = 512 * 1024;

function join(dir: string, name: string) {
  const sep = dir.includes("\\") ? "\\" : "/";
  return dir.endsWith(sep) ? dir + name : dir + sep + name;
}

export function FilesPanel({ serverId, online, osFamily }: { serverId: string; online: boolean; osFamily: string }) {
  const { privileged } = useSession();
  const [listing, setListing] = useState<Listing | null>(null);
  const [path, setPath] = useState(osFamily === "windows" ? "C:\\ProgramData" : "/etc");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [open, setOpen] = useState<FileContent | null>(null);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const [creating, setCreating] = useState<"file" | "dir" | null>(null);
  const [newName, setNewName] = useState("");

  const list = useCallback(
    async (p: string) => {
      setLoading(true);
      setError(null);
      try {
        const l = await runTask<Listing>(serverId, "files.list", { path: p });
        setListing(l);
        setPath(l.path);
      } catch (e) {
        setError(errorMessage(e));
      } finally {
        setLoading(false);
      }
    },
    [serverId],
  );

  useEffect(() => {
    if (online) void list(path);
    // Initial listing only; navigation calls list() directly.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [online, list]);

  async function openFile(name: string) {
    setError(null);
    setNotice(null);
    try {
      const f = await runTask<FileContent>(serverId, "files.read", { path: join(path, name) });
      setOpen(f);
      setDraft(f.content);
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function save() {
    if (!open) return;
    setSaving(true);
    setError(null);
    try {
      await privileged(() => runTask(serverId, "files.write", { path: open.path, content: draft }));
      setNotice(`Saved ${open.path}.`);
      setOpen(null);
      void list(path);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSaving(false);
    }
  }

  async function create() {
    if (!creating || !newName.trim()) return;
    const target = join(path, newName.trim());
    setError(null);
    try {
      if (creating === "dir") await privileged(() => runTask(serverId, "files.mkdir", { path: target }));
      else await privileged(() => runTask(serverId, "files.write", { path: target, content: "", create: true }));
      setNotice(`Created ${target}.`);
      setCreating(null);
      setNewName("");
      void list(path);
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function remove(e: Entry) {
    const target = join(path, e.name);
    if (!confirm(`Delete ${target}? This cannot be undone.${e.type === "dir" ? " Only empty folders can be deleted." : ""}`)) return;
    setError(null);
    try {
      await privileged(() => runTask(serverId, "files.delete", { path: target }));
      setNotice(`Deleted ${target}.`);
      void list(path);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  if (!online && !listing) return <EmptyState title="Agent offline">Files can only be browsed while the agent is online.</EmptyState>;

  return (
    <div className="flex flex-col gap-4">
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void list(path);
        }}
      >
        <input aria-label="Path" value={path} onChange={(e) => setPath(e.target.value)} className="h-9 min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 font-mono text-sm" />
        <Button size="sm" type="submit" variant="secondary" loading={loading}>
          Go
        </Button>
        {listing?.parent && (
          <Button size="sm" type="button" variant="ghost" onClick={() => void list(listing.parent!)}>
            ↑ Up
          </Button>
        )}
        <Button size="sm" type="button" variant="ghost" onClick={() => setCreating("file")}>
          New file
        </Button>
        <Button size="sm" type="button" variant="ghost" onClick={() => setCreating("dir")}>
          New folder
        </Button>
      </form>
      {listing && (
        <p className="text-xs text-muted">
          Allowed directories on this server:{" "}
          {listing.roots.map((r) => (
            <button key={r} className="mr-2 font-mono text-accent hover:underline" onClick={() => void list(r)}>
              {r}
            </button>
          ))}
          (set by <code className="font-mono">file_roots</code> in the agent&apos;s own configuration; the panel cannot widen it)
        </p>
      )}
      {error && <Alert>{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {!listing ? (
        loading ? <LoadingBlock label="Listing…" /> : null
      ) : listing.entries.length === 0 ? (
        <EmptyState title="Empty folder" />
      ) : (
        <Table>
          <thead>
            <tr>
              <Th>Name</Th>
              <Th>Size</Th>
              <Th>Permissions</Th>
              <Th>Modified</Th>
              <Th className="text-right">
                <span className="sr-only">Actions</span>
              </Th>
            </tr>
          </thead>
          <tbody>
            {listing.entries.map((e) => (
              <tr key={e.name}>
                <Td>
                  {e.denied ? (
                    <span className="font-mono text-xs text-muted">
                      {e.name} <Badge>protected</Badge>
                    </span>
                  ) : e.type === "dir" ? (
                    <button className="font-mono text-xs font-medium text-accent hover:underline" onClick={() => void list(join(path, e.name))}>
                      {e.name}/
                    </button>
                  ) : e.type === "file" ? (
                    <button className="font-mono text-xs hover:underline" onClick={() => void openFile(e.name)}>
                      {e.name}
                    </button>
                  ) : (
                    <span className="font-mono text-xs text-muted">
                      {e.name} <Badge>{e.type}</Badge>
                    </span>
                  )}
                </Td>
                <Td className="tabular-nums text-muted">{e.type === "file" ? formatBytes(e.size) : "—"}</Td>
                <Td className="font-mono text-xs text-muted">{e.mode}</Td>
                <Td className="whitespace-nowrap text-muted">{formatDate(e.mod_time)}</Td>
                <Td className="text-right">
                  {!e.denied && (e.type === "file" || e.type === "dir") && (
                    <Button size="sm" variant="ghost" onClick={() => void remove(e)}>
                      Delete
                    </Button>
                  )}
                </Td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      <Modal
        open={!!open}
        onClose={() => setOpen(null)}
        title={open?.path ?? ""}
        footer={
          open && !open.binary && !open.truncated ? (
            <>
              <Button variant="secondary" onClick={() => setOpen(null)}>
                Cancel
              </Button>
              <Button loading={saving} onClick={() => void save()} disabled={draft === open.content || draft.length > MAX_EDIT}>
                Save
              </Button>
            </>
          ) : (
            <Button variant="secondary" onClick={() => setOpen(null)}>
              Close
            </Button>
          )
        }
      >
        {open &&
          (open.binary ? (
            <p className="text-sm text-muted">This is a binary file ({formatBytes(open.size)}). Only text files can be viewed and edited.</p>
          ) : (
            <div className="flex flex-col gap-2">
              {open.truncated && <Alert tone="warning">Only the first 512 KB are shown. Files this large cannot be edited here.</Alert>}
              <textarea
                aria-label="File content"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                readOnly={open.truncated}
                spellCheck={false}
                className="h-[50vh] w-full rounded-lg border border-line bg-surface-2 p-3 font-mono text-xs"
              />
              <p className="text-xs text-muted">
                {formatBytes(open.size)} · modified {formatDate(open.mod_time)} · saving replaces the file atomically and asks for your password
              </p>
            </div>
          ))}
      </Modal>

      <Modal open={!!creating} onClose={() => setCreating(null)} title={creating === "dir" ? "New folder" : "New file"}>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <Input label={`Name (inside ${path})`} value={newName} onChange={(e) => setNewName(e.target.value)} required autoFocus pattern="[^/\\]+" />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={() => setCreating(null)}>
              Cancel
            </Button>
            <Button type="submit">Create</Button>
          </div>
        </form>
      </Modal>
    </div>
  );
}
