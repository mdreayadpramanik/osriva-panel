package tasks

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func ed25519Line(t *testing.T, comment string) (string, string) {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " " + comment, ssh.FingerprintSHA256(k)
}

func TestParseUsers(t *testing.T) {
	r := ParseUsers(`root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
alice:x:1000:1000:Alice Doe,,,:/home/alice:/bin/bash
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
`, `root:x:0:
sudo:x:27:alice
alice:x:1000:
docker:x:999:alice,bob
`)
	got := map[string]agenttask.User{}
	for _, u := range r.Users {
		got[u.Name] = u
	}
	a := got["alice"]
	if a.FullName != "Alice Doe" || a.Home != "/home/alice" || a.System || !a.Enabled || strings.Join(a.Groups, ",") != "alice,sudo,docker" {
		t.Fatalf("alice: %+v", a)
	}
	if !got["daemon"].System || got["daemon"].Enabled || got["root"].System || !got["nobody"].System {
		t.Fatalf("system flags: %+v", got)
	}
	if len(r.Groups) != 4 {
		t.Fatalf("groups: %+v", r.Groups)
	}
}

func TestAuthorizedKeysEditing(t *testing.T) {
	k1, fp1 := ed25519Line(t, "alice@laptop")
	k2, fp2 := ed25519Line(t, "ci")
	// Existing file with a comment and a key carrying options: both must survive untouched.
	opt, fpOpt := ed25519Line(t, "backup")
	original := "# managed by hand\ncommand=\"/usr/bin/backup\",no-pty " + opt + "\n" + k1 + "\n"

	if keys := ListKeys(original); len(keys) != 2 || keys[0].Fingerprint != fpOpt || !keys[0].Options || keys[1].Comment != "alice@laptop" {
		t.Fatalf("list: %+v", keys)
	}
	next, k, added, err := AddKey(original, k2)
	if err != nil || !added || k.Fingerprint != fp2 || !strings.HasPrefix(next, original) {
		t.Fatalf("add: %v %v %+v\n%s", added, err, k, next)
	}
	if again, _, added, _ := AddKey(next, k2); added || again != next {
		t.Fatal("duplicate key added")
	}
	removed, n := RemoveKey(next, fp1)
	if n != 1 || strings.Contains(removed, k1) || !strings.Contains(removed, "# managed by hand\n") || !strings.Contains(removed, opt) || !strings.Contains(removed, k2) {
		t.Fatalf("remove: %d\n%s", n, removed)
	}
	if _, n := RemoveKey(removed, "SHA256:doesnotexist"); n != 0 {
		t.Fatal("removed a non-matching key")
	}
}

func TestParsePublicKeyRejects(t *testing.T) {
	good, _ := ed25519Line(t, "x")
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	wk, _ := ssh.NewPublicKey(&weak.PublicKey)
	cases := map[string]string{
		"options":     `command="rm -rf /" ` + good,
		"two lines":   good + "\n" + good,
		"garbage":     "ssh-ed25519 notbase64!!",
		"private key": "-----BEGIN OPENSSH PRIVATE KEY-----",
		"weak rsa":    strings.TrimSpace(string(ssh.MarshalAuthorizedKey(wk))),
	}
	for name, line := range cases {
		if _, _, _, err := agenttask.ParsePublicKey(line); !errors.Is(err, agenttask.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, fp, comment, err := agenttask.ParsePublicKey(good); err != nil || !strings.HasPrefix(fp, "SHA256:") || comment != "x" {
		t.Fatalf("good key: %v %s %s", err, fp, comment)
	}
}

func TestParseCrontab(t *testing.T) {
	sys := ParseCrontab(`SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/bin
# m h dom mon dow user command
17 *	* * *	root    cd / && run-parts --report /etc/cron.hourly
@reboot   www-data  /usr/local/bin/warmup --all
broken line
`, "/etc/crontab", "", true)
	if len(sys) != 2 || sys[0].User != "root" || sys[0].Schedule != "17 * * * *" || sys[1].Schedule != "@reboot" || sys[1].User != "www-data" || sys[1].Command != "/usr/local/bin/warmup --all" {
		t.Fatalf("system crontab: %+v", sys)
	}
	user := ParseCrontab("*/5 * * * * /home/alice/bin/sync.sh >/dev/null 2>&1\n", "/var/spool/cron/crontabs/alice", "alice", false)
	if len(user) != 1 || user[0].User != "alice" || user[0].Command != "/home/alice/bin/sync.sh >/dev/null 2>&1" {
		t.Fatalf("user crontab: %+v", user)
	}
}

func TestValidateSystemParams(t *testing.T) {
	good, _ := ed25519Line(t, "x")
	ok := map[string]string{
		agenttask.SSHKeysList:   `{"user":"alice"}`,
		agenttask.SSHKeysAdd:    `{"user":"deploy","key":"` + good + `"}`,
		agenttask.SSHKeysRemove: `{"user":"root","fingerprint":"SHA256:abc"}`,
		agenttask.HostnameSet:   `{"hostname":"web-1.example.com"}`,
		agenttask.TimezoneSet:   `{"timezone":"America/Argentina/Buenos_Aires"}`,
		agenttask.UsersList:     `{}`,
	}
	for typ, p := range ok {
		if err := agenttask.Validate(typ, json.RawMessage(p)); err != nil {
			t.Errorf("%s %s: %v", typ, p, err)
		}
	}
	bad := map[string][]string{
		agenttask.SSHKeysList:   {`{"user":"../../etc"}`, `{"user":"Alice"}`, `{"user":"-r"}`},
		agenttask.HostnameSet:   {`{"hostname":"-bad"}`, `{"hostname":"a..b"}`, `{"hostname":"web_1"}`, `{"hostname":"x;reboot"}`},
		agenttask.TimezoneSet:   {`{"timezone":"../../etc/shadow"}`, `{"timezone":"Europe/..x"}`, `{"timezone":"/etc/passwd"}`},
		agenttask.SSHKeysRemove: {`{"user":"alice","fingerprint":"MD5:aa"}`},
	}
	for typ, list := range bad {
		for _, p := range list {
			if err := agenttask.Validate(typ, json.RawMessage(p)); !errors.Is(err, agenttask.ErrInvalid) {
				t.Errorf("%s %s: %v", typ, p, err)
			}
		}
	}
}

func TestPermissionClasses(t *testing.T) {
	want := map[string]string{
		agenttask.UsersList: agenttask.ClassRead, agenttask.SSHKeysList: agenttask.ClassRead, agenttask.CronList: agenttask.ClassRead,
		agenttask.SSHKeysAdd: agenttask.ClassAccess, agenttask.SSHKeysRemove: agenttask.ClassAccess,
		agenttask.HostnameSet: agenttask.ClassOperate, agenttask.FilesRead: agenttask.ClassFiles, agenttask.ServiceAction: agenttask.ClassOperate,
	}
	for typ, c := range want {
		if got := agenttask.Class(typ); got != c {
			t.Errorf("%s: %s, want %s", typ, got, c)
		}
	}
	if !agenttask.NeedsReauth(agenttask.SSHKeysAdd, nil) || agenttask.NeedsReauth(agenttask.UsersList, nil) ||
		!agenttask.NeedsReauth(agenttask.ServiceAction, json.RawMessage(`{"name":"x","action":"stop"}`)) ||
		agenttask.NeedsReauth(agenttask.ServiceAction, json.RawMessage(`{"name":"x","action":"restart"}`)) {
		t.Fatal("NeedsReauth")
	}
}

// Read-only system tasks against this host (Windows locally, Linux in CI).
func TestSystemTasksOnThisHost(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip()
	}
	ctx := context.Background()
	res, err := Execute(ctx, agenttask.UsersList, nil)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if u := res.(agenttask.UsersResult); len(u.Users) == 0 || len(u.Groups) == 0 {
		t.Fatalf("users: %+v", u)
	}
	ti, err := Execute(ctx, agenttask.TimeInfo, nil)
	if err != nil || ti.(agenttask.TimeResult).Hostname == "" {
		t.Fatalf("time: %+v %v", ti, err)
	}
	if _, err := Execute(ctx, agenttask.CronList, nil); err != nil {
		t.Fatalf("cron: %v", err)
	}
}
