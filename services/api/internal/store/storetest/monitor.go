package storetest

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testMonitor(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	org := store.DefaultOrganizationID
	a, _ := newServer(t, s, "mon-a", now)

	hook := &store.NotificationChannel{ID: auth.NewID(), OrganizationID: org, Name: "ops hook", Kind: "webhook",
		Config: json.RawMessage(`{"url":"https://hooks.example.com/x"}`), SecretEnc: []byte{9}, Enabled: true}
	mail := &store.NotificationChannel{ID: auth.NewID(), OrganizationID: org, Name: "ops mail", Kind: "email",
		Config: json.RawMessage(`{"host":"smtp.example.com","port":587}`), Enabled: true}
	for _, c := range []*store.NotificationChannel{hook, mail} {
		if err := s.CreateChannel(ctx(), c); err != nil {
			t.Fatal(err)
		}
	}
	upd := *hook
	upd.Name, upd.Enabled, upd.Kind = "renamed", false, "email"
	if err := s.UpdateChannel(ctx(), &upd); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetChannel(ctx(), hook.ID); got.Name != "renamed" || got.Enabled || got.Kind != "webhook" || got.SecretEnc[0] != 9 {
		t.Fatalf("UpdateChannel (kind immutable): %+v", got)
	}

	rule := &store.AlertRule{ID: auth.NewID(), OrganizationID: org, Name: "cpu high", ServerID: &a.ID, Metric: "cpu", Threshold: 90,
		DurationMinutes: 5, ChannelIDs: []string{hook.ID, mail.ID}, Enabled: true}
	if err := s.CreateAlertRule(ctx(), rule); err != nil {
		t.Fatal(err)
	}
	all := &store.AlertRule{ID: auth.NewID(), OrganizationID: org, Name: "any offline", Metric: "offline", Threshold: 5, DurationMinutes: 1, Enabled: true}
	if err := s.CreateAlertRule(ctx(), all); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAlertRule(ctx(), all.ID); got.ServerID != nil || got.ChannelIDs == nil || len(got.ChannelIDs) != 0 {
		t.Fatalf("rule for all servers: %+v", got)
	}
	if err := s.DeleteChannel(ctx(), hook.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAlertRule(ctx(), rule.ID); len(got.ChannelIDs) != 1 || got.ChannelIDs[0] != mail.ID {
		t.Fatalf("deleted channel must leave the rules: %+v", got.ChannelIDs)
	}
	if list, _ := s.ListAlertRules(ctx(), ""); len(list) < 2 {
		t.Fatalf("all rules: %d", len(list))
	}
	r2 := *rule
	r2.Threshold, r2.Enabled = 80, false
	if err := s.UpdateAlertRule(ctx(), &r2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAlertRule(ctx(), rule.ID); got.Threshold != 80 || got.Enabled {
		t.Fatalf("UpdateAlertRule: %+v", got)
	}

	if _, err := s.GetAlertState(ctx(), rule.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no state yet: %v", err)
	}
	st := &store.AlertState{RuleID: rule.ID, ServerID: a.ID, Firing: true, Since: now, Value: 95.5, UpdatedAt: now}
	if err := s.PutAlertState(ctx(), st); err != nil {
		t.Fatal(err)
	}
	st.Firing, st.Value = false, 10
	if err := s.PutAlertState(ctx(), st); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAlertState(ctx(), rule.ID, a.ID); got.Firing || got.Value != 10 {
		t.Fatalf("state upsert: %+v", got)
	}
	states, err := s.ListAlertStates(ctx())
	found := 0
	for _, x := range states {
		if x.RuleID == rule.ID && x.ServerID == a.ID && !x.Firing && x.Value == 10 {
			found++
		}
	}
	if err != nil || found != 1 {
		t.Fatalf("ListAlertStates: %+v %v", states, err)
	}
	// Panel-wide rules keep one state without a server; upserts replace it.
	if _, err := s.GetAlertState(ctx(), rule.ID, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no panel-wide state yet: %v", err)
	}
	for _, firing := range []bool{true, false} {
		if err := s.PutAlertState(ctx(), &store.AlertState{RuleID: rule.ID, Firing: firing, Since: now, Value: 30, UpdatedAt: now}); err != nil {
			t.Fatalf("panel-wide state: %v", err)
		}
	}
	if got, err := s.GetAlertState(ctx(), rule.ID, ""); err != nil || got.ServerID != "" || got.Firing {
		t.Fatalf("panel-wide state: %+v %v", got, err)
	}
	states, _ = s.ListAlertStates(ctx())
	panelWide := 0
	for _, x := range states {
		if x.RuleID == rule.ID && x.ServerID == "" {
			panelWide++
		}
	}
	if panelWide != 1 {
		t.Fatalf("panel-wide states: %+v", states)
	}

	ev := &store.AlertEvent{ID: auth.NewID(), OrganizationID: org, RuleID: &rule.ID, RuleName: rule.Name, ServerID: &a.ID, ServerName: a.Name,
		Metric: "cpu", State: "firing", Value: 95.5, Threshold: 90}
	if err := s.InsertAlertEvent(ctx(), ev); err != nil || ev.CreatedAt.IsZero() {
		t.Fatalf("InsertAlertEvent: %v", err)
	}
	if err := s.SetAlertEventDelivery(ctx(), ev.ID, 1, 1, "smtp: connection refused"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAlertRule(ctx(), rule.ID); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.ListAlertEvents(ctx(), org, 10)
	if len(evs) == 0 || evs[0].ID != ev.ID || evs[0].RuleID != nil || evs[0].RuleName != "cpu high" || evs[0].Failed != 1 || evs[0].DeliveryError == "" {
		t.Fatalf("events survive their rule: %+v", evs)
	}

	h := now.Truncate(time.Hour)
	for i := 0; i < 3; i++ {
		if err := s.UpsertRollup(ctx(), &store.MetricRollup{ServerID: a.ID, Hour: h.Add(-time.Duration(i) * time.Hour), Data: json.RawMessage(`{"cpu_avg":1}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertRollup(ctx(), &store.MetricRollup{ServerID: a.ID, Hour: h, Data: json.RawMessage(`{"cpu_avg":2}`)}); err != nil {
		t.Fatal(err)
	}
	ro, _ := s.ListRollups(ctx(), a.ID, h.Add(-90*time.Minute))
	if len(ro) != 2 || !ro[1].Hour.Equal(h) || string(ro[1].Data) == `{"cpu_avg":1}` {
		t.Fatalf("rollups: %+v", ro)
	}
	if n, err := s.PruneRollups(ctx(), h.Add(-90*time.Minute)); err != nil || n != 1 {
		t.Fatalf("PruneRollups: %d %v", n, err)
	}

	old := &store.AuditEntry{OrganizationID: &org, Action: "test.old", Outcome: store.OutcomeSuccess}
	if err := s.InsertAudit(ctx(), old); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneAudit(ctx(), time.Now().Add(time.Hour)); err != nil || n < 1 {
		t.Fatalf("PruneAudit: %d %v", n, err)
	}
	if left, _ := s.ListAudit(ctx(), store.AuditFilter{OrganizationID: org, Limit: 10}); len(left) != 0 {
		t.Fatalf("audit left: %d", len(left))
	}
}
