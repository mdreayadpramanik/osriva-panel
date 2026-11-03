package agenttask

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Encrypted backups (docs/adr/0010-backups.md). Archives are gzip-compressed
// tar files encrypted with age (X25519). The agent only ever holds the
// recipient (public key); the identity needed to decrypt is sent by the
// control plane for verify and restore only.

const (
	BackupCreate  = "backup.create"
	BackupList    = "backup.list"
	BackupVerify  = "backup.verify"
	BackupRestore = "backup.restore"
	BackupDelete  = "backup.delete"

	BackupVolume    = "volume"
	BackupWordPress = "wordpress"
)

func backupTask(typ string) bool { return strings.HasPrefix(typ, "backup.") }

func backupReadOnly(typ string) bool { return typ == BackupList || typ == BackupVerify }

var (
	backupID      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	ageRecipient  = regexp.MustCompile(`^age1[0-9a-z]{58}$`)
	ageIdentity   = regexp.MustCompile(`^AGE-SECRET-KEY-1[0-9A-Z]{58}$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	backupTargetV = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
)

type BackupCreateParams struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`   // volume | wordpress
	Target    string `json:"target"` // volume name or WordPress instance name
	Recipient string `json:"recipient"`
}

type BackupRefParams struct {
	ID string `json:"id"`
}

type BackupVerifyParams struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"` // of the encrypted file, recorded at creation
	BackupKey string `json:"backup_key"`
}

type BackupRestoreParams struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	SHA256    string `json:"sha256"`
	BackupKey string `json:"backup_key"` // age identity; redacted from stored task parameters
}

func validTarget(kind, target string) error {
	switch kind {
	case BackupVolume:
		if !backupTargetV.MatchString(target) {
			return fmt.Errorf("%w: invalid volume name", ErrInvalid)
		}
	case BackupWordPress:
		if !AppName.MatchString(target) {
			return fmt.Errorf("%w: invalid WordPress instance name", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: kind must be volume or wordpress", ErrInvalid)
	}
	return nil
}

func validateBackup(typ string, raw json.RawMessage) error {
	switch typ {
	case BackupList:
		var empty struct{}
		return decodeStrict(raw, &empty)
	case BackupCreate:
		var p BackupCreateParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !backupID.MatchString(p.ID) || !ageRecipient.MatchString(p.Recipient) {
			return fmt.Errorf("%w: invalid backup id or recipient", ErrInvalid)
		}
		return validTarget(p.Kind, p.Target)
	case BackupDelete:
		var p BackupRefParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !backupID.MatchString(p.ID) {
			return fmt.Errorf("%w: invalid backup id", ErrInvalid)
		}
	case BackupVerify:
		var p BackupVerifyParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !backupID.MatchString(p.ID) || !sha256Hex.MatchString(p.SHA256) || !ageIdentity.MatchString(p.BackupKey) {
			return fmt.Errorf("%w: invalid backup id, checksum or key", ErrInvalid)
		}
	case BackupRestore:
		var p BackupRestoreParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !backupID.MatchString(p.ID) || !sha256Hex.MatchString(p.SHA256) || !ageIdentity.MatchString(p.BackupKey) {
			return fmt.Errorf("%w: invalid backup id, checksum or key", ErrInvalid)
		}
		return validTarget(p.Kind, p.Target)
	default:
		return fmt.Errorf("%w: unknown task type %q", ErrInvalid, typ)
	}
	return nil
}

type BackupCreateResult struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	Size        int64  `json:"size"`         // encrypted bytes on disk
	SHA256      string `json:"sha256"`       // of the encrypted file
	PlainSHA256 string `json:"plain_sha256"` // of the tar stream before compression and encryption
	Files       int    `json:"files"`
	DurationMs  int64  `json:"duration_ms"`
}

type BackupFile struct {
	ID       string `json:"id"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type BackupVerifyResult struct {
	ID          string `json:"id"`
	OK          bool   `json:"ok"`
	Files       int    `json:"files"`
	PlainSHA256 string `json:"plain_sha256"`
	Problem     string `json:"problem,omitempty"`
}

type BackupRestoreResult struct {
	ID       string   `json:"id"`
	Files    int      `json:"files"`
	Stopped  []string `json:"stopped"` // containers stopped for the restore and started again
	Database bool     `json:"database"`
}
