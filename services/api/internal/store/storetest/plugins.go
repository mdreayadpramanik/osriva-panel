package storetest

import (
	"errors"
	"strings"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testPlugins(t *testing.T, s store.Store) {
	u := newUser(t, s, "plugin-owner@example.com", "owner")
	if _, err := s.GetPluginApproval(ctx(), "chat-notifier"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no approval yet: %v", err)
	}
	a := &store.PluginApproval{Name: "chat-notifier", Enabled: true, ApprovedSHA256: strings.Repeat("a", 64), ApprovedBy: &u.ID}
	if err := s.SetPluginApproval(ctx(), a); err != nil || a.ApprovedAt.IsZero() {
		t.Fatalf("approve: %v", err)
	}
	a.Enabled, a.ApprovedSHA256 = false, strings.Repeat("b", 64)
	if err := s.SetPluginApproval(ctx(), a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPluginApproval(ctx(), "chat-notifier")
	if err != nil || got.Enabled || got.ApprovedSHA256 != strings.Repeat("b", 64) || got.ApprovedBy == nil {
		t.Fatalf("update: %+v %v", got, err)
	}
	if list, _ := s.ListPluginApprovals(ctx()); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
}
