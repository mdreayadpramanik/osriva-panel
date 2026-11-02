package storetest

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testAgentCA(t *testing.T, s store.Store) {
	if _, _, err := s.GetAgentCA(ctx()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty CA: %v", err)
	}
	if err := s.InsertAgentCA(ctx(), "PEM", []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAgentCA(ctx(), "OTHER", []byte{3}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second CA: %v", err)
	}
	cert, key, err := s.GetAgentCA(ctx())
	if err != nil || cert != "PEM" || string(key) != "\x01\x02" {
		t.Fatalf("get CA: %q %v %v", cert, key, err)
	}
}

func newServer(t *testing.T, s store.Store, name string, now time.Time) (*store.Server, []byte) {
	t.Helper()
	_, hash, _ := auth.NewSessionToken()
	exp := now.Add(time.Hour)
	x := &store.Server{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Name: name,
		EnrollmentTokenHash: hash, EnrollmentExpiresAt: &exp}
	if err := s.CreateServer(ctx(), x); err != nil {
		t.Fatalf("CreateServer %s: %v", name, err)
	}
	return x, hash
}

func cert(serverID, serial string, now time.Time) *store.AgentCertificate {
	return &store.AgentCertificate{Serial: serial, ServerID: serverID, Fingerprint: "sha256:" + serial, NotAfter: now.Add(30 * 24 * time.Hour)}
}

func testServerEnrollment(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, hash := newServer(t, s, "web-1", now)
	if a.Status != store.ServerPending || a.CreatedAt.IsZero() {
		t.Fatalf("created server: %+v", a)
	}
	if err := s.CreateServer(ctx(), &store.Server{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Name: "web-1"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}

	if _, err := s.FindPendingEnrollment(ctx(), hash, now.Add(2*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired token found: %v", err)
	}
	found, err := s.FindPendingEnrollment(ctx(), hash, now)
	if err != nil || found.ID != a.ID {
		t.Fatalf("find: %v", err)
	}
	if err := s.CompleteEnrollment(ctx(), a.ID, hash, now, cert(a.ID, "a1", now)); err != nil {
		t.Fatal(err)
	}
	// The token is single use.
	if err := s.CompleteEnrollment(ctx(), a.ID, hash, now, cert(a.ID, "a2", now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token reused: %v", err)
	}
	got, _ := s.GetServer(ctx(), a.ID)
	if got.Status != store.ServerActive || got.EnrollmentTokenHash != nil || got.EnrollmentExpiresAt != nil || got.EnrolledAt == nil {
		t.Fatalf("after enrolment: %+v", got)
	}
	c, err := s.GetAgentCertificate(ctx(), "a1")
	if err != nil || c.ServerID != a.ID || c.RevokedAt != nil {
		t.Fatalf("cert: %+v %v", c, err)
	}
	if err := s.SetEnrollmentToken(ctx(), a.ID, make([]byte, 32), now.Add(time.Hour)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("new token for active server: %v", err)
	}
	if err := s.SetEnrollmentToken(ctx(), auth.NewID(), make([]byte, 32), now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token for missing server: %v", err)
	}

	if err := s.AddAgentCertificate(ctx(), cert(a.ID, "a3", now)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeServer(ctx(), a.ID, now); err != nil {
		t.Fatal(err)
	}
	for _, serial := range []string{"a1", "a3"} {
		if c, _ := s.GetAgentCertificate(ctx(), serial); c == nil || c.RevokedAt == nil {
			t.Errorf("cert %s not revoked", serial)
		}
	}
	if got, _ := s.GetServer(ctx(), a.ID); got.Status != store.ServerRevoked {
		t.Fatalf("status after revoke: %s", got.Status)
	}
}

func testServerTokenRotation(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, old := newServer(t, s, "db-1", now)
	_, fresh, _ := auth.NewSessionToken()
	if err := s.SetEnrollmentToken(ctx(), a.ID, fresh, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindPendingEnrollment(ctx(), old, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("old token still valid after rotation")
	}
	if _, err := s.FindPendingEnrollment(ctx(), fresh, now); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
}

func testServerReports(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, _ := newServer(t, s, "app-1", now)
	newServer(t, s, "app-0", now)
	if _, err := s.GetServerInventory(ctx(), a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("inventory before report: %v", err)
	}
	facts := store.ServerFacts{Hostname: "app-1.lan", OSFamily: "linux", OSName: "Debian GNU/Linux 12", OSVersion: "12", Kernel: "6.1.0", Arch: "amd64", AgentVersion: "0.2.0"}
	data := json.RawMessage(`{"hostname":"app-1.lan"}`)
	inv := &store.ServerInventory{CollectedAt: now, Data: data, Capabilities: json.RawMessage(`[{"key":"docker"}]`)}
	if err := s.RecordReport(ctx(), a.ID, facts, "203.0.113.9", now, inv); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetServer(ctx(), a.ID)
	if got.Facts != facts || got.LastSeenIP != "203.0.113.9" || got.LastSeenAt == nil || !got.LastSeenAt.Equal(now) {
		t.Fatalf("facts: %+v", got)
	}
	stored, err := s.GetServerInventory(ctx(), a.ID)
	if err != nil || !stored.CollectedAt.Equal(now) {
		t.Fatalf("inventory: %+v %v", stored, err)
	}
	var m map[string]any
	var caps []map[string]any
	if json.Unmarshal(stored.Data, &m) != nil || m["hostname"] != "app-1.lan" || json.Unmarshal(stored.Capabilities, &caps) != nil || len(caps) != 1 {
		t.Fatalf("inventory payload: %s %s", stored.Data, stored.Capabilities)
	}
	// A second report replaces the snapshot.
	if err := s.RecordReport(ctx(), a.ID, facts, "", now.Add(time.Minute), &store.ServerInventory{CollectedAt: now.Add(time.Minute), Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if stored, _ = s.GetServerInventory(ctx(), a.ID); string(stored.Capabilities) != "[]" {
		t.Fatalf("capabilities default: %s", stored.Capabilities)
	}
	if err := s.RecordReport(ctx(), auth.NewID(), facts, "", now, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("report for missing server: %v", err)
	}
	list, _ := s.ListServers(ctx(), store.DefaultOrganizationID)
	if len(list) != 2 || list[0].Name != "app-0" {
		t.Fatalf("list sorted by name: %+v", list)
	}
	if other, _ := s.ListServers(ctx(), auth.NewID()); len(other) != 0 {
		t.Fatal("servers leaked across organizations")
	}
}
