package tasks

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

type backupEnv struct {
	f        *fakeEngine
	volumes  map[string]string // name -> directory
	identity *age.X25519Identity
	dbCalls  []string
}

func setupBackup(t *testing.T) *backupEnv {
	f, _ := setupDeploy(t)
	f.networks, f.volumes = map[string]map[string]string{}, map[string]map[string]string{}
	id, _ := age.GenerateX25519Identity()
	e := &backupEnv{f: f, volumes: map[string]string{}, identity: id}
	oldDir, oldPath, oldDB := backupDir, volumePath, runDBTool
	backupDir = t.TempDir()
	volumePath = func(_ context.Context, _ *dockerClient, name string) (string, error) {
		if d, ok := e.volumes[name]; ok {
			return d, nil
		}
		return "", os.ErrNotExist
	}
	runDBTool = func(_ context.Context, _ *dockerClient, name, dir, script string) error {
		e.dbCalls = append(e.dbCalls, script)
		if strings.Contains(script, "mariadb-dump") {
			return os.WriteFile(filepath.Join(dir, "db.sql"), []byte("-- dump of "+name+"\nCREATE TABLE wp_posts (id int);\n"), 0o600)
		}
		b, err := os.ReadFile(filepath.Join(dir, "db.sql"))
		if err == nil && !strings.Contains(string(b), "CREATE TABLE wp_posts") {
			t.Errorf("restored dump: %s", b)
		}
		return err
	}
	t.Cleanup(func() { backupDir, volumePath, runDBTool = oldDir, oldPath, oldDB })
	return e
}

func (e *backupEnv) run(t *testing.T, typ string, p any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return Execute(context.Background(), typ, raw)
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			out[rel] = "-> " + l
		case info.IsDir():
			out[rel] = "dir"
		default:
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func TestVolumeBackupVerifyRestore(t *testing.T) {
	e := setupBackup(t)
	vol := t.TempDir()
	e.volumes["data"] = vol
	writeFile(t, filepath.Join(vol, "a.txt"), "alpha")
	writeFile(t, filepath.Join(vol, "sub", "deep", "b.bin"), strings.Repeat("\x00\x01binary", 1000))
	if runtime.GOOS != "windows" {
		_ = os.Symlink("../a.txt", filepath.Join(vol, "sub", "link"))
	}
	before := snapshot(t, vol)
	e.f.cts["app"] = &fakeCt{ID: "app", Name: "app", Image: "app:1", State: "running", Labels: map[string]string{},
		Body: map[string]any{"HostConfig": map[string]any{"Mounts": []any{map[string]any{"Source": "data"}}}}}

	out, err := e.run(t, agenttask.BackupCreate, agenttask.BackupCreateParams{ID: "b1", Kind: "volume", Target: "data", Recipient: e.identity.Recipient().String()})
	cr := out.(agenttask.BackupCreateResult)
	if err != nil || cr.Size == 0 || len(cr.SHA256) != 64 || cr.Files < 3 {
		t.Fatalf("create: %+v %v", cr, err)
	}
	fi, _ := os.Stat(backupPath("b1"))
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode %v", fi.Mode())
	}
	raw, _ := os.ReadFile(backupPath("b1"))
	if strings.Contains(string(raw), "alpha") {
		t.Fatal("backup is not encrypted")
	}

	out, err = e.run(t, agenttask.BackupVerify, agenttask.BackupVerifyParams{ID: "b1", SHA256: cr.SHA256, BackupKey: e.identity.String()})
	if v := out.(agenttask.BackupVerifyResult); err != nil || !v.OK || v.PlainSHA256 != cr.PlainSHA256 || v.Files != cr.Files {
		t.Fatalf("verify: %+v %v (created %+v)", v, err, cr)
	}
	other, _ := age.GenerateX25519Identity()
	out, _ = e.run(t, agenttask.BackupVerify, agenttask.BackupVerifyParams{ID: "b1", SHA256: cr.SHA256, BackupKey: other.String()})
	if v := out.(agenttask.BackupVerifyResult); v.OK || !strings.Contains(v.Problem, "decrypt") {
		t.Fatalf("wrong key: %+v", v)
	}

	// damage the live data, then restore
	_ = os.Remove(filepath.Join(vol, "a.txt"))
	writeFile(t, filepath.Join(vol, "junk"), "should disappear")
	out, err = e.run(t, agenttask.BackupRestore, agenttask.BackupRestoreParams{ID: "b1", Kind: "volume", Target: "data", SHA256: cr.SHA256, BackupKey: e.identity.String()})
	rr := out.(agenttask.BackupRestoreResult)
	if err != nil || len(rr.Stopped) != 1 || e.f.cts["app"].State != "running" {
		t.Fatalf("restore: %+v %v state=%s", rr, err, e.f.cts["app"].State)
	}
	if after := snapshot(t, vol); len(after) != len(before) || after["a.txt"] != "alpha" || after["junk"] != "" {
		t.Fatalf("restored tree differs:\nbefore %v\nafter  %v", before, after)
	}

	// a damaged file is detected and nothing is touched
	raw[len(raw)/2] ^= 0xff
	_ = os.WriteFile(backupPath("b1"), raw, 0o600)
	writeFile(t, filepath.Join(vol, "keep"), "x")
	if _, err := e.run(t, agenttask.BackupRestore, agenttask.BackupRestoreParams{ID: "b1", Kind: "volume", Target: "data", SHA256: cr.SHA256, BackupKey: e.identity.String()}); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("damaged backup restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vol, "keep")); err != nil {
		t.Fatal("live data was touched by a failed restore")
	}

	out, _ = e.run(t, agenttask.BackupList, map[string]any{})
	if l := out.([]agenttask.BackupFile); len(l) != 1 || l[0].ID != "b1" {
		t.Fatalf("list: %+v", l)
	}
	if _, err := e.run(t, agenttask.BackupDelete, agenttask.BackupRefParams{ID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupPath("b1")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
}

func TestBackupRefusesControlPlaneVolume(t *testing.T) {
	e := setupBackup(t)
	e.volumes["osriva_postgres-data"] = t.TempDir()
	e.f.cts["pg"] = &fakeCt{ID: "pg", Name: "osriva-postgres-1", State: "running", Labels: map[string]string{agenttask.ControlPlaneLabel: "true"},
		Body: map[string]any{"HostConfig": map[string]any{"Mounts": []any{map[string]any{"Source": "osriva_postgres-data"}}}}}
	if _, err := e.run(t, agenttask.BackupCreate, agenttask.BackupCreateParams{ID: "b2", Kind: "volume", Target: "osriva_postgres-data", Recipient: e.identity.Recipient().String()}); err == nil || !strings.Contains(err.Error(), "control plane") {
		t.Fatalf("control-plane volume: %v", err)
	}
}

func TestWordPressBackupRestore(t *testing.T) {
	e := setupBackup(t)
	files := t.TempDir()
	e.volumes[agenttask.WPFilesVolume("blog")] = files
	writeFile(t, filepath.Join(files, "wp-config.php"), "<?php // config")
	labels := map[string]string{agenttask.CMSLabel: "blog"}
	e.f.cts["wp"] = &fakeCt{ID: "wp", Name: agenttask.WPContainer("blog"), Image: agenttask.WordPressImage, State: "running", Labels: labels, Body: map[string]any{}}
	e.f.cts["db"] = &fakeCt{ID: "db", Name: agenttask.WPDBContainer("blog"), Image: agenttask.MariaDBImage, State: "running", Labels: labels, Body: map[string]any{}}

	out, err := e.run(t, agenttask.BackupCreate, agenttask.BackupCreateParams{ID: "w1", Kind: "wordpress", Target: "blog", Recipient: e.identity.Recipient().String()})
	cr := out.(agenttask.BackupCreateResult)
	if err != nil || len(e.dbCalls) != 1 || !strings.Contains(e.dbCalls[0], "--single-transaction") {
		t.Fatalf("create: %+v %v %v", cr, err, e.dbCalls)
	}
	writeFile(t, filepath.Join(files, "wp-config.php"), "changed")
	out, err = e.run(t, agenttask.BackupRestore, agenttask.BackupRestoreParams{ID: "w1", Kind: "wordpress", Target: "blog", SHA256: cr.SHA256, BackupKey: e.identity.String()})
	rr := out.(agenttask.BackupRestoreResult)
	if err != nil || !rr.Database || len(rr.Stopped) != 1 || rr.Stopped[0] != "wp" || e.f.cts["wp"].State != "running" {
		t.Fatalf("restore: %+v %v", rr, err)
	}
	if b, _ := os.ReadFile(filepath.Join(files, "wp-config.php")); string(b) != "<?php // config" {
		t.Fatalf("files not restored: %s", b)
	}
	if len(e.dbCalls) != 2 || !strings.Contains(e.dbCalls[1], "mariadb -h") {
		t.Fatalf("database import: %v", e.dbCalls)
	}
}

func TestExtractRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../evil", "/etc/passwd", "a/../../evil", "a\x00b"} {
		if err := extractEntry(root, name, &tar.Header{Typeflag: tar.TypeReg, Mode: 0o644}, strings.NewReader("x")); err == nil {
			t.Errorf("%q extracted", name)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	outside := t.TempDir()
	if err := extractEntry(root, "link", &tar.Header{Typeflag: tar.TypeSymlink, Linkname: outside}, nil); err != nil {
		t.Fatal(err)
	}
	if err := extractEntry(root, "link/pwned", &tar.Header{Typeflag: tar.TypeReg, Mode: 0o644}, strings.NewReader("x")); err == nil {
		t.Fatal("wrote through a symbolic link")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatal("file created outside the root")
	}
}
