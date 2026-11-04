package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func setup(t testing.TB) (root, outside string, g *FSGuard) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "sub"), filepath.Join(root, "secret")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, "hello.txt"):         "hello\nworld\n",
		filepath.Join(root, "secret", "key.pem"): "PRIVATE",
		filepath.Join(outside, "passwd"):         "root:x:0:0",
		filepath.Join(root, "sub", "id_rsa"):     "KEY",
		filepath.Join(root, "bin.dat"):           "a\x00b",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g = NewFSGuard([]string{root}, []string{filepath.Join(root, "secret")}, []string{filepath.Join(root, "*", "id_*")})
	return root, outside, g
}

func doFile(t *testing.T, g *FSGuard, typ string, params any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	if err := agenttask.Validate(typ, raw); err != nil {
		return nil, err
	}
	return fileTask(context.Background(), g, typ, raw)
}

func TestGuardBlocksEscapes(t *testing.T) {
	root, outside, g := setup(t)
	denied := []string{
		filepath.Join(outside, "passwd"),
		filepath.Join(root, "..", "outside", "passwd"),
		filepath.Join(root, "secret", "key.pem"),
		filepath.Join(root, "secret"),
		filepath.Join(root, "sub", "id_rsa"),
		filepath.Dir(root),
	}
	for _, p := range denied {
		if _, err := g.Check(p); !errors.Is(err, ErrPathDenied) {
			t.Errorf("%s: expected ErrPathDenied, got %v", p, err)
		}
	}
	if _, err := g.Check("relative/path"); err == nil {
		t.Error("relative path accepted")
	}
	for _, p := range []string{root, filepath.Join(root, "hello.txt"), filepath.Join(root, "sub"), filepath.Join(root, "new.txt")} {
		if _, err := g.Check(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestGuardBlocksSymlinkEscape(t *testing.T) {
	root, outside, g := setup(t)
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	for _, p := range []string{link, filepath.Join(link, "passwd"), filepath.Join(link, "new.txt")} {
		if _, err := g.Check(p); !errors.Is(err, ErrPathDenied) {
			t.Errorf("symlink escape %s: %v", p, err)
		}
	}
	// Listing the root shows the link but marks nothing outside as readable.
	if _, err := doFile(t, g, agenttask.FilesRead, agenttask.PathParams{Path: filepath.Join(link, "passwd")}); !errors.Is(err, ErrPathDenied) {
		t.Errorf("read through symlink: %v", err)
	}
}

func TestFileOperations(t *testing.T) {
	root, _, g := setup(t)
	res, err := doFile(t, g, agenttask.FilesList, agenttask.PathParams{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	list := res.(agenttask.FilesListResult)
	if list.Parent != "" || len(list.Roots) != 1 || list.Entries[0].Type != "dir" {
		t.Fatalf("list root: %+v", list)
	}
	byName := map[string]agenttask.FileEntry{}
	for _, e := range list.Entries {
		byName[e.Name] = e
	}
	if !byName["secret"].Denied || byName["hello.txt"].Denied || byName["hello.txt"].Size != 12 {
		t.Fatalf("entries: %+v", byName)
	}
	sub, _ := doFile(t, g, agenttask.FilesList, agenttask.PathParams{Path: filepath.Join(root, "sub")})
	if sub.(agenttask.FilesListResult).Parent != root {
		t.Fatalf("parent: %+v", sub)
	}

	r, err := doFile(t, g, agenttask.FilesRead, agenttask.PathParams{Path: filepath.Join(root, "hello.txt")})
	if err != nil || r.(agenttask.FilesReadResult).Content != "hello\nworld\n" {
		t.Fatalf("read: %+v %v", r, err)
	}
	b, _ := doFile(t, g, agenttask.FilesRead, agenttask.PathParams{Path: filepath.Join(root, "bin.dat")})
	if br := b.(agenttask.FilesReadResult); !br.Binary || br.Content != "" {
		t.Fatalf("binary file must not be returned as text: %+v", br)
	}

	newFile := filepath.Join(root, "sub", "new.conf")
	if _, err := doFile(t, g, agenttask.FilesWrite, agenttask.FilesWriteParams{Path: newFile, Content: "x=1\n"}); err == nil {
		t.Fatal("write to a missing file without create succeeded")
	}
	w, err := doFile(t, g, agenttask.FilesWrite, agenttask.FilesWriteParams{Path: newFile, Content: "x=1\n", Create: true})
	if err != nil || !w.(agenttask.FilesWriteResult).Created {
		t.Fatalf("create: %+v %v", w, err)
	}
	if _, err := doFile(t, g, agenttask.FilesWrite, agenttask.FilesWriteParams{Path: newFile, Content: "x=2\n"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(newFile); string(got) != "x=2\n" {
		t.Fatalf("content after overwrite: %q", got)
	}
	if _, err := doFile(t, g, agenttask.FilesWrite, agenttask.FilesWriteParams{Path: filepath.Join(root, "secret", "key.pem"), Content: "x"}); !errors.Is(err, ErrPathDenied) {
		t.Fatalf("write to denied file: %v", err)
	}

	dir := filepath.Join(root, "made")
	if _, err := doFile(t, g, agenttask.FilesMkdir, agenttask.PathParams{Path: dir}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	if _, err := doFile(t, g, agenttask.FilesDelete, agenttask.PathParams{Path: dir}); err == nil {
		t.Fatal("non-empty directory deleted")
	}
	if _, err := doFile(t, g, agenttask.FilesDelete, agenttask.PathParams{Path: filepath.Join(dir, "f")}); err != nil {
		t.Fatal(err)
	}
	if _, err := doFile(t, g, agenttask.FilesDelete, agenttask.PathParams{Path: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := doFile(t, g, agenttask.FilesDelete, agenttask.PathParams{Path: root}); !errors.Is(err, ErrPathDenied) {
		t.Fatalf("root deleted: %v", err)
	}
	// No temp files are left behind by atomic writes.
	ents, _ := os.ReadDir(filepath.Join(root, "sub"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".osriva-write-") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestDefaultPolicyProtectsSecrets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux paths")
	}
	ConfigureFiles(nil, "/etc/osriva-agent")
	g := currentGuard()
	for _, p := range []string{"/etc/shadow", "/etc/sudoers", "/etc/osriva-agent/agent.key", "/root/.ssh/authorized_keys", "/proc/1/environ"} {
		if _, err := g.Check(p); err == nil {
			t.Errorf("%s allowed by default policy", p)
		}
	}
	if _, err := g.Check("/etc/hostname"); err != nil {
		t.Errorf("/etc/hostname: %v", err)
	}
}

func TestValidateFileParams(t *testing.T) {
	bad := []struct{ typ, params string }{
		{agenttask.FilesList, `{"path":""}`},
		{agenttask.FilesList, `{"path":"relative"}`},
		{agenttask.FilesRead, `{"path":"/etc/x\u0000y"}`},
		{agenttask.FilesWrite, `{"path":"/etc/x","content":"` + strings.Repeat("a", agenttask.MaxFileBytes+1) + `"}`},
		{agenttask.FilesDelete, `{"path":"/x","recursive":true}`},
	}
	for _, c := range bad {
		if err := agenttask.Validate(c.typ, json.RawMessage(c.params)); !errors.Is(err, agenttask.ErrInvalid) {
			t.Errorf("%s %.60s: %v", c.typ, c.params, err)
		}
	}
	if err := agenttask.Validate(agenttask.FilesList, json.RawMessage(`{"path":"C:\\inetpub"}`)); err != nil {
		t.Errorf("windows path: %v", err)
	}
}

// Paths plainly outside every root are refused before touching the disk, so
// even unreadable system files produce a policy error, not an OS error.
func TestGuardRefusesBeforeTouchingDisk(t *testing.T) {
	root, _, g := setup(t)
	outside := filepath.Join(filepath.Dir(root), "does-not-exist", "deeper", "file")
	if _, err := g.Check(outside); !errors.Is(err, ErrPathDenied) {
		t.Fatalf("nonexistent outside path: %v (want ErrPathDenied, not a stat error)", err)
	}
}
