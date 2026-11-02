// Package agenttask defines the closed set of typed tasks the control plane
// may send to an agent (docs/adr/0005-agent-tasks-and-metrics.md). Both sides
// validate every task with Validate; there is deliberately no generic
// "run a command" task.
package agenttask

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const (
	ServicesList  = "services.list"
	ServiceAction = "service.action"
	ProcessesList = "processes.list"
)

// Types is the complete allowlist.
var Types = map[string]bool{ServicesList: true, ServiceAction: true, ProcessesList: true}

// ReadOnly reports whether a task type only reads state.
func ReadOnly(typ string) bool { return typ == ServicesList || typ == ProcessesList }

const (
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
	ActionEnable  = "enable"
	ActionDisable = "disable"
)

var actions = map[string]bool{ActionStart: true, ActionStop: true, ActionRestart: true, ActionEnable: true, ActionDisable: true}

// Disruptive actions need recent re-authentication on the control plane.
func Disruptive(action string) bool { return action == ActionStop || action == ActionDisable }

// ServiceName allows systemd unit names (incl. templates like getty@tty1), OpenRC
// and Windows service names. It forbids path separators, whitespace and shell metacharacters.
var ServiceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:+-]{0,127}$`)

type ServiceActionParams struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

var ErrInvalid = errors.New("invalid task")

func decodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: params: %v", ErrInvalid, err)
	}
	return nil
}

// Validate checks the type against the allowlist and the parameters strictly.
func Validate(typ string, params json.RawMessage) error {
	if !Types[typ] {
		return fmt.Errorf("%w: unknown task type %q", ErrInvalid, typ)
	}
	switch typ {
	case ServicesList, ProcessesList:
		var empty struct{}
		return decodeStrict(params, &empty)
	case ServiceAction:
		var p ServiceActionParams
		if err := decodeStrict(params, &p); err != nil {
			return err
		}
		if !ServiceName.MatchString(p.Name) {
			return fmt.Errorf("%w: service name %q is not allowed", ErrInvalid, p.Name)
		}
		if !actions[p.Action] {
			return fmt.Errorf("%w: action must be start, stop, restart, enable or disable", ErrInvalid)
		}
	}
	return nil
}

// ---- results ----

type Service struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// State is normalised across platforms: running, stopped, failed, starting, stopping, unknown.
	State string `json:"state"`
	// Detail is the platform's own state (e.g. systemd "active (running)", Windows "START_PENDING").
	Detail string `json:"detail,omitempty"`
	// Startup: enabled, disabled, manual, static, unknown.
	Startup string `json:"startup"`
}

type ServicesResult struct {
	Manager  string    `json:"manager"`
	Services []Service `json:"services"`
}

type ServiceActionResult struct {
	Name    string `json:"name"`
	Action  string `json:"action"`
	State   string `json:"state"`
	Startup string `json:"startup"`
}

type Process struct {
	PID      int    `json:"pid"`
	PPID     int    `json:"ppid"`
	Name     string `json:"name"`
	User     string `json:"user,omitempty"`
	RSSBytes uint64 `json:"rss_bytes,omitempty"`
	Threads  int    `json:"threads,omitempty"`
	Command  string `json:"command,omitempty"`
}

type ProcessesResult struct {
	Processes []Process `json:"processes"`
	// Truncated is set when the list was cut to MaxProcesses.
	Truncated bool `json:"truncated,omitempty"`
}

const MaxProcesses = 2000
