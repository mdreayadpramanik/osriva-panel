//go:build windows

package service

import (
	"context"
	"errors"

	"golang.org/x/sys/windows/svc"
)

// IsService reports whether the process was started by the Service Control Manager.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// ErrRevoked makes the service exit with code 3 (see RestartPreventExitStatus on Linux).
var ErrRevoked = errors.New("agent revoked by the control plane")

// RunService runs fn under the SCM; fn must return when ctx is cancelled.
func RunService(fn func(ctx context.Context) error) error {
	return svc.Run(Name, &handler{fn: fn})
}

type handler struct {
	fn func(ctx context.Context) error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.fn(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			switch {
			case err == nil:
				return false, 0
			case errors.Is(err, ErrRevoked):
				return true, 3
			default:
				return true, 1
			}
		}
	}
}
