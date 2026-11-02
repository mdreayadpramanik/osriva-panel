package rbac

import "testing"

func TestBuiltinRolesOnlyUseCatalogPermissions(t *testing.T) {
	for _, r := range BuiltinRoles() {
		for _, p := range r.Permissions {
			if _, ok := Catalog[p]; !ok {
				t.Errorf("role %s references unknown permission %s", r.Key, p)
			}
		}
	}
}

func TestOwnerHasEverything(t *testing.T) {
	for _, r := range BuiltinRoles() {
		if r.Key == RoleOwner && len(r.Permissions) != len(Catalog) {
			t.Fatalf("owner has %d of %d permissions", len(r.Permissions), len(Catalog))
		}
	}
}

func TestRoleHierarchyIsStrict(t *testing.T) {
	roles := map[string]Set{}
	for _, r := range BuiltinRoles() {
		roles[r.Key] = NewSet(r.Permissions...)
	}
	if !roles[RoleOwner].Covers(roles[RoleAdmin]) || roles[RoleAdmin].Covers(roles[RoleOwner]) {
		t.Error("owner must strictly cover admin")
	}
	if !roles[RoleAdmin].Covers(roles[RoleAuditor]) || roles[RoleAuditor].Covers(roles[RoleAdmin]) {
		t.Error("admin must strictly cover auditor")
	}
	if roles[RoleAuditor].Has(UsersWrite) || roles[RoleAuditor].Has(SettingsWrite) {
		t.Error("auditor must be read-only")
	}
	if len(roles[RoleMember]) != 0 {
		t.Error("member must have no permissions")
	}
}

func TestSet(t *testing.T) {
	s := NewSet("a.b", "c.d")
	if !s.Has("a.b") || s.Has("x.y") {
		t.Fatal("Has broken")
	}
	if !s.Covers(NewSet("a.b")) || s.Covers(NewSet("a.b", "x.y")) || !s.Covers(NewSet()) {
		t.Fatal("Covers broken")
	}
	if got := s.Sorted(); len(got) != 2 || got[0] != "a.b" {
		t.Fatalf("Sorted = %v", got)
	}
}
