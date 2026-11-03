package agenttask

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	UsersList      = "users.list"
	SSHKeysList    = "sshkeys.list"
	SSHKeysAdd     = "sshkeys.add"
	SSHKeysRemove  = "sshkeys.remove"
	CronList       = "cron.list"
	TimeInfo       = "system.time"
	HostnameSet    = "system.hostname"
	TimezoneSet    = "system.timezone"
	MaxSSHKeyBytes = 16 << 10
)

// Permission classes, mapped to RBAC permissions by the control plane.
const (
	ClassRead    = "read"    // servers.read
	ClassFiles   = "files"   // servers.files
	ClassOperate = "operate" // servers.operate
	ClassAccess  = "access"  // servers.access (grants or removes login access)
)

// Class returns the permission class of a task type.
func Class(typ string) string {
	switch {
	case FileTask(typ):
		return ClassFiles
	case typ == SSHKeysAdd || typ == SSHKeysRemove:
		return ClassAccess
	case typ == ServiceAction || typ == HostnameSet || typ == TimezoneSet:
		return ClassOperate
	case dockerTask(typ) && !dockerReadOnly(typ):
		return ClassOperate
	case appTask(typ):
		return ClassOperate
	case typ == ProxyApply || typ == ProxyDisable:
		return ClassOperate
	case cmsTask(typ) && !cmsReadOnly(typ):
		return ClassOperate
	case backupTask(typ) && !backupReadOnly(typ):
		return ClassOperate
	case vmTask(typ) && !vmReadOnly(typ):
		return ClassOperate
	}
	return ClassRead
}

// NeedsReauth reports whether a task requires a fresh password confirmation.
func NeedsReauth(typ string, params json.RawMessage) bool {
	if dockerTask(typ) {
		return dockerReauth(typ, params)
	}
	switch typ {
	case FilesWrite, FilesMkdir, FilesDelete, SSHKeysAdd, SSHKeysRemove, HostnameSet, TimezoneSet, AppDeploy, AppRemove, ProxyApply, ProxyDisable, CMSWPInstall, CMSWPUpdate, CMSRemove, BackupRestore, BackupDelete:
		return true
	case VMDelete:
		return true
	case VMAction:
		var p VMActionParams
		_ = json.Unmarshal(params, &p)
		return p.Action == VMStop
	case ServiceAction:
		var p ServiceActionParams
		_ = json.Unmarshal(params, &p)
		return Disruptive(p.Action)
	}
	return false
}

// UserName follows the portable POSIX user name rules used by useradd.
var UserName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

var hostnameLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ValidHostname checks an RFC 1123 host name (labels of 1–63 characters, at most 253 in total).
func ValidHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !hostnameLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// Timezone names: IANA (Europe/Berlin, America/Argentina/Buenos_Aires, Etc/GMT+5)
// or Windows (W. Europe Standard Time). Path traversal is impossible: no dots.
var Timezone = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\- /]{0,63}$`)

type UserParams struct {
	User string `json:"user"`
}

type SSHKeyAddParams struct {
	User string `json:"user"`
	Key  string `json:"key"`
}

type SSHKeyRemoveParams struct {
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
}

type HostnameParams struct {
	Hostname string `json:"hostname"`
}

type TimezoneParams struct {
	Timezone string `json:"timezone"`
}

// ParsePublicKey accepts one OpenSSH public key line ("type base64 [comment]")
// without options (command=, from=, …), and returns its SHA-256 fingerprint.
func ParsePublicKey(line string) (ssh.PublicKey, string, string, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return nil, "", "", fmt.Errorf("%w: key must be a single line", ErrInvalid)
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: not a valid OpenSSH public key", ErrInvalid)
	}
	if len(options) > 0 || len(rest) > 0 {
		return nil, "", "", fmt.Errorf("%w: key options (command=, from=, …) are not accepted", ErrInvalid)
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		if ck, ok := key.(ssh.CryptoPublicKey); ok {
			if rk, ok := ck.CryptoPublicKey().(interface{ Size() int }); ok && rk.Size()*8 < 2048 {
				return nil, "", "", fmt.Errorf("%w: RSA keys must be at least 2048 bits", ErrInvalid)
			}
		}
	default:
		return nil, "", "", fmt.Errorf("%w: key type %s is not accepted (use ed25519, ecdsa or rsa ≥ 2048)", ErrInvalid, key.Type())
	}
	return key, ssh.FingerprintSHA256(key), comment, nil
}

func validateSystem(typ string, raw json.RawMessage) error {
	switch typ {
	case UsersList, CronList, TimeInfo:
		var empty struct{}
		return decodeStrict(raw, &empty)
	case SSHKeysList:
		var p UserParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !UserName.MatchString(p.User) {
			return fmt.Errorf("%w: invalid user name", ErrInvalid)
		}
	case SSHKeysAdd:
		var p SSHKeyAddParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !UserName.MatchString(p.User) {
			return fmt.Errorf("%w: invalid user name", ErrInvalid)
		}
		if len(p.Key) > MaxSSHKeyBytes {
			return fmt.Errorf("%w: key is too long", ErrInvalid)
		}
		_, _, _, err := ParsePublicKey(p.Key)
		return err
	case SSHKeysRemove:
		var p SSHKeyRemoveParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !UserName.MatchString(p.User) {
			return fmt.Errorf("%w: invalid user name", ErrInvalid)
		}
		if !strings.HasPrefix(p.Fingerprint, "SHA256:") || len(p.Fingerprint) > 100 {
			return fmt.Errorf("%w: fingerprint must look like SHA256:…", ErrInvalid)
		}
	case HostnameSet:
		var p HostnameParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !ValidHostname(p.Hostname) {
			return fmt.Errorf("%w: invalid host name", ErrInvalid)
		}
	case TimezoneSet:
		var p TimezoneParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !Timezone.MatchString(p.Timezone) || strings.Contains(p.Timezone, "..") {
			return fmt.Errorf("%w: invalid timezone name", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown task type %q", ErrInvalid, typ)
	}
	return nil
}

// ---- results ----

type User struct {
	Name     string   `json:"name"`
	UID      string   `json:"uid,omitempty"`
	GID      string   `json:"gid,omitempty"`
	FullName string   `json:"full_name,omitempty"`
	Home     string   `json:"home,omitempty"`
	Shell    string   `json:"shell,omitempty"`
	Groups   []string `json:"groups"`
	System   bool     `json:"system"`
	Enabled  bool     `json:"enabled"`
}

type Group struct {
	Name    string   `json:"name"`
	GID     string   `json:"gid,omitempty"`
	Members []string `json:"members"`
}

type UsersResult struct {
	Users  []User  `json:"users"`
	Groups []Group `json:"groups"`
}

type SSHKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment,omitempty"`
	Options     bool   `json:"has_options,omitempty"`
}

type SSHKeysResult struct {
	User string   `json:"user"`
	Path string   `json:"path"`
	Keys []SSHKey `json:"keys"`
}

type CronEntry struct {
	Source   string `json:"source"`         // file path or task path
	User     string `json:"user,omitempty"` // who it runs as
	Schedule string `json:"schedule"`       // cron expression or scheduler description
	Command  string `json:"command"`
	Enabled  bool   `json:"enabled"`
	NextRun  string `json:"next_run,omitempty"` // Windows only
}

type CronResult struct {
	Backend string      `json:"backend"` // cron | windows-task-scheduler
	Entries []CronEntry `json:"entries"`
}

type TimeResult struct {
	Hostname   string `json:"hostname"`
	Timezone   string `json:"timezone"`
	LocalTime  string `json:"local_time"`
	NTPSynced  *bool  `json:"ntp_synced,omitempty"`
	CanSetName bool   `json:"can_set_hostname"`
	CanSetZone bool   `json:"can_set_timezone"`
}
