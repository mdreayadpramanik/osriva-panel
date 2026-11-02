//go:build linux

package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func readText(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func listUsers(context.Context) (any, error) {
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	return ParseUsers(string(passwd), readText("/etc/group")), nil
}

type account struct {
	home     string
	uid, gid int
}

func lookupAccount(name string) (account, error) {
	for _, u := range ParseUsers(readText("/etc/passwd"), "").Users {
		if u.Name == name {
			uid, err1 := strconv.Atoi(u.UID)
			gid, err2 := strconv.Atoi(u.GID)
			if err1 != nil || err2 != nil || !filepath.IsAbs(u.Home) {
				return account{}, fmt.Errorf("user %s has an invalid passwd entry", name)
			}
			return account{home: filepath.Clean(u.Home), uid: uid, gid: gid}, nil
		}
	}
	return account{}, fmt.Errorf("user %s does not exist", name)
}

// sshPaths returns ~/.ssh and ~/.ssh/authorized_keys after refusing symlinks and
// foreign ownership: root must never be tricked into writing through a link
// that a user planted (e.g. authorized_keys -> /etc/shadow).
func sshPaths(a account) (dir, file string, err error) {
	if fi, err := os.Lstat(a.home); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("home directory %s is missing or not a directory", a.home)
	}
	dir, file = filepath.Join(a.home, ".ssh"), filepath.Join(a.home, ".ssh", "authorized_keys")
	for _, p := range []string{dir, file} {
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("%w: %s is a symbolic link; refusing to follow it", ErrPathDenied, p)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != a.uid && int(st.Uid) != 0 {
			return "", "", fmt.Errorf("%w: %s is owned by another user", ErrPathDenied, p)
		}
	}
	return dir, file, nil
}

func listSSHKeys(_ context.Context, user string) (any, error) {
	a, err := lookupAccount(user)
	if err != nil {
		return nil, err
	}
	_, file, err := sshPaths(a)
	if err != nil {
		return nil, err
	}
	return agenttask.SSHKeysResult{User: user, Path: file, Keys: ListKeys(readText(file))}, nil
}

func writeAuthorizedKeys(a account, dir, file, content string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dir, a.uid, a.gid); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".authorized_keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Chown(tmp.Name(), a.uid, a.gid); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func addSSHKey(_ context.Context, user, key string) (any, error) {
	a, err := lookupAccount(user)
	if err != nil {
		return nil, err
	}
	dir, file, err := sshPaths(a)
	if err != nil {
		return nil, err
	}
	next, k, added, err := AddKey(readText(file), key)
	if err != nil {
		return nil, err
	}
	if added {
		if err := writeAuthorizedKeys(a, dir, file, next); err != nil {
			return nil, err
		}
	}
	return map[string]any{"user": user, "fingerprint": k.Fingerprint, "added": added}, nil
}

func removeSSHKey(_ context.Context, user, fingerprint string) (any, error) {
	a, err := lookupAccount(user)
	if err != nil {
		return nil, err
	}
	dir, file, err := sshPaths(a)
	if err != nil {
		return nil, err
	}
	next, removed := RemoveKey(readText(file), fingerprint)
	if removed == 0 {
		return nil, fmt.Errorf("no key with fingerprint %s for %s", fingerprint, user)
	}
	if err := writeAuthorizedKeys(a, dir, file, next); err != nil {
		return nil, err
	}
	return map[string]any{"user": user, "fingerprint": fingerprint, "removed": removed}, nil
}

func listCron(context.Context) (any, error) {
	res := agenttask.CronResult{Backend: "cron", Entries: []agenttask.CronEntry{}}
	res.Entries = append(res.Entries, ParseCrontab(readText("/etc/crontab"), "/etc/crontab", "", true)...)
	if files, err := filepath.Glob("/etc/cron.d/*"); err == nil {
		sort.Strings(files)
		for _, f := range files {
			if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
				res.Entries = append(res.Entries, ParseCrontab(readText(f), f, "", true)...)
			}
		}
	}
	// User crontabs: Debian/Ubuntu and RHEL/Fedora spool locations.
	for _, spool := range []string{"/var/spool/cron/crontabs", "/var/spool/cron"} {
		ents, err := os.ReadDir(spool)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(spool, e.Name())
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && agenttask.UserName.MatchString(e.Name()) {
				res.Entries = append(res.Entries, ParseCrontab(readText(p), p, e.Name(), false)...)
			}
		}
	}
	return res, nil
}

func timeInfo(ctx context.Context) (any, error) {
	res := agenttask.TimeResult{LocalTime: time.Now().Format(time.RFC3339)}
	res.Hostname, _ = os.Hostname()
	_, errHN := exec.LookPath("hostnamectl")
	_, errTD := exec.LookPath("timedatectl")
	res.CanSetName, res.CanSetZone = errHN == nil, errTD == nil
	if errTD == nil {
		if out, err := run(ctx, "timedatectl", "show", "--property=Timezone,NTPSynchronized"); err == nil {
			p := ParseSystemctlShow(out)
			res.Timezone = p["Timezone"]
			if v, ok := p["NTPSynchronized"]; ok {
				b := v == "yes"
				res.NTPSynced = &b
			}
		}
	}
	if res.Timezone == "" {
		if l, err := os.Readlink("/etc/localtime"); err == nil {
			if i := strings.Index(l, "zoneinfo/"); i >= 0 {
				res.Timezone = l[i+len("zoneinfo/"):]
			}
		}
	}
	return res, nil
}

func setHostname(ctx context.Context, name string) (any, error) {
	if _, err := exec.LookPath("hostnamectl"); err != nil {
		return nil, fmt.Errorf("%w: hostnamectl is not available", ErrUnsupported)
	}
	if _, err := run(ctx, "hostnamectl", "set-hostname", "--", name); err != nil {
		return nil, err
	}
	return timeInfo(ctx)
}

func setTimezone(ctx context.Context, tz string) (any, error) {
	if _, err := exec.LookPath("timedatectl"); err != nil {
		return nil, fmt.Errorf("%w: timedatectl is not available", ErrUnsupported)
	}
	// Only zones that exist in the tz database (validation already forbids "..").
	if fi, err := os.Stat(filepath.Join("/usr/share/zoneinfo", tz)); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("unknown timezone %q", tz)
	}
	if _, err := run(ctx, "timedatectl", "set-timezone", "--", tz); err != nil {
		return nil, err
	}
	return timeInfo(ctx)
}
