// Package tasks executes the typed tasks of pkg/agenttask on this host. Every
// task is re-validated here, independently of the control plane, before it
// touches the system. Platform tools are invoked with explicit arguments,
// never through a shell.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// ErrUnsupported reports a task this platform cannot perform (yet).
var ErrUnsupported = errors.New("not supported on this host")

// Timeout bounds every task.
const Timeout = 60 * time.Second

// Execute validates and runs one task, returning a JSON-encodable result.
func Execute(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	if err := agenttask.Validate(typ, params); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	switch typ {
	case agenttask.ServicesList:
		return listServices(ctx)
	case agenttask.ServiceAction:
		var p agenttask.ServiceActionParams
		_ = json.Unmarshal(params, &p) // already validated strictly
		return serviceAction(ctx, p.Name, p.Action)
	case agenttask.ProcessesList:
		return listProcesses(ctx)
	case agenttask.LogsRead:
		var p agenttask.LogsParams
		_ = json.Unmarshal(params, &p)
		if p.Lines == 0 {
			p.Lines = 200
		}
		return readLogs(ctx, p)
	}
	if agenttask.FileTask(typ) {
		return fileTask(ctx, currentGuard(), typ, params)
	}
	if out, ok, err := systemTask(ctx, typ, params); ok {
		return out, err
	}
	return nil, fmt.Errorf("%w: %s", agenttask.ErrInvalid, typ)
}

func fileTask(ctx context.Context, g *FSGuard, typ string, params json.RawMessage) (any, error) {
	switch typ {
	case agenttask.FilesWrite:
		var p agenttask.FilesWriteParams
		_ = json.Unmarshal(params, &p)
		return filesWrite(ctx, g, p)
	}
	var p agenttask.PathParams
	_ = json.Unmarshal(params, &p)
	switch typ {
	case agenttask.FilesList:
		return filesList(ctx, g, p.Path)
	case agenttask.FilesRead:
		return filesRead(ctx, g, p.Path)
	case agenttask.FilesMkdir:
		return filesMkdir(ctx, g, p.Path)
	case agenttask.FilesDelete:
		return filesDelete(ctx, g, p.Path)
	}
	return nil, fmt.Errorf("%w: %s", agenttask.ErrInvalid, typ)
}

// run executes a tool and returns stdout; stderr is included in errors.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return out, nil
}

func systemTask(ctx context.Context, typ string, params json.RawMessage) (any, bool, error) {
	var out any
	var err error
	switch typ {
	case agenttask.UsersList:
		out, err = listUsers(ctx)
	case agenttask.CronList:
		out, err = listCron(ctx)
	case agenttask.TimeInfo:
		out, err = timeInfo(ctx)
	case agenttask.SSHKeysList:
		var p agenttask.UserParams
		_ = json.Unmarshal(params, &p)
		out, err = listSSHKeys(ctx, p.User)
	case agenttask.SSHKeysAdd:
		var p agenttask.SSHKeyAddParams
		_ = json.Unmarshal(params, &p)
		out, err = addSSHKey(ctx, p.User, p.Key)
	case agenttask.SSHKeysRemove:
		var p agenttask.SSHKeyRemoveParams
		_ = json.Unmarshal(params, &p)
		out, err = removeSSHKey(ctx, p.User, p.Fingerprint)
	case agenttask.HostnameSet:
		var p agenttask.HostnameParams
		_ = json.Unmarshal(params, &p)
		out, err = setHostname(ctx, p.Hostname)
	case agenttask.TimezoneSet:
		var p agenttask.TimezoneParams
		_ = json.Unmarshal(params, &p)
		out, err = setTimezone(ctx, p.Timezone)
	default:
		return nil, false, nil
	}
	return out, true, err
}
