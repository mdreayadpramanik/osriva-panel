package tasks

import (
	"bufio"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// ---- /etc/passwd and /etc/group (portable parsers, tested everywhere) ----

func noLogin(shell string) bool {
	return strings.HasSuffix(shell, "/nologin") || strings.HasSuffix(shell, "/false") || shell == ""
}

// ParseUsers combines /etc/passwd and /etc/group.
func ParseUsers(passwd, group string) agenttask.UsersResult {
	res := agenttask.UsersResult{Users: []agenttask.User{}, Groups: []agenttask.Group{}}
	gidName := map[string]string{}
	member := map[string][]string{}
	for _, line := range strings.Split(group, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 4 || f[0] == "" || strings.HasPrefix(f[0], "#") {
			continue
		}
		g := agenttask.Group{Name: f[0], GID: f[2], Members: []string{}}
		for _, m := range strings.Split(f[3], ",") {
			if m = strings.TrimSpace(m); m != "" {
				g.Members = append(g.Members, m)
				member[m] = append(member[m], g.Name)
			}
		}
		gidName[f[2]] = f[0]
		res.Groups = append(res.Groups, g)
	}
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 7 || f[0] == "" || strings.HasPrefix(f[0], "#") {
			continue
		}
		u := agenttask.User{Name: f[0], UID: f[2], GID: f[3], FullName: strings.Split(f[4], ",")[0], Home: f[5], Shell: f[6], Groups: []string{}}
		if g, ok := gidName[f[3]]; ok {
			u.Groups = append(u.Groups, g)
		}
		for _, g := range member[u.Name] {
			if len(u.Groups) == 0 || u.Groups[0] != g {
				u.Groups = append(u.Groups, g)
			}
		}
		uid, _ := strconv.Atoi(f[2])
		u.System = uid != 0 && (uid < 1000 || uid == 65534)
		u.Enabled = !noLogin(u.Shell)
		res.Users = append(res.Users, u)
	}
	sort.Slice(res.Users, func(i, j int) bool { return res.Users[i].Name < res.Users[j].Name })
	sort.Slice(res.Groups, func(i, j int) bool { return res.Groups[i].Name < res.Groups[j].Name })
	return res
}

// ---- authorized_keys ----

// keyOfLine returns the fingerprint of an authorized_keys line, if it holds a key.
func keyOfLine(line string) (agenttask.SSHKey, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return agenttask.SSHKey{}, false
	}
	key, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(t))
	if err != nil {
		return agenttask.SSHKey{}, false
	}
	return agenttask.SSHKey{Type: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), Comment: comment, Options: len(options) > 0}, true
}

// ListKeys returns the keys in an authorized_keys file (unparseable lines are skipped).
func ListKeys(content string) []agenttask.SSHKey {
	keys := []agenttask.SSHKey{}
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if k, ok := keyOfLine(sc.Text()); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// AddKey appends a validated key unless its fingerprint is already present.
// All other lines are kept byte for byte.
func AddKey(content, line string) (string, agenttask.SSHKey, bool, error) {
	key, fp, comment, err := agenttask.ParsePublicKey(line)
	if err != nil {
		return content, agenttask.SSHKey{}, false, err
	}
	k := agenttask.SSHKey{Type: key.Type(), Fingerprint: fp, Comment: comment}
	for _, existing := range ListKeys(content) {
		if existing.Fingerprint == fp {
			return content, k, false, nil
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + strings.TrimSpace(line) + "\n", k, true, nil
}

// RemoveKey drops every line whose key has the fingerprint; other lines are untouched.
func RemoveKey(content, fingerprint string) (string, int) {
	var b strings.Builder
	removed := 0
	lines := strings.SplitAfter(content, "\n")
	for _, l := range lines {
		if k, ok := keyOfLine(l); ok && k.Fingerprint == fingerprint {
			removed++
			continue
		}
		b.WriteString(l)
	}
	return b.String(), removed
}

// ---- crontab ----

// ParseCrontab parses a crontab. systemFormat is /etc/crontab and /etc/cron.d,
// which have a user column after the schedule.
func ParseCrontab(content, source, owner string, systemFormat bool) []agenttask.CronEntry {
	var out []agenttask.CronEntry
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Environment assignments (SHELL=/bin/sh, MAILTO=…) are not jobs.
		if i := strings.IndexByte(line, '='); i > 0 && !strings.ContainsAny(line[:i], " \t") {
			continue
		}
		f := strings.Fields(line)
		var schedule string
		var rest []string
		if strings.HasPrefix(f[0], "@") {
			schedule, rest = f[0], f[1:]
		} else if len(f) >= 6 {
			schedule, rest = strings.Join(f[:5], " "), f[5:]
		} else {
			continue
		}
		user := owner
		if systemFormat {
			if len(rest) < 2 {
				continue
			}
			user, rest = rest[0], rest[1:]
		}
		if len(rest) == 0 {
			continue
		}
		out = append(out, agenttask.CronEntry{Source: source, User: user, Schedule: schedule, Command: clip(strings.Join(rest, " ")), Enabled: true})
	}
	return out
}
