// SPDX-License-Identifier: Apache-2.0

// Command osriva-plugin creates publisher keys and signs plugin manifests, so
// plugin authors do not need the panel to publish a plugin.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
)

const usage = `osriva-plugin: sign OSRIVA Panel plugins

  osriva-plugin keygen --out FILE        write a new private key (0600) and print its public key
  osriva-plugin sign --key FILE DIR      hash the executables listed in DIR/plugin.json and sign it
  osriva-plugin verify --trusted KEY DIR check DIR/plugin.json against a base64 public key
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "private key file to create")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("public key: %s\nkey id:     %s\n", base64.StdEncoding.EncodeToString(pub), pluginsdk.KeyID(pub))
	return nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyFile := fs.String("key", "", "private key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" || fs.NArg() != 1 {
		return errors.New("usage: osriva-plugin sign --key FILE DIR")
	}
	dir := fs.Arg(0)
	raw, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	key, err := pluginsdk.ParsePrivateKey(string(raw))
	if err != nil {
		return err
	}
	m, err := pluginsdk.LoadManifest(dir)
	if err != nil {
		return err
	}
	for p, e := range m.Executables {
		sum, err := pluginsdk.HashFile(filepath.Join(dir, e.Path))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		e.SHA256 = sum
		m.Executables[p] = e
	}
	if err := m.Validate(); err != nil {
		return err
	}
	m.Sign(key)
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, pluginsdk.ManifestFile), append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("signed %s %s with key %s\n", m.Name, m.Version, m.Signature.KeyID)
	return nil
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	trusted := fs.String("trusted", "", "base64 public key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: osriva-plugin verify --trusted KEY DIR")
	}
	pub, err := pluginsdk.ParsePublicKey(*trusted)
	if err != nil {
		return err
	}
	m, err := pluginsdk.LoadManifest(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	kid, err := m.VerifySignature([]ed25519.PublicKey{pub})
	if err != nil {
		return err
	}
	for p, e := range m.Executables {
		sum, err := pluginsdk.HashFile(filepath.Join(fs.Arg(0), e.Path))
		if err != nil || sum != e.SHA256 {
			return fmt.Errorf("%s: the executable does not match the manifest", p)
		}
	}
	fmt.Printf("ok: %s %s signed by %s\n", m.Name, m.Version, kid)
	return nil
}
