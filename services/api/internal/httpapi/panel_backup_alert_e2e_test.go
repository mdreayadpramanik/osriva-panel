package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
)

// The control plane's own backups: the installer records each success in a
// setting; a panel_backup rule fires when none happened within the limit and
// resolves after the next one.
func TestPanelBackupAlert(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	mon := monitor.New(e.store(), e.svc, keys, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	smtpAddr, mails := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(smtpAddr)
	em, err := mon.CreateChannel(ctx, e.owner, monitor.ChannelInput{Name: "ops mail", Kind: "email", Enabled: true,
		Config: json.RawMessage(`{"host":"` + host + `","port":` + port + `,"tls":"none","from":"osriva@example.com","to":["ops@example.com"]}`)})
	if err != nil {
		t.Fatal(err)
	}

	var ve *identity.ValidationError
	res, _ := e.enroll(e.addServer("pb-1"))
	srv := res.Response.ServerID
	for _, bad := range []monitor.RuleInput{
		{Name: "pb", Metric: "panel_backup", Threshold: 0, DurationMinutes: 1},
		{Name: "pb", Metric: "panel_backup", Threshold: 721, DurationMinutes: 1},
		{Name: "pb", Metric: "panel_backup", Threshold: 26, DurationMinutes: 1, ServerID: &srv},
	} {
		if _, err := mon.CreateRule(ctx, e.owner, bad); !errors.As(err, &ve) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	rule, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "panel backups", Metric: "panel_backup", Threshold: 26,
		DurationMinutes: 1, ChannelIDs: []string{em.ID}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	created := rule.CreatedAt

	// Never backed up: counted from the rule's creation, so nothing fires at first.
	mon.Evaluate(ctx, created.Add(time.Hour))
	if evs, _ := mon.ListEvents(ctx, e.owner, 10); len(evs) != 0 {
		t.Fatalf("fired before the limit: %+v", evs)
	}
	mon.Evaluate(ctx, created.Add(27*time.Hour))
	mon.Evaluate(ctx, created.Add(27*time.Hour+time.Minute)) // no duplicate
	evs, _ := mon.ListEvents(ctx, e.owner, 10)
	if len(evs) != 1 || evs[0].State != "firing" || evs[0].Metric != "panel_backup" || evs[0].ServerID != nil ||
		evs[0].ServerName != "control plane" || evs[0].Value < 26 || evs[0].Delivered != 1 {
		t.Fatalf("firing: %+v", evs)
	}
	if m := mails(); len(m) != 1 || !strings.Contains(m[0], "no successful backup of the panel itself") {
		t.Fatalf("mail: %q", m)
	}

	// The installer records a successful backup: the alert resolves.
	at := created.Add(27*time.Hour + 2*time.Minute)
	if err := e.store().PutSetting(ctx, monitor.SettingPanelBackup, json.RawMessage(fmt.Sprintf(`{"at_unix":%d,"name":"20261104T030000Z"}`, at.Unix())), nil); err != nil {
		t.Fatal(err)
	}
	mon.Evaluate(ctx, at.Add(time.Minute))
	if evs, _ = mon.ListEvents(ctx, e.owner, 10); len(evs) != 2 || evs[0].State != "resolved" {
		t.Fatalf("resolved: %+v", evs)
	}
	// An unreadable status changes nothing (no flapping on bad data).
	_ = e.store().PutSetting(ctx, monitor.SettingPanelBackup, json.RawMessage(`{"at_unix":"soon"}`), nil)
	mon.Evaluate(ctx, at.Add(100*time.Hour))
	if evs, _ = mon.ListEvents(ctx, e.owner, 10); len(evs) != 2 {
		t.Fatalf("bad status changed the state: %+v", evs)
	}
}
