package tasks

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func tarOf(entries ...tar.Header) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		h := h
		var body []byte
		if h.Typeflag == tar.TypeReg {
			body = []byte("x")
			h.Size = 1
		}
		_ = tw.WriteHeader(&h)
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	return buf.Bytes()
}

// FuzzExtract restores arbitrary tar archives the way backup restores do.
// Whatever the archive holds (traversal, absolute names, symlinks followed by
// files through them, duplicates), nothing may appear outside the target.
func FuzzExtract(f *testing.F) {
	reg := func(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644} }
	link := func(name, to string) tar.Header {
		return tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: to, Mode: 0o777}
	}
	dir := func(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755} }
	f.Add(tarOf(reg("a.txt"), dir("d/"), reg("d/b.txt")))
	f.Add(tarOf(reg("../outside/evil")))
	f.Add(tarOf(reg("/etc/evil"), reg("./a/../../evil")))
	f.Add(tarOf(link("l", "../outside"), reg("l/evil")))
	f.Add(tarOf(link("l", "/"), reg("l/tmp/evil")))
	f.Add(tarOf(link("x", "../outside/evil"), reg("x")))
	f.Add(tarOf(dir("d"), link("d/l", "../../outside"), reg("d/l/evil")))
	f.Add(tarOf(reg("a"), reg("a")))
	f.Fuzz(func(t *testing.T, archive []byte) {
		base := t.TempDir()
		root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
		for _, d := range []string{root, outside} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		tr := tar.NewReader(bytes.NewReader(archive))
		for n := 0; n < 64; n++ {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			_ = extractEntry(root, hdr.Name, hdr, io.LimitReader(tr, 1<<16))
		}
		// Only root and outside may exist next to each other, and outside stays empty.
		top, _ := os.ReadDir(base)
		if len(top) != 2 {
			t.Fatalf("extraction created %v next to the target", top)
		}
		if inner, _ := os.ReadDir(outside); len(inner) != 0 {
			t.Fatalf("extraction wrote %v outside the target", inner)
		}
		// Every regular file written resolves inside root.
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			real, err := filepath.EvalSymlinks(p)
			if err == nil && !within(root, real) {
				if r, _ := filepath.EvalSymlinks(root); !within(r, real) {
					t.Fatalf("%s resolves outside the target: %s", p, real)
				}
			}
			return nil
		})
	})
}
