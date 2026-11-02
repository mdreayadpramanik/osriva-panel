package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	if _, err := Load(dir); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty dir: %v", err)
	}
	s := &State{Dir: dir, Config: Config{ServerID: "id-1", AgentEndpoint: "https://x:8443"},
		CAPEM: []byte("ca"), CertPEM: []byte("cert"), KeyPEM: []byte("key")}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("Exists = false after Save")
	}
	got, err := Load(dir)
	if err != nil || !reflect.DeepEqual(got.Config, s.Config) || string(got.KeyPEM) != "key" || string(got.CAPEM) != "ca" {
		t.Fatalf("load: %+v %v", got, err)
	}
	if err := got.SaveCredentials([]byte("cert2"), []byte("key2")); err != nil {
		t.Fatal(err)
	}
	again, _ := Load(dir)
	if string(again.CertPEM) != "cert2" || string(again.KeyPEM) != "key2" {
		t.Fatal("renewed credentials not persisted")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, fileKey))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key mode %v", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
