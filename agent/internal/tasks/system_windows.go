//go:build windows

package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// powershell runs a FIXED script (never built from task parameters) and returns its stdout.
func powershell(ctx context.Context, script string) ([]byte, error) {
	return run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
}

// jsonList decodes ConvertTo-Json output, which is an object for one item and an array for many.
func jsonList[T any](out []byte) ([]T, error) {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "{") {
		s = "[" + s + "]"
	}
	var v []T
	return v, json.Unmarshal([]byte(s), &v)
}

const usersScript = `$ErrorActionPreference='Stop'
$u = Get-LocalUser | ForEach-Object { [pscustomobject]@{ Name=$_.Name; SID=$_.SID.Value; FullName=$_.FullName; Enabled=$_.Enabled } }
$g = Get-LocalGroup | ForEach-Object { $n=$_.Name; [pscustomobject]@{ Name=$n; SID=$_.SID.Value; Members=@(try { Get-LocalGroupMember -Group $n -ErrorAction Stop | ForEach-Object { ($_.Name -split '\\')[-1] } } catch { @() }) } }
[pscustomobject]@{ users=@($u); groups=@($g) } | ConvertTo-Json -Depth 4 -Compress`

func listUsers(ctx context.Context) (any, error) {
	out, err := powershell(ctx, usersScript)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Users []struct {
			Name, SID, FullName string
			Enabled             bool
		} `json:"users"`
		Groups []struct {
			Name, SID string
			Members   json.RawMessage
		} `json:"groups"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse local users: %w", err)
	}
	res := agenttask.UsersResult{Users: []agenttask.User{}, Groups: []agenttask.Group{}}
	member := map[string][]string{}
	for _, g := range raw.Groups {
		members, _ := jsonList[string](g.Members)
		if members == nil {
			members = []string{}
		}
		for _, m := range members {
			member[strings.ToLower(m)] = append(member[strings.ToLower(m)], g.Name)
		}
		res.Groups = append(res.Groups, agenttask.Group{Name: g.Name, GID: g.SID, Members: members})
	}
	for _, u := range raw.Users {
		groups := member[strings.ToLower(u.Name)]
		if groups == nil {
			groups = []string{}
		}
		// Built-in accounts have well-known RIDs below 1000 (500 Administrator, 501 Guest, 503, 504 …).
		rid := u.SID[strings.LastIndexByte(u.SID, '-')+1:]
		res.Users = append(res.Users, agenttask.User{Name: u.Name, UID: u.SID, FullName: u.FullName, Groups: groups,
			System: len(rid) <= 3, Enabled: u.Enabled})
	}
	sort.Slice(res.Users, func(i, j int) bool { return res.Users[i].Name < res.Users[j].Name })
	return res, nil
}

func listSSHKeys(context.Context, string) (any, error) {
	return nil, fmt.Errorf("%w: SSH key management is implemented for Linux only", ErrUnsupported)
}
func addSSHKey(context.Context, string, string) (any, error) { return listSSHKeys(nil, "") }
func removeSSHKey(context.Context, string, string) (any, error) {
	return listSSHKeys(nil, "")
}

// Built-in \Microsoft\ tasks (hundreds) are left out; they are part of Windows itself.
const tasksScript = `$ErrorActionPreference='Stop'
Get-ScheduledTask | Where-Object { $_.TaskPath -notlike '\Microsoft\*' } | ForEach-Object {
  $i = $_ | Get-ScheduledTaskInfo -ErrorAction SilentlyContinue
  [pscustomobject]@{
    Path = $_.TaskPath + $_.TaskName
    State = [string]$_.State
    User = [string]$_.Principal.UserId
    Actions = (@($_.Actions | ForEach-Object { ($_.Execute + ' ' + $_.Arguments).Trim() }) -join '; ')
    Triggers = (@($_.Triggers | ForEach-Object { $_.CimClass.CimClassName -replace '^MSFT_Task|Trigger$','' } | Where-Object { $_ }) -join ', ')
    NextRun = if ($i -and $i.NextRunTime) { $i.NextRunTime.ToUniversalTime().ToString('o') } else { '' }
  } } | ConvertTo-Json -Compress`

func listCron(ctx context.Context) (any, error) {
	out, err := powershell(ctx, tasksScript)
	if err != nil {
		return nil, err
	}
	items, err := jsonList[struct{ Path, State, User, Actions, Triggers, NextRun string }](out)
	if err != nil {
		return nil, fmt.Errorf("parse scheduled tasks: %w", err)
	}
	res := agenttask.CronResult{Backend: "windows-task-scheduler", Entries: []agenttask.CronEntry{}}
	for _, t := range items {
		next := ""
		if ts, err := time.Parse(time.RFC3339Nano, t.NextRun); err == nil {
			next = ts.UTC().Format(time.RFC3339)
		}
		res.Entries = append(res.Entries, agenttask.CronEntry{Source: t.Path, User: t.User, Schedule: t.Triggers,
			Command: clip(t.Actions), Enabled: t.State != "Disabled", NextRun: next})
	}
	return res, nil
}

func timeInfo(ctx context.Context) (any, error) {
	res := agenttask.TimeResult{LocalTime: time.Now().Format(time.RFC3339), CanSetName: false, CanSetZone: true}
	res.Hostname, _ = os.Hostname()
	if out, err := run(ctx, "tzutil", "/g"); err == nil {
		res.Timezone = strings.TrimSpace(string(out))
	}
	return res, nil
}

func setHostname(context.Context, string) (any, error) {
	return nil, fmt.Errorf("%w: renaming a Windows computer needs a reboot and is not offered", ErrUnsupported)
}

func setTimezone(ctx context.Context, tz string) (any, error) {
	out, err := run(ctx, "tzutil", "/l")
	if err != nil {
		return nil, err
	}
	known := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == tz {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("unknown Windows timezone %q (see tzutil /l)", tz)
	}
	if _, err := run(ctx, "tzutil", "/s", tz); err != nil {
		return nil, err
	}
	return timeInfo(ctx)
}
