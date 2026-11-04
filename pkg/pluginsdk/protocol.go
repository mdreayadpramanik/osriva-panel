// SPDX-License-Identifier: Apache-2.0

// Package pluginsdk is the SDK for OSRIVA Panel plugins (docs/adr/0016-plugins.md).
//
// A plugin is an executable. For every call the panel starts it, writes one JSON
// request to stdin, closes stdin and reads one JSON response from stdout. The
// environment holds nothing of the panel's; secrets arrive only in the request.
//
//	func main() {
//		pluginsdk.Serve(pluginsdk.Handlers{
//			Describe: func() pluginsdk.Description { return pluginsdk.Description{Name: "my-plugin", Version: "1.0.0", Kinds: []string{pluginsdk.KindNotifier}} },
//			Notify:   func(ctx context.Context, n pluginsdk.Notification) error { … },
//		})
//	}
package pluginsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Protocol is the protocol version spoken by this SDK.
const Protocol = 1

const (
	KindNotifier = "notifier"

	MethodDescribe = "describe"
	MethodNotify   = "notify"

	// MaxRequestBytes bounds what a plugin reads from stdin.
	MaxRequestBytes = 1 << 20
)

type Request struct {
	Protocol int             `json:"protocol"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a plugin failure. Permanent errors are not retried by the panel.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Permanent bool   `json:"permanent,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Permanent wraps err so the panel does not retry the call (e.g. a bad URL or a 4xx answer).
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: "permanent", Message: err.Error(), Permanent: true}
}

type Description struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Kinds   []string `json:"kinds"`
}

// Event is an alert notification (the same fields as OSRIVA's signed webhooks).
type Event struct {
	Event     string  `json:"event"` // alert.firing | alert.resolved | alert.test
	ID        string  `json:"id"`
	Rule      string  `json:"rule"`
	Server    string  `json:"server"`
	ServerID  string  `json:"server_id,omitempty"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Message   string  `json:"message"`
	At        string  `json:"at"`
}

// Notification is the params of a notify call.
type Notification struct {
	Event Event `json:"event"`
	// Settings is the channel configuration chosen by the operator (plugin-defined JSON).
	Settings json.RawMessage `json:"settings"`
	// Secret is the channel secret (e.g. a token); empty when none is set.
	Secret string `json:"secret,omitempty"`
}

type Handlers struct {
	Describe func() Description
	Notify   func(ctx context.Context, n Notification) error
}

// Serve handles one request from stdin and writes the response to stdout.
func Serve(h Handlers) {
	resp := handle(os.Stdin, h)
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	if !resp.OK {
		os.Exit(1)
	}
}

func handle(r io.Reader, h Handlers) Response {
	fail := func(code, msg string, permanent bool) Response {
		return Response{Error: &Error{Code: code, Message: msg, Permanent: permanent}}
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return fail("bad_request", "cannot read the request", true)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fail("bad_request", "the request is not JSON", true)
	}
	if req.Protocol != Protocol {
		return fail("unsupported_protocol", fmt.Sprintf("this plugin speaks protocol %d, not %d", Protocol, req.Protocol), true)
	}
	// The panel enforces its own timeout; this one stops runaway handlers early.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch req.Method {
	case MethodDescribe:
		if h.Describe == nil {
			return fail("unsupported_method", "describe is not implemented", true)
		}
		out, _ := json.Marshal(h.Describe())
		return Response{OK: true, Result: out}
	case MethodNotify:
		if h.Notify == nil {
			return fail("unsupported_method", "this plugin is not a notifier", true)
		}
		var n Notification
		if err := json.Unmarshal(req.Params, &n); err != nil {
			return fail("bad_request", "invalid notify params", true)
		}
		if err := h.Notify(ctx, n); err != nil {
			var pe *Error
			if errors.As(err, &pe) {
				return Response{Error: pe}
			}
			return fail("failed", err.Error(), false)
		}
		return Response{OK: true, Result: json.RawMessage(`{}`)}
	}
	return fail("unsupported_method", "unknown method "+req.Method, true)
}
