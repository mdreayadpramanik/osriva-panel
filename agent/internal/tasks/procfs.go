package tasks

import (
	"fmt"
	"strconv"
	"strings"
)

// ProcStat holds the fields used from /proc/<pid>/stat.
type ProcStat struct {
	PID, PPID, Threads int
	Comm               string
	RSSPages           int64
}

// ParseProcPIDStat parses /proc/<pid>/stat. The command name is enclosed in
// parentheses and may itself contain spaces and parentheses, so fields are
// located relative to the LAST closing parenthesis.
func ParseProcPIDStat(content string) (ProcStat, error) {
	open, close := strings.IndexByte(content, '('), strings.LastIndexByte(content, ')')
	if open < 0 || close < open {
		return ProcStat{}, fmt.Errorf("malformed stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(content[:open]))
	if err != nil {
		return ProcStat{}, err
	}
	rest := strings.Fields(content[close+1:])
	// rest[0]=state(3) rest[1]=ppid(4) ... rest[17]=num_threads(20) ... rest[21]=rss(24)
	if len(rest) < 22 {
		return ProcStat{}, fmt.Errorf("short stat")
	}
	ppid, _ := strconv.Atoi(rest[1])
	threads, _ := strconv.Atoi(rest[17])
	rss, _ := strconv.ParseInt(rest[21], 10, 64)
	return ProcStat{PID: pid, PPID: ppid, Threads: threads, Comm: content[open+1 : close], RSSPages: rss}, nil
}

// ParseUID returns the real UID from /proc/<pid>/status.
func ParseUID(status string) (string, bool) {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "Uid:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				return f[1], true
			}
		}
	}
	return "", false
}

// CmdLine converts a NUL-separated /proc/<pid>/cmdline to a display string.
func CmdLine(raw []byte, max int) string {
	s := strings.TrimRight(strings.ReplaceAll(string(raw), "\x00", " "), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
