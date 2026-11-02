//go:build !windows

package service

import (
	"context"
	"errors"
)

// ErrRevoked makes the agent exit with code 3, which the systemd unit does not restart.
var ErrRevoked = errors.New("agent revoked by the control plane")

// IsService is always false outside Windows: Unix service managers just run the process.
func IsService() bool { return false }

func RunService(fn func(ctx context.Context) error) error { return fn(context.Background()) }
