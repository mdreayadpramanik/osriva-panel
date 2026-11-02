// Command osriva-agent runs on a managed server: it enrols with the OSRIVA
// control plane and reports the host inventory over mutual TLS. In Phase 2 it
// executes no commands from the control plane.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/agent/internal/service"
	"github.com/mdreayadpramanik/osriva-panel/agent/internal/state"
	"github.com/mdreayadpramanik/osriva-panel/agent/internal/tasks"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentclient"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/compat"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
)

var version = "0.3.0-dev"

const usage = `OSRIVA Agent

Usage:
  osriva-agent inventory [--capabilities]   Print this host's inventory as JSON (no network)
  osriva-agent enroll --panel-url URL --token TOKEN --ca-fingerprint sha256:...
                                            Enrol this host (token may also come from OSRIVA_ENROLL_TOKEN)
  osriva-agent run [--once]                 Report to the control plane every 60 s
  osriva-agent status                       Show enrolment state and certificate expiry
  osriva-agent service install|uninstall|status
                                            Run the agent as a system service (root / Administrator)
  osriva-agent version

All commands except inventory accept --config-dir (default: ` + "%s" + `).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, state.DefaultDir())
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "inventory":
		err = cmdInventory(os.Args[2:])
	case "enroll":
		err = cmdEnroll(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "service":
		err = cmdService(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Printf(usage, state.DefaultDir())
	default:
		fmt.Fprintf(os.Stderr, usage, state.DefaultDir())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, service.ErrRevoked) {
			os.Exit(3) // service managers are configured not to restart on 3
		}
		os.Exit(1)
	}
}

func cmdInventory(args []string) error {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	withCaps := fs.Bool("capabilities", false, "also evaluate compatibility locally")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inv := inventory.Collect()
	var out any = inv
	if *withCaps {
		out = struct {
			Inventory    *inventory.Inventory `json:"inventory"`
			Capabilities []compat.Capability  `json:"capabilities"`
		}{inv, compat.Evaluate(inv)}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	panelURL := fs.String("panel-url", "", "panel base URL, e.g. https://panel.example.com")
	token := fs.String("token", os.Getenv("OSRIVA_ENROLL_TOKEN"), "one-time enrolment token")
	fp := fs.String("ca-fingerprint", "", "expected CA fingerprint (sha256:...)")
	dir := fs.String("config-dir", state.DefaultDir(), "agent state directory")
	force := fs.Bool("force", false, "replace an existing enrolment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *panelURL == "" || *token == "" || *fp == "" {
		return errors.New("--panel-url, --token and --ca-fingerprint are required")
	}
	if state.Exists(*dir) && !*force {
		return fmt.Errorf("this host is already enrolled (%s). Use --force to replace the enrolment", *dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := agentclient.Enroll(ctx, *panelURL, *token, *fp, version, nil)
	if err != nil {
		return err
	}
	st := &state.State{Dir: *dir, CAPEM: []byte(res.Response.CAPEM), CertPEM: []byte(res.Response.CertificatePEM), KeyPEM: res.KeyPEM,
		Config: state.Config{ServerID: res.Response.ServerID, ServerName: res.Response.ServerName, PanelURL: strings.TrimRight(*panelURL, "/"),
			AgentEndpoint: res.Response.AgentEndpoint, CAFingerprint: *fp}}
	if err := st.Save(); err != nil {
		return fmt.Errorf("enrolled, but saving credentials failed (revoke this server in the panel and retry): %w", err)
	}
	fmt.Printf("enrolled as %q (%s)\ncredentials saved in %s\n", res.Response.ServerName, res.Response.ServerID, *dir)
	if !st.Secured {
		fmt.Println("warning: not elevated, so the credential directory was not restricted to administrators")
	}
	fmt.Println("next: `osriva-agent service install` (as root / Administrator) to run it as a service")
	return nil
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: osriva-agent service install|uninstall|status [--config-dir DIR]")
	}
	fs := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	dir := fs.String("config-dir", state.DefaultDir(), "agent state directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "install":
		// Refuse to install a service that would only crash-loop.
		if _, err := state.Load(*dir); err != nil {
			return fmt.Errorf("cannot install the service: %w", err)
		}
		if err := service.Install(service.Options{ConfigDir: *dir}); err != nil {
			return err
		}
		st, _ := service.Query()
		fmt.Printf("service %s installed (%s), binary %s\n", service.Name, st, service.BinaryPath())
	case "uninstall":
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Printf("service %s removed (credentials in %s were kept)\n", service.Name, *dir)
	case "status":
		st, err := service.Query()
		if err != nil {
			return err
		}
		fmt.Println(st)
	default:
		return fmt.Errorf("unknown service action %q", args[0])
	}
	return nil
}

func load(dir string) (*state.State, *agentclient.Client, error) {
	st, err := state.Load(dir)
	if err != nil {
		return nil, nil, err
	}
	c, err := agentclient.New(st.Config.AgentEndpoint, st.CAPEM, st.CertPEM, st.KeyPEM)
	if err != nil {
		return nil, nil, err
	}
	return st, c, nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dir := fs.String("config-dir", state.DefaultDir(), "agent state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := state.Load(*dir)
	if err != nil {
		return err
	}
	renew, err := agentclient.NeedsRenewal(st.CertPEM, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("server:     %s (%s)\npanel:      %s\nendpoint:   %s\nCA:         %s\nrenew soon: %v\n",
		st.Config.ServerName, st.Config.ServerID, st.Config.PanelURL, st.Config.AgentEndpoint, st.Config.CAFingerprint, renew)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dir := fs.String("config-dir", state.DefaultDir(), "agent state directory")
	once := fs.Bool("once", false, "send a single report and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logOut := io.Writer(os.Stderr)
	if service.IsService() {
		// The Windows SCM discards stderr; log next to the credentials instead.
		if f, err := os.OpenFile(filepath.Join(*dir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			logOut = f
		}
	}
	log := slog.New(slog.NewTextHandler(logOut, nil)).With("component", "osriva-agent")
	if service.IsService() {
		return service.RunService(func(ctx context.Context) error { return runLoop(ctx, log, *dir, false) })
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runLoop(ctx, log, *dir, *once)
}

func runLoop(ctx context.Context, log *slog.Logger, dir string, once bool) error {
	st, client, err := load(dir)
	if err != nil {
		log.Error("cannot start", "err", err)
		return err
	}
	log.Info("agent starting", "version", version, "server_id", st.Config.ServerID, "endpoint", st.Config.AgentEndpoint)

	h := &clientHolder{c: client}
	if once {
		_, err := cycle(ctx, log, st, h)
		if isRevoked(err) {
			return revokedErr(log, err)
		}
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	revoked := make(chan error, 3)
	go metricsWorker(ctx, log, h, revoked)
	go taskWorker(ctx, log, h, revoked)

	backoff := 5 * time.Second
	for {
		next, err := cycle(ctx, log, st, h)
		if isRevoked(err) {
			return revokedErr(log, err)
		}
		wait := next
		if err != nil {
			log.Warn("report failed", "err", err, "retry_in", backoff)
			wait, backoff = backoff, min(backoff*2, 5*time.Minute)
		} else {
			backoff = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			log.Info("agent stopping")
			return nil
		case err := <-revoked:
			return revokedErr(log, err)
		case <-time.After(wait):
		}
	}
}

// clientHolder shares the current client between workers; renewal swaps it.
type clientHolder struct {
	mu sync.RWMutex
	c  *agentclient.Client
}

func (h *clientHolder) get() *agentclient.Client  { h.mu.RLock(); defer h.mu.RUnlock(); return h.c }
func (h *clientHolder) set(c *agentclient.Client) { h.mu.Lock(); h.c = c; h.mu.Unlock() }

func isRevoked(err error) bool {
	var ae *agentclient.APIError
	return err != nil && errors.As(err, &ae) && ae.Code == "agent_forbidden"
}

func revokedErr(log *slog.Logger, err error) error {
	log.Error("the control plane rejected this agent's certificate; it was probably revoked", "err", err)
	return fmt.Errorf("%w: %v", service.ErrRevoked, err)
}

// metricsWorker samples every 10 s and uploads every 30 s, keeping a bounded
// buffer while the control plane is unreachable.
func metricsWorker(ctx context.Context, log *slog.Logger, h *clientHolder, revoked chan<- error) {
	col := metrics.NewCollector()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	var buf []metrics.Sample
	for n := 1; ; n++ {
		buf = append(buf, col.Sample())
		if len(buf) > agentproto.MaxMetricsBatch {
			buf = buf[len(buf)-agentproto.MaxMetricsBatch:]
		}
		if n%3 == 0 {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := h.get().SendMetrics(cctx, buf)
			cancel()
			switch {
			case err == nil:
				buf = buf[:0]
			case isRevoked(err):
				revoked <- err
				return
			case ctx.Err() == nil:
				log.Warn("metrics upload failed; keeping samples", "err", err, "buffered", len(buf))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// taskWorker long-polls for tasks and executes them one at a time.
func taskWorker(ctx context.Context, log *slog.Logger, h *clientHolder, revoked chan<- error) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		list, err := h.get().PollTasks(ctx)
		if err != nil {
			if isRevoked(err) {
				revoked <- err
				return
			}
			if ctx.Err() != nil {
				return
			}
			log.Warn("task poll failed", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = 5 * time.Second
		for _, t := range list {
			res := agentproto.TaskResult{Status: "succeeded"}
			out, err := tasks.Execute(ctx, t.Type, t.Params)
			if err == nil {
				res.Result, err = json.Marshal(out)
			}
			if err != nil {
				res = agentproto.TaskResult{Status: "failed", Error: err.Error()}
			}
			log.Info("task done", "task_id", t.ID, "type", t.Type, "status", res.Status)
			for attempt := 0; attempt < 3; attempt++ {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err = h.get().SendResult(cctx, t.ID, res)
				cancel()
				if err == nil || isRevoked(err) || ctx.Err() != nil {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			}
			if isRevoked(err) {
				revoked <- err
				return
			}
			if err != nil {
				log.Warn("could not deliver task result", "task_id", t.ID, "err", err)
			}
		}
	}
}

// cycle renews the certificate when due, then sends one report.
func cycle(ctx context.Context, log *slog.Logger, st *state.State, h *clientHolder) (time.Duration, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if due, err := agentclient.NeedsRenewal(st.CertPEM, time.Now()); err == nil && due {
		certPEM, keyPEM, err := h.get().Renew(cctx)
		if err != nil {
			log.Warn("certificate renewal failed; will retry", "err", err)
		} else if err := st.SaveCredentials(certPEM, keyPEM); err != nil {
			log.Error("saving renewed certificate failed", "err", err)
		} else if c, err := agentclient.New(st.Config.AgentEndpoint, st.CAPEM, certPEM, keyPEM); err == nil {
			h.set(c)
			log.Info("certificate renewed")
		}
	}
	resp, err := h.get().Report(cctx, agentproto.ReportRequest{AgentVersion: version, Inventory: inventory.Collect()})
	if err != nil {
		return 0, err
	}
	next := time.Duration(resp.NextReportSeconds) * time.Second
	if next < 15*time.Second || next > time.Hour {
		next = agentproto.DefaultReportInterval
	}
	log.Debug("report sent", "next_in", next)
	return next, nil
}
