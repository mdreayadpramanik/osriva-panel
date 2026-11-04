// Package plugins loads signed out-of-process plugins and runs them with a
// clean environment, a timeout and output limits (docs/adr/0016-plugins.md).
// Third-party code never runs inside the API process.
package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	maxStdout      = 1 << 20
	maxStderr      = 8 << 10
	maxSettingSize = 4 << 10
)

// CallTimeout bounds every plugin call (a variable so tests can shorten it).
var CallTimeout = 20 * time.Second

var ErrDisabled = errors.New("plugins are not configured: set OSRIVA_PLUGIN_DIR and OSRIVA_PLUGIN_TRUSTED_KEYS")

// Entry is a plugin found on disk.
type Entry struct {
	Name     string
	Manifest *pluginsdk.Manifest
	Dir      string
	Exe      string // absolute path of this platform's executable
	SHA256   string // of the executable on disk
	KeyID    string
	Valid    bool
	Reason   string // why the plugin is invalid
}

// View is an entry with its approval.
type View struct {
	Entry    Entry
	Approval *store.PluginApproval
	// Runnable is true when the plugin is valid, enabled and its binary is the approved one.
	Runnable bool
}

type Registry struct {
	dir     string
	trusted []ed25519.PublicKey
	store   store.Store
	id      *identity.Service
	log     *slog.Logger

	mu      sync.RWMutex
	entries map[string]*Entry
}

// New returns a registry; with an empty dir plugins are off.
func New(dir string, trustedKeys []string, st store.Store, id *identity.Service, log *slog.Logger) (*Registry, error) {
	if log == nil {
		log = slog.Default()
	}
	r := &Registry{dir: dir, store: st, id: id, log: log, entries: map[string]*Entry{}}
	for _, k := range trustedKeys {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		pub, err := pluginsdk.ParsePublicKey(k)
		if err != nil {
			return nil, fmt.Errorf("OSRIVA_PLUGIN_TRUSTED_KEYS: %w", err)
		}
		r.trusted = append(r.trusted, pub)
	}
	if dir != "" {
		r.Scan()
	}
	return r, nil
}

func (r *Registry) Enabled() bool { return r != nil && r.dir != "" && len(r.trusted) > 0 }

// Scan reads every plugin directory and verifies it.
func (r *Registry) Scan() {
	found := map[string]*Entry{}
	if r.Enabled() {
		dirs, err := os.ReadDir(r.dir)
		if err != nil {
			r.log.Warn("plugin directory", "dir", r.dir, "err", err)
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			e := r.check(filepath.Join(r.dir, d.Name()), d.Name())
			found[e.Name] = e
			if !e.Valid {
				r.log.Warn("plugin rejected", "plugin", e.Name, "reason", e.Reason)
			}
		}
	}
	r.mu.Lock()
	r.entries = found
	r.mu.Unlock()
}

func (r *Registry) check(dir, name string) *Entry {
	e := &Entry{Name: name, Dir: dir}
	bad := func(format string, a ...any) *Entry {
		e.Reason = fmt.Sprintf(format, a...)
		return e
	}
	m, err := pluginsdk.LoadManifest(dir)
	if err != nil {
		return bad("cannot read plugin.json: %v", err)
	}
	e.Manifest = m
	if err := m.Validate(); err != nil {
		return bad("invalid manifest: %v", err)
	}
	if m.Name != name {
		return bad("the manifest name %q does not match the directory", m.Name)
	}
	kid, err := m.VerifySignature(r.trusted)
	if err != nil {
		return bad("%v", err)
	}
	e.KeyID = kid
	exe, ok := m.Executables[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return bad("no executable for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	e.Exe = filepath.Join(dir, exe.Path)
	st, err := os.Lstat(e.Exe)
	if err != nil {
		return bad("executable missing: %v", err)
	}
	if !st.Mode().IsRegular() {
		return bad("the executable must be a regular file (no symlinks)")
	}
	sum, err := pluginsdk.HashFile(e.Exe)
	if err != nil {
		return bad("cannot hash the executable: %v", err)
	}
	e.SHA256 = sum
	if sum != exe.SHA256 {
		return bad("the executable does not match the signed manifest")
	}
	e.Valid = true
	return e
}

func (r *Registry) entry(name string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return nil, false
	}
	c := *e
	return &c, true
}

func runnable(e *Entry, a *store.PluginApproval) bool {
	return e.Valid && a != nil && a.Enabled && a.ApprovedSHA256 == e.SHA256
}

func (r *Registry) List(ctx context.Context, actor *identity.Principal) ([]View, error) {
	if err := r.id.Require(ctx, actor, rbac.SettingsRead); err != nil {
		return nil, err
	}
	approvals, err := r.store.ListPluginApprovals(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*store.PluginApproval{}
	for i := range approvals {
		byName[approvals[i].Name] = &approvals[i]
	}
	r.mu.RLock()
	out := make([]View, 0, len(r.entries))
	for _, e := range r.entries {
		a := byName[e.Name]
		out = append(out, View{Entry: *e, Approval: a, Runnable: runnable(e, a)})
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Entry.Name < out[j].Entry.Name })
	return out, nil
}

// Rescan re-reads the plugin directory (settings.write).
func (r *Registry) Rescan(ctx context.Context, actor *identity.Principal) error {
	if err := r.id.Require(ctx, actor, rbac.SettingsWrite); err != nil {
		return err
	}
	if !r.Enabled() {
		return &identity.PolicyError{Message: ErrDisabled.Error()}
	}
	r.Scan()
	r.id.Audit(ctx, actor.User, "plugins.rescan", "plugin", "", store.OutcomeSuccess, nil)
	return nil
}

// SetEnabled approves (pinning the current binary) or disables a plugin.
func (r *Registry) SetEnabled(ctx context.Context, actor *identity.Principal, name string, enabled bool) (*View, error) {
	if err := r.id.Require(ctx, actor, rbac.SettingsWrite); err != nil {
		return nil, err
	}
	if err := r.id.RequireRecentReauth(actor); err != nil {
		return nil, err
	}
	e, ok := r.entry(name)
	if !ok {
		return nil, store.ErrNotFound
	}
	if enabled && !e.Valid {
		return nil, &identity.PolicyError{Message: "the plugin is invalid: " + e.Reason}
	}
	sha := e.SHA256
	if sha == "" {
		old, err := r.store.GetPluginApproval(ctx, name)
		if err != nil {
			return nil, &identity.PolicyError{Message: "the plugin was never approved"}
		}
		sha = old.ApprovedSHA256
	}
	a := &store.PluginApproval{Name: name, Enabled: enabled, ApprovedSHA256: sha, ApprovedBy: &actor.User.ID}
	if err := r.store.SetPluginApproval(ctx, a); err != nil {
		return nil, err
	}
	r.id.Audit(ctx, actor.User, "plugins.approve", "plugin", name, store.OutcomeSuccess, map[string]any{
		"enabled": enabled, "sha256": sha, "version": e.Manifest.Version, "key_id": e.KeyID})
	return &View{Entry: *e, Approval: a, Runnable: runnable(e, a)}, nil
}

// HasNotifier reports whether name is a valid notifier plugin (approval is checked when it runs).
func (r *Registry) HasNotifier(name string) bool {
	e, ok := r.entry(name)
	if !ok || !e.Valid {
		return false
	}
	for _, k := range e.Manifest.Kinds {
		if k == pluginsdk.KindNotifier {
			return true
		}
	}
	return false
}

// CallError is a failure reported by a plugin or by running it.
type CallError struct {
	Code      string
	Message   string
	Permanent bool
}

func (e *CallError) Error() string { return e.Message }

// Call runs one method of an approved plugin.
func (r *Registry) Call(ctx context.Context, name, method string, params any) (json.RawMessage, error) {
	if !r.Enabled() {
		return nil, &CallError{Code: "disabled", Message: ErrDisabled.Error(), Permanent: true}
	}
	e, ok := r.entry(name)
	if !ok || !e.Valid {
		return nil, &CallError{Code: "unavailable", Message: "plugin " + name + " is not installed or not valid", Permanent: true}
	}
	a, err := r.store.GetPluginApproval(ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if a == nil || !a.Enabled {
		return nil, &CallError{Code: "not_approved", Message: "plugin " + name + " is not enabled", Permanent: true}
	}
	// Hash again right before starting it: a changed binary never runs unapproved.
	sum, err := pluginsdk.HashFile(e.Exe)
	if err != nil || sum != a.ApprovedSHA256 || sum != e.SHA256 {
		r.log.Warn("plugin binary changed since approval", "plugin", name)
		return nil, &CallError{Code: "changed", Message: "plugin " + name + " changed on disk since it was approved", Permanent: true}
	}
	p, _ := json.Marshal(params)
	req, _ := json.Marshal(pluginsdk.Request{Protocol: pluginsdk.Protocol, Method: method, Params: p})
	return r.exec(ctx, e, req)
}

// exec starts the plugin with a clean environment and bounded output.
func (r *Registry) exec(ctx context.Context, e *Entry, req []byte) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Exe)
	cmd.Dir = e.Dir
	cmd.Env = cleanEnv()
	cmd.Stdin = bytes.NewReader(req)
	stdout := &capped{max: maxStdout}
	stderr := &capped{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	started, done := isolate(cmd)
	defer done()
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Start()
	if err == nil {
		if serr := started(); serr != nil {
			r.log.Warn("plugin isolation incomplete", "plugin", e.Name, "err", serr)
		}
		err = cmd.Wait()
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		r.log.Info("plugin stderr", "plugin", e.Name, "stderr", s)
	}
	if ctx.Err() != nil {
		return nil, &CallError{Code: "timeout", Message: fmt.Sprintf("plugin %s did not answer within %s", e.Name, CallTimeout)}
	}
	if stdout.overflow {
		return nil, &CallError{Code: "output_too_large", Message: "plugin " + e.Name + " wrote more than 1 MiB", Permanent: true}
	}
	var resp pluginsdk.Response
	line := bytes.TrimSpace(stdout.buf.Bytes())
	if i := bytes.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	if jerr := json.Unmarshal(line, &resp); jerr != nil {
		msg := "plugin " + e.Name + " did not answer with JSON"
		if err != nil {
			msg += ": " + err.Error()
		}
		return nil, &CallError{Code: "bad_response", Message: msg}
	}
	r.log.Debug("plugin call", "plugin", e.Name, "ok", resp.OK, "duration", time.Since(start))
	if !resp.OK {
		if resp.Error == nil {
			return nil, &CallError{Code: "failed", Message: "plugin " + e.Name + " failed"}
		}
		return nil, &CallError{Code: resp.Error.Code, Message: resp.Error.Message, Permanent: resp.Error.Permanent}
	}
	return resp.Result, nil
}

// cleanEnv is all a plugin sees of the environment: nothing of the panel's.
func cleanEnv() []string {
	env := []string{"OSRIVA_PLUGIN_PROTOCOL=1"}
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		return append(env, "SystemRoot="+root, `PATH=`+root+`\System32`)
	}
	return append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
}

type capped struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.overflow = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil // keep draining so the plugin does not block
	}
	return c.buf.Write(p)
}

func (c *capped) String() string { return c.buf.String() }

// Notify sends an alert event through a notifier plugin.
func (r *Registry) Notify(ctx context.Context, name string, settings json.RawMessage, secret string, ev pluginsdk.Event) error {
	if !r.HasNotifier(name) {
		return &CallError{Code: "unavailable", Message: "plugin " + name + " is not an available notifier", Permanent: true}
	}
	_, err := r.Call(ctx, name, pluginsdk.MethodNotify, pluginsdk.Notification{Event: ev, Settings: settings, Secret: secret})
	return err
}

// ValidSettings checks the size and JSON of a channel's plugin settings.
func ValidSettings(raw json.RawMessage) bool {
	return len(raw) <= maxSettingSize && (len(raw) == 0 || json.Valid(raw))
}
