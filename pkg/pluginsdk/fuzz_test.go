// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzManifestSignature mutates a signed plugin.json. A manifest that still
// loads and verifies must sign exactly the same content as the original: no
// change to a name, version, hash or path survives verification.
func FuzzManifestSignature(f *testing.F) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		f.Fatal(err)
	}
	m := Manifest{
		Name: "chat-notifier", Version: "1.0.0", Publisher: "Example", Protocol: Protocol,
		Kinds:       []string{KindNotifier},
		Executables: map[string]Executable{"linux/amd64": {Path: "chat-notifier", SHA256: strings.Repeat("a", 64)}},
	}
	m.Sign(priv)
	signed, _ := json.Marshal(m)
	want := m.Canonical()
	f.Add(signed)
	f.Add(bytes.Replace(signed, []byte("1.0.0"), []byte("1.0.1"), 1))
	f.Add(append(append([]byte{}, signed...), []byte(`{"name":"evil"}`)...))
	f.Add(bytes.Replace(signed, []byte(`"name"`), []byte(`"Name"`), 1))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadManifest(dir)
		if err != nil || got.Validate() != nil {
			return
		}
		if _, err := got.VerifySignature([]ed25519.PublicKey{pub}); err != nil {
			return
		}
		if !bytes.Equal(got.Canonical(), want) {
			t.Fatalf("a changed manifest verified:\n%s", got.Canonical())
		}
	})
}
