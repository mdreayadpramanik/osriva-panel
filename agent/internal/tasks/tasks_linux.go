//go:build linux

package tasks

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func hasSystemd() bool { _, err := os.Stat("/run/systemd/system"); return err == nil }

func listServices(ctx context.Context) (any, error) {
	if !hasSystemd() {
		return nil, fmt.Errorf("%w: service management is implemented for systemd only (OpenRC is planned)", ErrUnsupported)
	}
	units, err := run(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--plain", "--output=json")
	if err != nil {
		return nil, err
	}
	files, err := run(ctx, "systemctl", "list-unit-files", "--type=service", "--no-pager", "--plain", "--output=json")
	if err != nil {
		return nil, err
	}
	svcs, err := ParseSystemd(units, files)
	if err != nil {
		return nil, fmt.Errorf("parse systemctl output: %w", err)
	}
	return agenttask.ServicesResult{Manager: "systemd", Services: svcs}, nil
}

func serviceAction(ctx context.Context, name, action string) (any, error) {
	if !hasSystemd() {
		return nil, fmt.Errorf("%w: service management is implemented for systemd only (OpenRC is planned)", ErrUnsupported)
	}
	// "--" ends option parsing, so a name can never be read as a flag.
	if _, err := run(ctx, "systemctl", action, "--no-ask-password", "--", name); err != nil {
		return nil, err
	}
	out, err := run(ctx, "systemctl", "show", "--property=ActiveState,UnitFileState", "--", name)
	if err != nil {
		return nil, err
	}
	p := ParseSystemctlShow(out)
	return agenttask.ServiceActionResult{Name: name, Action: action,
		State: normalizeSystemdState(p["ActiveState"]), Startup: normalizeSystemdStartup(p["UnitFileState"])}, nil
}

func listProcesses(ctx context.Context) (any, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	page := int64(os.Getpagesize())
	users := map[string]string{}
	res := agenttask.ProcessesResult{Processes: []agenttask.Process{}}
	for _, e := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue // process exited meanwhile
		}
		st, err := ParseProcPIDStat(string(raw))
		if err != nil {
			continue
		}
		p := agenttask.Process{PID: st.PID, PPID: st.PPID, Name: st.Comm, Threads: st.Threads}
		if st.RSSPages > 0 {
			p.RSSBytes = uint64(st.RSSPages * page)
		}
		if status, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
			if uid, ok := ParseUID(string(status)); ok {
				name, cached := users[uid]
				if !cached {
					name = uid
					if u, err := user.LookupId(uid); err == nil {
						name = u.Username
					}
					users[uid] = name
				}
				p.User = name
			}
		}
		if cl, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			p.Command = CmdLine(cl, 300)
		}
		res.Processes = append(res.Processes, p)
	}
	sort.Slice(res.Processes, func(i, j int) bool { return res.Processes[i].RSSBytes > res.Processes[j].RSSBytes })
	if len(res.Processes) > agenttask.MaxProcesses {
		res.Processes, res.Truncated = res.Processes[:agenttask.MaxProcesses], true
	}
	return res, nil
}
