// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func testManifest() Manifest {
	return Manifest{Name: "chat-notifier", Version: "0.1.0", Publisher: "Example", Protocol: Protocol, Kinds: []string{KindNotifier},
		Executables: map[string]Executable{"linux/amd64": {Path: "chat-notifier", SHA256: strings.Repeat("a", 64)}}}
}

func TestSignAndVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	m := testManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.VerifySignature([]ed25519.PublicKey{pub}); err == nil {
		t.Fatal("an unsigned manifest verified")
	}
	m.Sign(priv)
	if kid, err := m.VerifySignature([]ed25519.PublicKey{other, pub}); err != nil || kid != KeyID(pub) {
		t.Fatalf("verify: %v %v", kid, err)
	}
	if _, err := m.VerifySignature([]ed25519.PublicKey{other}); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("untrusted key: %v", err)
	}
	tampered := m
	tampered.Executables = map[string]Executable{"linux/amd64": {Path: "chat-notifier", SHA256: strings.Repeat("b", 64)}}
	if _, err := tampered.VerifySignature([]ed25519.PublicKey{pub}); err == nil {
		t.Fatal("a changed hash must break the signature")
	}
	renamed := m
	renamed.Version = "9.9.9"
	if _, err := renamed.VerifySignature([]ed25519.PublicKey{pub}); err == nil {
		t.Fatal("a changed version must break the signature")
	}
	bad := testManifest()
	bad.Executables["linux/amd64"] = Executable{Path: "../../bin/sh", SHA256: strings.Repeat("a", 64)}
	if err := bad.Validate(); err == nil {
		t.Fatal("an executable path outside the plugin directory was accepted")
	}
}

func TestHandle(t *testing.T) {
	var got Notification
	h := Handlers{
		Describe: func() Description { return Description{Name: "x", Version: "1", Kinds: []string{KindNotifier}} },
		Notify: func(_ context.Context, n Notification) error {
			got = n
			if n.Secret == "fail" {
				return Permanent(errors.New("nope"))
			}
			return nil
		},
	}
	r := handle(strings.NewReader(`{"protocol":1,"method":"notify","params":{"event":{"message":"hi"},"settings":{"a":1},"secret":"s"}}`), h)
	if !r.OK || got.Event.Message != "hi" || got.Secret != "s" || string(got.Settings) != `{"a":1}` {
		t.Fatalf("notify: %+v %+v", r, got)
	}
	r = handle(strings.NewReader(`{"protocol":1,"method":"notify","params":{"secret":"fail"}}`), h)
	if r.OK || r.Error == nil || !r.Error.Permanent {
		t.Fatalf("permanent error: %+v", r)
	}
	if r := handle(strings.NewReader(`{"protocol":2,"method":"describe"}`), h); r.OK {
		t.Fatal("unknown protocol accepted")
	}
	if r := handle(strings.NewReader(`{"protocol":1,"method":"describe"}`), h); !r.OK || !strings.Contains(string(r.Result), `"name":"x"`) {
		t.Fatalf("describe: %+v", r)
	}
}
