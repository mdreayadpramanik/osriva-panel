package agenttask

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	LogsRead    = "logs.read"
	FilesList   = "files.list"
	FilesRead   = "files.read"
	FilesWrite  = "files.write"
	FilesMkdir  = "files.mkdir"
	FilesDelete = "files.delete"
)

// ---- logs ----

const (
	LogSourceSystem      = "system"      // journald (whole journal) / Windows "System" log
	LogSourceApplication = "application" // Windows "Application" log
	LogSourceService     = "service"     // one service (journald unit)
	MaxLogLines          = 1000
)

type LogsParams struct {
	Source  string `json:"source"`
	Service string `json:"service,omitempty"`
	Lines   int    `json:"lines,omitempty"`
	// MinLevel filters by severity: error, warning, info (default) or debug.
	MinLevel string `json:"min_level,omitempty"`
}

var levels = map[string]bool{"": true, "error": true, "warning": true, "info": true, "debug": true}

func validateLogs(raw json.RawMessage) error {
	var p LogsParams
	if err := decodeStrict(raw, &p); err != nil {
		return err
	}
	switch p.Source {
	case LogSourceSystem, LogSourceApplication:
		if p.Service != "" {
			return fmt.Errorf("%w: service is only allowed with source=service", ErrInvalid)
		}
	case LogSourceService:
		if !ServiceName.MatchString(p.Service) {
			return fmt.Errorf("%w: service name %q is not allowed", ErrInvalid, p.Service)
		}
	default:
		return fmt.Errorf("%w: source must be system, application or service", ErrInvalid)
	}
	if p.Lines < 0 || p.Lines > MaxLogLines {
		return fmt.Errorf("%w: lines must be between 1 and %d", ErrInvalid, MaxLogLines)
	}
	if !levels[p.MinLevel] {
		return fmt.Errorf("%w: min_level must be error, warning, info or debug", ErrInvalid)
	}
	return nil
}

type LogEntry struct {
	TS      string `json:"ts"` // RFC 3339
	Level   string `json:"level"`
	Source  string `json:"source,omitempty"` // unit / provider
	Message string `json:"message"`
}

type LogsResult struct {
	Backend string     `json:"backend"` // journald | windows-eventlog
	Entries []LogEntry `json:"entries"` // oldest first
}

// ---- files ----

// MaxFileBytes bounds file reads and writes through the task channel.
const MaxFileBytes = 512 << 10

type PathParams struct {
	Path string `json:"path"`
}

type FilesReadParams struct {
	Path string `json:"path"`
}

type FilesWriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Create allows creating a new file; otherwise the file must exist.
	Create bool `json:"create,omitempty"`
}

type FilesDeleteParams struct {
	Path string `json:"path"`
}

// validPath rejects obviously malformed paths; real confinement happens on the agent.
func validPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: path is required", ErrInvalid)
	case len(p) > 4096:
		return fmt.Errorf("%w: path is too long", ErrInvalid)
	case !utf8.ValidString(p) || strings.ContainsRune(p, 0):
		return fmt.Errorf("%w: path contains invalid characters", ErrInvalid)
	case !(strings.HasPrefix(p, "/") || (len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))):
		return fmt.Errorf("%w: path must be absolute", ErrInvalid)
	}
	return nil
}

func validateFiles(typ string, raw json.RawMessage) error {
	switch typ {
	case FilesList, FilesMkdir:
		var p PathParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		return validPath(p.Path)
	case FilesRead:
		var p FilesReadParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		return validPath(p.Path)
	case FilesWrite:
		var p FilesWriteParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if len(p.Content) > MaxFileBytes {
			return fmt.Errorf("%w: content is larger than %d bytes", ErrInvalid, MaxFileBytes)
		}
		if !utf8.ValidString(p.Content) {
			return fmt.Errorf("%w: only UTF-8 text files can be written", ErrInvalid)
		}
		return validPath(p.Path)
	case FilesDelete:
		var p FilesDeleteParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		return validPath(p.Path)
	}
	return fmt.Errorf("%w: unknown task type %q", ErrInvalid, typ)
}

type FileEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // file | dir | symlink | other
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
	// Denied entries exist but are blocked by the agent's policy.
	Denied bool `json:"denied,omitempty"`
}

type FilesListResult struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent,omitempty"` // empty at a root
	Entries []FileEntry `json:"entries"`
	// Roots are the directories this agent allows, so the UI can offer them.
	Roots []string `json:"roots"`
}

type FilesReadResult struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Content   string `json:"content"` // UTF-8 text; empty when Binary
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	ModTime   string `json:"mod_time"`
}

type FilesWriteResult struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Created bool   `json:"created"`
}
