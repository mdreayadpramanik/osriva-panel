// Package rbac defines the permission catalog and built-in roles. The catalog
// lives in code and is synced to the database at startup, so a permission
// cannot be granted unless the code that checks it exists.
package rbac

import "sort"

// Permission keys. Add a key here only when an endpoint checks it.
const (
	UsersRead         = "users.read"
	UsersWrite        = "users.write"
	RolesRead         = "roles.read"
	AuditRead         = "audit.read"
	SettingsRead      = "settings.read"
	SettingsWrite     = "settings.write"
	SessionsManageAll = "sessions.manage_all"
	ServersRead       = "servers.read"
	ServersWrite      = "servers.write"
	ServersOperate    = "servers.operate"
	ServersFiles      = "servers.files"
)

// Catalog maps every permission to its description.
var Catalog = map[string]string{
	UsersRead:         "View users and their roles",
	UsersWrite:        "Create users, change their status and roles",
	RolesRead:         "View roles and their permissions",
	AuditRead:         "View the audit log",
	SettingsRead:      "View panel settings",
	SettingsWrite:     "Change panel settings",
	SessionsManageAll: "View and revoke other users' sessions",
	ServersRead:       "View managed servers and their inventory",
	ServersWrite:      "Add servers, issue enrolment tokens and revoke agents",
	ServersOperate:    "Control services on managed servers (start, stop, restart, enable, disable)",
	ServersFiles:      "Browse, read and edit files on managed servers (within each agent's allowed directories)",
}

// BuiltinRole is a role shipped with OSRIVA and recreated at every startup.
type BuiltinRole struct {
	Key, Name, Description string
	Permissions            []string
}

const (
	RoleOwner   = "owner"
	RoleAdmin   = "admin"
	RoleAuditor = "auditor"
	RoleMember  = "member"
)

func BuiltinRoles() []BuiltinRole {
	all := make([]string, 0, len(Catalog))
	for k := range Catalog {
		all = append(all, k)
	}
	sort.Strings(all)
	return []BuiltinRole{
		{RoleOwner, "Owner", "Full control of the panel, including settings", all},
		{RoleAdmin, "Administrator", "Manage users and sessions; read settings and audit log",
			[]string{UsersRead, UsersWrite, RolesRead, AuditRead, SettingsRead, SessionsManageAll, ServersRead, ServersWrite, ServersOperate, ServersFiles}},
		{RoleAuditor, "Auditor", "Read-only access to users, roles, settings and the audit log",
			[]string{UsersRead, RolesRead, AuditRead, SettingsRead, ServersRead}},
		{RoleMember, "Member", "Can sign in and manage their own account only", nil},
	}
}

// Set is a set of permission keys.
type Set map[string]struct{}

func NewSet(perms ...string) Set {
	s := make(Set, len(perms))
	for _, p := range perms {
		s[p] = struct{}{}
	}
	return s
}

func (s Set) Has(p string) bool { _, ok := s[p]; return ok }

// Covers reports whether s contains every permission in other.
func (s Set) Covers(other Set) bool {
	for p := range other {
		if !s.Has(p) {
			return false
		}
	}
	return true
}

func (s Set) Sorted() []string {
	out := make([]string, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
