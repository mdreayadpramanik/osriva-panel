package tasks

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// Parsers for systemctl output; OS-independent so they are tested everywhere.

type systemdUnit struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

type systemdUnitFile struct {
	UnitFile string `json:"unit_file"`
	State    string `json:"state"`
}

func normalizeSystemdState(active string) string {
	switch active {
	case "active", "reloading", "refreshing":
		return "running"
	case "inactive":
		return "stopped"
	case "failed":
		return "failed"
	case "activating":
		return "starting"
	case "deactivating":
		return "stopping"
	}
	return "unknown"
}

func normalizeSystemdStartup(state string) string {
	switch state {
	case "enabled", "enabled-runtime", "linked", "linked-runtime":
		return "enabled"
	case "disabled", "masked", "masked-runtime":
		return "disabled"
	case "static", "indirect", "generated", "transient", "alias":
		return "static"
	}
	return "unknown"
}

// ParseSystemd merges `systemctl list-units --type=service --all --output=json`
// with `systemctl list-unit-files --type=service --output=json`.
func ParseSystemd(unitsJSON, filesJSON []byte) ([]agenttask.Service, error) {
	var units []systemdUnit
	if err := json.Unmarshal(unitsJSON, &units); err != nil {
		return nil, err
	}
	var files []systemdUnitFile
	if len(filesJSON) > 0 {
		if err := json.Unmarshal(filesJSON, &files); err != nil {
			return nil, err
		}
	}
	startup := map[string]string{}
	for _, f := range files {
		startup[f.UnitFile] = f.State
	}
	byName := map[string]agenttask.Service{}
	for _, u := range units {
		if u.Load == "not-found" || !strings.HasSuffix(u.Unit, ".service") {
			continue
		}
		detail := u.Active
		if u.Sub != "" {
			detail += " (" + u.Sub + ")"
		}
		st := normalizeSystemdState(u.Active)
		if u.Active == "active" && u.Sub == "exited" {
			st = "stopped" // oneshot that completed: nothing is running
		}
		byName[u.Unit] = agenttask.Service{Name: u.Unit, Description: u.Description, State: st, Detail: detail,
			Startup: normalizeSystemdStartup(startup[u.Unit])}
	}
	// Installed but never loaded units appear only in list-unit-files.
	for _, f := range files {
		if _, seen := byName[f.UnitFile]; seen || strings.Contains(f.UnitFile, "@.") || !strings.HasSuffix(f.UnitFile, ".service") {
			continue
		}
		byName[f.UnitFile] = agenttask.Service{Name: f.UnitFile, State: "stopped", Detail: "inactive (not loaded)",
			Startup: normalizeSystemdStartup(f.State)}
	}
	out := make([]agenttask.Service, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ParseSystemctlShow parses `systemctl show --property=A,B` key=value output.
func ParseSystemctlShow(out []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			m[k] = v
		}
	}
	return m
}
