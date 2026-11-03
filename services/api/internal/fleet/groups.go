package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	// MaxBatchServers bounds one fleet batch.
	MaxBatchServers = 200
	maxGroupMembers = 1000
)

// ---- server groups ----

type GroupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func checkGroupInput(in *GroupInput) error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || len(in.Name) > 100 || strings.ContainsAny(in.Name, "\r\n\t\x00") {
		return &identity.ValidationError{Field: "name", Message: "must be 1-100 characters on one line"}
	}
	if len(in.Description) > 500 || strings.ContainsRune(in.Description, 0) {
		return &identity.ValidationError{Field: "description", Message: "must be at most 500 characters"}
	}
	return nil
}

func groupConflict(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return &identity.ValidationError{Field: "name", Message: "is already used by another group"}
	}
	return err
}

func (s *Service) ListGroups(ctx context.Context, actor *identity.Principal) ([]store.ServerGroup, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	return s.store.ListServerGroups(ctx, actor.User.OrganizationID)
}

func (s *Service) groupInOrg(ctx context.Context, actor *identity.Principal, perm, id string) (*store.ServerGroup, error) {
	if err := s.id.Require(ctx, actor, perm); err != nil {
		return nil, err
	}
	g, err := s.store.GetServerGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if g.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return g, nil
}

func (s *Service) GetGroup(ctx context.Context, actor *identity.Principal, id string) (*store.ServerGroup, error) {
	return s.groupInOrg(ctx, actor, rbac.ServersRead, id)
}

func (s *Service) CreateGroup(ctx context.Context, actor *identity.Principal, in GroupInput) (*store.ServerGroup, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersWrite); err != nil {
		return nil, err
	}
	if err := checkGroupInput(&in); err != nil {
		return nil, err
	}
	g := &store.ServerGroup{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: in.Name, Description: in.Description,
		CreatedBy: &actor.User.ID}
	if err := s.store.CreateServerGroup(ctx, g); err != nil {
		return nil, groupConflict(err)
	}
	s.id.Audit(ctx, actor.User, "servers.group_create", "server_group", g.ID, store.OutcomeSuccess, map[string]any{"name": g.Name})
	return g, nil
}

func (s *Service) UpdateGroup(ctx context.Context, actor *identity.Principal, id string, in GroupInput) (*store.ServerGroup, error) {
	g, err := s.groupInOrg(ctx, actor, rbac.ServersWrite, id)
	if err != nil {
		return nil, err
	}
	if err := checkGroupInput(&in); err != nil {
		return nil, err
	}
	old := g.Name
	g.Name, g.Description = in.Name, in.Description
	if err := s.store.UpdateServerGroup(ctx, g); err != nil {
		return nil, groupConflict(err)
	}
	s.id.Audit(ctx, actor.User, "servers.group_update", "server_group", g.ID, store.OutcomeSuccess, map[string]any{"name": g.Name, "old_name": old})
	return g, nil
}

func (s *Service) DeleteGroup(ctx context.Context, actor *identity.Principal, id string) error {
	g, err := s.groupInOrg(ctx, actor, rbac.ServersWrite, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteServerGroup(ctx, g.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return &identity.PolicyError{Message: "an alert rule targets this group: change or delete the rule first"}
		}
		return err
	}
	s.id.Audit(ctx, actor.User, "servers.group_delete", "server_group", g.ID, store.OutcomeSuccess,
		map[string]any{"name": g.Name, "members": len(g.ServerIDs)})
	return nil
}

// SetGroupMembers replaces a group's members with servers of the actor's organization.
func (s *Service) SetGroupMembers(ctx context.Context, actor *identity.Principal, id string, serverIDs []string) (*store.ServerGroup, error) {
	g, err := s.groupInOrg(ctx, actor, rbac.ServersWrite, id)
	if err != nil {
		return nil, err
	}
	ids := slices.Clone(serverIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > maxGroupMembers {
		return nil, &identity.ValidationError{Field: "server_ids", Message: fmt.Sprintf("at most %d servers per group", maxGroupMembers)}
	}
	for _, sid := range ids {
		if _, err := s.getInOrg(ctx, actor, sid); err != nil {
			return nil, &identity.ValidationError{Field: "server_ids", Message: "unknown server " + truncate(sid, 40)}
		}
	}
	if err := s.store.SetServerGroupMembers(ctx, g.ID, ids); err != nil {
		return nil, err
	}
	added, removed := 0, 0
	for _, sid := range ids {
		if !slices.Contains(g.ServerIDs, sid) {
			added++
		}
	}
	for _, sid := range g.ServerIDs {
		if !slices.Contains(ids, sid) {
			removed++
		}
	}
	s.id.Audit(ctx, actor.User, "servers.group_members", "server_group", g.ID, store.OutcomeSuccess,
		map[string]any{"name": g.Name, "members": len(ids), "added": added, "removed": removed})
	return s.store.GetServerGroup(ctx, g.ID)
}

// ---- fleet batches ----

type BatchInput struct {
	Type      string          `json:"type"`
	Params    json.RawMessage `json:"params"`
	GroupID   *string         `json:"group_id"`
	ServerIDs []string        `json:"server_ids"`
}

// SkippedServer is a target that was not queued, with the reason.
type SkippedServer struct {
	ServerID string `json:"server_id"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
}

// BatchTask is one server's task of a batch.
type BatchTask struct {
	Task       store.Task
	ServerName string
}

type BatchView struct {
	Batch   store.TaskBatch
	Tasks   []BatchTask
	Skipped []SkippedServer
	// Counts per task status (queued, running, succeeded, failed, expired).
	Counts map[string]int
}

// RunBatch queues one task on every online server of a group or list. Each
// task passes the same validation, permission and audit as a single-server task.
func (s *Service) RunBatch(ctx context.Context, actor *identity.Principal, in BatchInput) (*BatchView, error) {
	if !agenttask.Bulk(in.Type) {
		return nil, &identity.ValidationError{Field: "type", Message: "this task type cannot run on several servers at once"}
	}
	if err := agenttask.Validate(in.Type, in.Params); err != nil {
		return nil, &identity.ValidationError{Field: "task", Message: strings.TrimPrefix(err.Error(), agenttask.ErrInvalid.Error()+": ")}
	}
	if err := s.id.Require(ctx, actor, classPermission[agenttask.Class(in.Type)]); err != nil {
		return nil, err
	}
	if agenttask.NeedsReauth(in.Type, in.Params) {
		if err := s.id.RequireRecentReauth(actor); err != nil {
			return nil, err
		}
	}
	if (in.GroupID == nil) == (len(in.ServerIDs) == 0) {
		return nil, &identity.ValidationError{Field: "group_id", Message: "choose either a group or a list of servers"}
	}
	var ids []string
	var target string
	if in.GroupID != nil {
		g, err := s.groupInOrg(ctx, actor, rbac.ServersRead, *in.GroupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, &identity.ValidationError{Field: "group_id", Message: "unknown server group"}
			}
			return nil, err
		}
		ids, target = g.ServerIDs, "group "+g.Name
	} else {
		ids = slices.Clone(in.ServerIDs)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		target = fmt.Sprintf("%d selected server(s)", len(ids))
	}
	if len(ids) == 0 {
		return nil, &identity.PolicyError{Message: "the group has no servers"}
	}
	if len(ids) > MaxBatchServers {
		return nil, &identity.ValidationError{Field: "server_ids", Message: fmt.Sprintf("at most %d servers per batch", MaxBatchServers)}
	}
	params := in.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	now := s.id.Now()
	batch := &store.TaskBatch{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Type: in.Type, GroupID: in.GroupID,
		Target: target, RequestedBy: &actor.User.ID}
	batch.Params, _ = json.Marshal(RedactedParams(params))
	skipped := []SkippedServer{}
	var tasks []*store.Task
	names := map[string]string{}
	for _, sid := range ids {
		x, err := s.getInOrg(ctx, actor, sid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, &identity.ValidationError{Field: "server_ids", Message: "unknown server " + truncate(sid, 40)}
			}
			return nil, err
		}
		names[x.ID] = x.Name
		switch v := s.view(*x); v.Connection {
		case ConnOnline:
			tasks = append(tasks, &store.Task{ID: auth.NewID(), ServerID: x.ID, Type: in.Type, Params: params, RequestedBy: &actor.User.ID,
				CreatedAt: now, ExpiresAt: now.Add(taskQueueTTL), RunTimeout: agenttask.RunTimeout(in.Type)})
		case ConnOffline:
			skipped = append(skipped, SkippedServer{x.ID, x.Name, "agent offline"})
		case ConnPending:
			skipped = append(skipped, SkippedServer{x.ID, x.Name, "agent not enrolled"})
		default:
			skipped = append(skipped, SkippedServer{x.ID, x.Name, "agent revoked"})
		}
	}
	if len(tasks) == 0 {
		return nil, &identity.PolicyError{Message: "none of the target servers has an online agent"}
	}
	batch.Skipped, _ = json.Marshal(skipped)
	if err := s.store.CreateTaskBatch(ctx, batch, tasks); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "fleet.batch", "task_batch", batch.ID, store.OutcomeSuccess, map[string]any{
		"type": in.Type, "params": RedactedParams(params), "target": target, "queued": len(tasks), "skipped": len(skipped)})
	view := &BatchView{Batch: *batch, Skipped: skipped, Counts: map[string]int{}}
	for _, t := range tasks {
		s.id.Audit(ctx, actor.User, "servers.task", "server", t.ServerID, store.OutcomeSuccess,
			map[string]any{"task_id": t.ID, "type": t.Type, "params": RedactedParams(params), "server": names[t.ServerID], "batch_id": batch.ID})
		s.notify.wake(t.ServerID)
		view.Tasks = append(view.Tasks, BatchTask{Task: *t, ServerName: names[t.ServerID]})
		view.Counts[string(t.Status)]++
	}
	return view, nil
}

func (s *Service) batchInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.TaskBatch, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	b, err := s.store.GetTaskBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return b, nil
}

func (s *Service) batchView(ctx context.Context, b *store.TaskBatch) (*BatchView, error) {
	tasks, err := s.store.ListBatchTasks(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	v := &BatchView{Batch: *b, Skipped: []SkippedServer{}, Counts: map[string]int{}}
	_ = json.Unmarshal(b.Skipped, &v.Skipped)
	for _, t := range tasks {
		name := ""
		if x, err := s.store.GetServer(ctx, t.ServerID); err == nil {
			name = x.Name
		}
		v.Tasks = append(v.Tasks, BatchTask{Task: t, ServerName: name})
		v.Counts[string(t.Status)]++
	}
	slices.SortFunc(v.Tasks, func(a, b BatchTask) int { return strings.Compare(a.ServerName, b.ServerName) })
	return v, nil
}

// GetBatch returns a batch with the state of every task (results are read per task).
func (s *Service) GetBatch(ctx context.Context, actor *identity.Principal, id string) (*BatchView, error) {
	b, err := s.batchInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	return s.batchView(ctx, b)
}

func (s *Service) ListBatches(ctx context.Context, actor *identity.Principal, limit int) ([]BatchView, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	list, err := s.store.ListTaskBatches(ctx, actor.User.OrganizationID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]BatchView, 0, len(list))
	for i := range list {
		v, err := s.batchView(ctx, &list[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}
