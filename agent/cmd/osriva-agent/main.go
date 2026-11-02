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
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/agent/internal/state"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentclient"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/compat"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
)

var version = "0.2.0-dev"

const usage = `OSRIVA Agent

Usage:
  osriva-agent inventory [--capabilities]   Print this host's inventory as JSON (no network)
  osriva-agent enroll --panel-url URL --token TOKEN --ca-fingerprint sha256:...
                                            Enrol this host (token may also come from OSRIVA_ENROLL_TOKEN)
  osriva-agent run [--once]                 Report to the control plane every 60 s
  osriva-agent status                       Show enrolment state and certificate expiry
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
	fmt.Printf("enrolled as %q (%s)\ncredentials saved in %s\nnext: run `osriva-agent run` (the Phase 2b installer will set this up as a service)\n",
		res.Response.ServerName, res.Response.ServerID, *dir)
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
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "osriva-agent")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, client, err := load(*dir)
	if err != nil {
		return err
	}
	log.Info("agent starting", "version", version, "server_id", st.Config.ServerID, "endpoint", st.Config.AgentEndpoint)

	backoff := 5 * time.Second
	for {
		next, err := cycle(ctx, log, st, &client)
		if *once {
			return err
		}
		wait := next
		if err != nil {
			var ae *agentclient.APIError
			if errors.As(err, &ae) && ae.Code == "agent_forbidden" {
				return fmt.Errorf("the control plane rejected this agent's certificate (server revoked?): %w", err)
			}
			log.Warn("report failed", "err", err, "retry_in", backoff)
			wait, backoff = backoff, min(backoff*2, 5*time.Minute)
		} else {
			backoff = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			log.Info("agent stopping")
			return nil
		case <-time.After(wait):
		}
	}
}

// cycle renews the certificate when due, then sends one report.
func cycle(ctx context.Context, log *slog.Logger, st *state.State, client **agentclient.Client) (time.Duration, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if due, err := agentclient.NeedsRenewal(st.CertPEM, time.Now()); err == nil && due {
		certPEM, keyPEM, err := (*client).Renew(cctx)
		if err != nil {
			log.Warn("certificate renewal failed; will retry", "err", err)
		} else if err := st.SaveCredentials(certPEM, keyPEM); err != nil {
			log.Error("saving renewed certificate failed", "err", err)
		} else if c, err := agentclient.New(st.Config.AgentEndpoint, st.CAPEM, certPEM, keyPEM); err == nil {
			*client = c
			log.Info("certificate renewed")
		}
	}
	resp, err := (*client).Report(cctx, agentproto.ReportRequest{AgentVersion: version, Inventory: inventory.Collect()})
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
