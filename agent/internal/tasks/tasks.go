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
