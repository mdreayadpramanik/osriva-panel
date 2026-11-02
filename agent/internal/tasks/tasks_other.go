//go:build !linux && !windows

package tasks

import (
	"context"
	"fmt"
	"runtime"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func unsupported() error {
	return fmt.Errorf("%w: %s has no service or process adapter yet", ErrUnsupported, runtime.GOOS)
}

func listServices(context.Context) (any, error)                   { return nil, unsupported() }
func serviceAction(context.Context, string, string) (any, error)  { return nil, unsupported() }
func listProcesses(context.Context) (any, error)                  { return nil, unsupported() }
func readLogs(context.Context, agenttask.LogsParams) (any, error) { return nil, unsupported() }

func listUsers(context.Context) (any, error)                    { return nil, unsupported() }
func listSSHKeys(context.Context, string) (any, error)          { return nil, unsupported() }
func addSSHKey(context.Context, string, string) (any, error)    { return nil, unsupported() }
func removeSSHKey(context.Context, string, string) (any, error) { return nil, unsupported() }
func listCron(context.Context) (any, error)                     { return nil, unsupported() }
func timeInfo(context.Context) (any, error)                     { return nil, unsupported() }
func setHostname(context.Context, string) (any, error)          { return nil, unsupported() }
func setTimezone(context.Context, string) (any, error)          { return nil, unsupported() }
