package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testBackups(t *testing.T, s store.Store) {
	now := time.Now().UTC()
	a, _ := newServer(t, s, "bk-a", now)
	b, _ := newServer(t, s, "bk-b", now)

	k1, err := s.EnsureBackupKey(ctx(), &store.BackupKey{ServerID: a.ID, Recipient: "age1one", IdentityEnc: []byte{1}})
	if err != nil || k1.Recipient != "age1one" {
		t.Fatalf("EnsureBackupKey: %+v %v", k1, err)
	}
	// a second key never replaces the first (existing backups would become unreadable)
	if k2, _ := s.EnsureBackupKey(ctx(), &store.BackupKey{ServerID: a.ID, Recipient: "age1two", IdentityEnc: []byte{2}}); k2.Recipient != "age1one" || k2.IdentityEnc[0] != 1 {
		t.Fatalf("key replaced: %+v", k2)
	}

	pol := &store.BackupPolicy{ID: auth.NewID(), ServerID: a.ID, Kind: "volume", Target: "data", IntervalHours: 24, Keep: 7}
	if err := s.CreateBackupPolicy(ctx(), pol); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBackupPolicy(ctx(), &store.BackupPolicy{ID: auth.NewID(), ServerID: a.ID, Kind: "volume", Target: "data", IntervalHours: 1, Keep: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate policy: %v", err)
	}
	if err := s.CreateBackupPolicy(ctx(), &store.BackupPolicy{ID: auth.NewID(), ServerID: b.ID, Kind: "wordpress", Target: "blog", IntervalHours: 6, Keep: 3}); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListBackupPolicies(ctx(), ""); len(all) < 2 {
		t.Fatalf("all policies: %+v", all)
	}
	if mine, _ := s.ListBackupPolicies(ctx(), a.ID); len(mine) != 1 || mine[0].LastRunAt != nil {
		t.Fatalf("server policies: %+v", mine)
	}
	ran := now.Truncate(time.Second)
	if err := s.MarkBackupPolicyRun(ctx(), pol.ID, ran); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBackupPolicy(ctx(), pol.ID); got.LastRunAt == nil || !got.LastRunAt.Equal(ran) {
		t.Fatalf("last run: %+v", got)
	}

	task := newTask(t, s, a.ID, "backup.create", now.Add(time.Hour))
	bk := &store.Backup{ID: auth.NewID(), ServerID: a.ID, Kind: "volume", Target: "data", TaskID: &task.ID, PolicyID: &pol.ID}
	if err := s.CreateBackup(ctx(), bk); err != nil || bk.CreatedAt.IsZero() {
		t.Fatalf("CreateBackup: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	manual := &store.Backup{ID: auth.NewID(), ServerID: a.ID, Kind: "wordpress", Target: "blog"}
	if err := s.CreateBackup(ctx(), manual); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBackupResult(ctx(), bk.ID, 1234, "aa", "bb", 9); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetBackup(ctx(), bk.ID)
	if got.Size == nil || *got.Size != 1234 || *got.SHA256 != "aa" || *got.PlainSHA256 != "bb" || *got.Files != 9 || got.PolicyID == nil || *got.PolicyID != pol.ID {
		t.Fatalf("GetBackup: %+v", got)
	}
	if list, _ := s.ListBackups(ctx(), a.ID, 10); len(list) != 2 || list[0].ID != manual.ID || list[0].SHA256 != nil {
		t.Fatalf("ListBackups newest first: %+v", list)
	}
	if err := s.DeleteBackupPolicy(ctx(), pol.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBackup(ctx(), bk.ID); got.PolicyID != nil {
		t.Fatal("policy link must clear when the policy is deleted")
	}
	if err := s.DeleteBackup(ctx(), bk.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBackup(ctx(), bk.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
