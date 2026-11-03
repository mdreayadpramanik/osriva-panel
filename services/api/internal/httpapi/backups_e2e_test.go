package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentclient"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// finishCreate answers the next backup.create task like an agent would.
func finishCreate(t *testing.T, ctx context.Context, client *agentclient.Client) agenttask.BackupCreateParams {
	t.Helper()
	ts, err := client.PollTasks(ctx)
	if err != nil || len(ts) != 1 || ts[0].Type != agenttask.BackupCreate {
		t.Fatalf("poll: %+v %v", ts, err)
	}
	var p agenttask.BackupCreateParams
	_ = json.Unmarshal(ts[0].Params, &p)
	res, _ := json.Marshal(agenttask.BackupCreateResult{ID: p.ID, Kind: p.Kind, Target: p.Target, Size: 42,
		SHA256: strings.Repeat("a", 64), PlainSHA256: strings.Repeat("b", 64), Files: 3})
	if err := client.SendResult(ctx, ts[0].ID, agentproto.TaskResult{Status: "succeeded", Result: res}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBackupsKeysAndSchedules(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("bk-1"))
	id := res.Response.ServerID
	ctx := context.Background()
	auditor := e.principal("auditor-bk@example.com", rbac.RoleAuditor)

	if _, err := e.fleet.CreateBackup(ctx, auditor, id, "volume", "data"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor created a backup: %v", err)
	}
	var ve *identity.ValidationError
	if _, err := e.fleet.CreateBackup(ctx, e.owner, id, "disk", "/dev/sda"); !errors.As(err, &ve) {
		t.Fatalf("bad kind: %v", err)
	}
	b, err := e.fleet.CreateBackup(ctx, e.owner, id, "volume", "data")
	if err != nil || b.Status != store.TaskQueued {
		t.Fatalf("create: %+v %v", b, err)
	}
	var pe *identity.PolicyError
	if _, err := e.fleet.RestoreBackup(ctx, e.owner, id, b.ID); !errors.As(err, &pe) {
		t.Fatalf("restore of an unfinished backup: %v", err)
	}
	p := finishCreate(t, ctx, client)
	if !strings.HasPrefix(p.Recipient, "age1") || p.ID != b.ID {
		t.Fatalf("agent got: %+v", p)
	}
	list, _ := e.fleet.ListBackups(ctx, auditor, id)
	if len(list.Backups) != 1 || list.Backups[0].SHA256 == nil || *list.Backups[0].Size != 42 || list.Backups[0].Status != store.TaskSucceeded {
		t.Fatalf("list after completion: %+v", list.Backups)
	}

	// verify sends the identity, which is gone from storage once the task ends
	vt, err := e.fleet.VerifyBackup(ctx, e.owner, id, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := client.PollTasks(ctx)
	if !strings.Contains(string(ts[0].Params), "AGE-SECRET-KEY-1") {
		t.Fatalf("agent needs the key to verify: %s", ts[0].Params)
	}
	ok, _ := json.Marshal(agenttask.BackupVerifyResult{ID: b.ID, OK: true})
	_ = client.SendResult(ctx, vt.ID, agentproto.TaskResult{Status: "succeeded", Result: ok})
	if task, _ := e.store().GetTask(ctx, vt.ID); strings.Contains(string(task.Params), "AGE-SECRET-KEY") {
		t.Fatalf("backup key kept at rest: %s", task.Params)
	}

	stale := *e.owner
	sess := *e.owner.Session
	old := time.Now().Add(-time.Hour)
	sess.ReauthenticatedAt = &old
	stale.Session = &sess
	if _, err := e.fleet.RestoreBackup(ctx, &stale, id, b.ID); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("restore without reauth: %v", err)
	}
	if _, err := e.fleet.ExportRecoveryKey(ctx, &stale, id); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("key export without reauth: %v", err)
	}
	rt, err := e.fleet.RestoreBackup(ctx, e.owner, id, b.ID)
	if err != nil || !strings.Contains(string(rt.Params), `"target":"data"`) {
		t.Fatalf("restore: %+v %v", rt, err)
	}
	if ts, _ := client.PollTasks(ctx); len(ts) != 1 || ts[0].Type != agenttask.BackupRestore {
		t.Fatalf("restore task: %+v", ts)
	}

	// the exported recovery key decrypts what the server's recipient encrypted
	key, err := e.fleet.ExportRecoveryKey(ctx, e.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := age.ParseX25519Identity(key)
	if err != nil || ident.Recipient().String() != p.Recipient {
		t.Fatalf("recovery key does not match the recipient: %v", err)
	}
	rcp, _ := age.ParseX25519Recipient(p.Recipient)
	var buf bytes.Buffer
	w, _ := age.Encrypt(&buf, rcp)
	_, _ = io.WriteString(w, "hello")
	_ = w.Close()
	r, err := age.Decrypt(&buf, ident)
	if out, _ := io.ReadAll(r); err != nil || string(out) != "hello" {
		t.Fatalf("decrypt with recovery key: %v", err)
	}
	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, Limit: 500})
	for _, a := range audit {
		if raw, _ := json.Marshal(a.Metadata); strings.Contains(string(raw), "AGE-SECRET-KEY") {
			t.Fatalf("key in audit: %s %s", a.Action, raw)
		}
	}

	// schedules: due policies start backups, old ones are pruned
	pol, err := e.fleet.CreateBackupPolicy(ctx, e.owner, id, "wordpress", "blog", 24, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.fleet.CreateBackupPolicy(ctx, e.owner, id, "wordpress", "blog", 6, 3); !errors.As(err, &ve) {
		t.Fatalf("second schedule for the same target: %v", err)
	}
	now := time.Now()
	e.fleet.RunBackupSchedule(ctx, now)
	finishCreate(t, ctx, client)
	e.fleet.RunBackupSchedule(ctx, now.Add(time.Hour)) // not due yet
	if ts, _ := e.store().ListTasks(ctx, id, 100); countType(ts, agenttask.BackupCreate) != 2 {
		t.Fatalf("backup started before it was due: %d create tasks", countType(ts, agenttask.BackupCreate))
	}
	e.fleet.RunBackupSchedule(ctx, now.Add(25*time.Hour))
	second := finishCreate(t, ctx, client)
	e.fleet.RunBackupSchedule(ctx, now.Add(26*time.Hour)) // prune: keep 1
	ts, _ = client.PollTasks(ctx)
	if len(ts) != 1 || ts[0].Type != agenttask.BackupDelete || strings.Contains(string(ts[0].Params), second.ID) {
		t.Fatalf("pruning must delete the older scheduled backup only: %+v", ts)
	}
	list, _ = e.fleet.ListBackups(ctx, e.owner, id)
	scheduled := 0
	for _, x := range list.Backups {
		if x.PolicyID != nil && *x.PolicyID == pol.ID {
			scheduled++
		}
	}
	if scheduled != 1 || len(list.Backups) != 2 {
		t.Fatalf("after pruning: %d scheduled, %d total", scheduled, len(list.Backups))
	}
}

func countType(ts []store.Task, typ string) int {
	n := 0
	for _, t := range ts {
		if t.Type == typ {
			n++
		}
	}
	return n
}
