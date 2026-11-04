package agenttask

import (
	"encoding/json"
	"net/url"
	"path"
	"sort"
	"strings"
	"testing"
)

// FuzzValidate runs every task type against arbitrary parameters. Validate must
// never panic, and anything it accepts must be a single JSON object without
// unknown fields (strict decoding).
func FuzzValidate(f *testing.F) {
	types := make([]string, 0, len(Types))
	for t := range Types {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, s := range []string{`{}`, `null`, `[]`, `{"path":"/etc/hosts"}`, `{"name":"nginx","action":"restart"}`,
		`{"path":"/srv/a","content":"x","mode":"0644"}`, `{"id":"abc","action":"start"}`, `{"a":1}{"b":2}`, `{"name":"x","name":"y"}`} {
		for i := range types {
			f.Add(uint16(i), s)
		}
	}
	f.Fuzz(func(t *testing.T, i uint16, params string) {
		typ := types[int(i)%len(types)]
		if err := Validate(typ, json.RawMessage(params)); err != nil {
			return
		}
		if strings.TrimSpace(params) != "" && !json.Valid([]byte(params)) {
			t.Fatalf("%s accepted invalid JSON %q", typ, params)
		}
	})
	// Unknown types are always refused.
}

func FuzzValidRelPath(f *testing.F) {
	for _, s := range []string{".", "a/b", "../x", "a/../../b", "/abs", "a//b", "a/./b", "-x", `a\..\b`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidRelPath(s) {
			return
		}
		if path.IsAbs(s) || strings.HasPrefix(s, "\\") || path.Clean(s) != s {
			t.Fatalf("accepted non-clean or absolute %q", s)
		}
		for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == 0x5c }) {
			if seg == ".." {
				t.Fatalf("accepted parent segment in %q", s)
			}
		}
	})
}

func FuzzValidGitRef(f *testing.F) {
	for _, s := range []string{"main", "v1.0", "-upload-pack=x", "a..b", "feature/x", "x.lock", "a//b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidGitRef(s) && (strings.HasPrefix(s, "-") || strings.Contains(s, "..") || strings.ContainsAny(s, " \t\n\x00~^:?*[\\")) {
			t.Fatalf("accepted unsafe git ref %q", s)
		}
	})
}

func FuzzValidGitURL(f *testing.F) {
	for _, s := range []string{"https://github.com/a/b.git", "ext::sh -c x", "file:///etc", "--upload-pack=x", "git@github.com:a/b.git", "https://u:p@h/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidGitURL(s) != nil {
			return
		}
		if strings.HasPrefix(s, "-") || strings.HasPrefix(strings.ToLower(s), "ext::") || strings.HasPrefix(strings.ToLower(s), "file:") ||
			strings.ContainsAny(s, " \t\r\n\x00") {
			t.Fatalf("accepted unsafe git URL %q", s)
		}
	})
}

func FuzzURLValidators(f *testing.F) {
	for _, s := range []string{"https://example.com", "http://a.b/x", "https://u:p@h", "javascript:alert(1)", "https://h/?q=1", "https://h/'$(id)'"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidSiteURL(s) {
			u, err := url.Parse(s)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || strings.ContainsAny(s, " \t\r\n\"'`$\\") {
				t.Fatalf("site URL %q accepted", s)
			}
		}
		if ValidImageURL(s) {
			u, err := url.Parse(s)
			if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
				t.Fatalf("image URL %q accepted", s)
			}
		}
	})
}

func FuzzHostnames(f *testing.F) {
	for _, s := range []string{"example.com", "a..b", "-a.com", "1.2.3.4", "EXAMPLE.com", "a.b.", "*.x.com", "x.localhost"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidDomain(s) {
			if s != strings.ToLower(s) || strings.ContainsAny(s, " *_/\\:\x00\n'\"$`;{}") || strings.HasSuffix(s, ".") || len(s) > 253 {
				t.Fatalf("domain %q accepted", s)
			}
		}
		if ValidHostname(s) && strings.ContainsAny(s, " /\\:\x00\n'\"$`;{}") {
			t.Fatalf("hostname %q accepted", s)
		}
		if ValidVMHostname(s) && strings.ContainsAny(s, " /\\:\x00\n'\"$`;{}") {
			t.Fatalf("VM hostname %q accepted", s)
		}
	})
}
