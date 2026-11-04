package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzGuardCheck feeds arbitrary paths to the file guard. Whatever the input,
// an allowed path must resolve inside the root and outside the deny list, and
// never into the sibling directory reached through a symlink.
func FuzzGuardCheck(f *testing.F) {
	root, outside, g := setup(f)
	symlinks := os.Symlink(outside, filepath.Join(root, "escape")) == nil
	for _, s := range []string{"", "hello.txt", "../outside/passwd", "sub/../../outside", "secret/key.pem",
		"escape/passwd", "escape/../hello.txt", "sub/id_rsa", "./././", "sub//x", "SECRET/key.pem", "..", "\x00"} {
		f.Add(s, false)
		f.Add(s, true)
	}
	secret := filepath.Join(root, "secret")
	f.Fuzz(func(t *testing.T, s string, absolute bool) {
		p := filepath.Join(root, s)
		if absolute {
			p = s
		}
		real, err := g.Check(p)
		if err != nil {
			return
		}
		if !within(root, real) {
			t.Fatalf("%q allowed but resolves to %q outside the root", p, real)
		}
		if within(secret, real) || within(outside, real) {
			t.Fatalf("%q allowed but resolves to denied %q", p, real)
		}
		if symlinks && within(filepath.Join(root, "escape"), real) {
			t.Fatalf("%q allowed through the symlink: %q", p, real)
		}
		if ok, _ := filepath.Match(filepath.Join(root, "*", "id_*"), real); ok {
			t.Fatalf("%q allowed but matches the deny glob: %q", p, real)
		}
	})
}
