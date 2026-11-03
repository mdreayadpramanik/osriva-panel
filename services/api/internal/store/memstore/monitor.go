package memstore

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func copyChannel(c *store.NotificationChannel) *store.NotificationChannel {
	x := *c
	x.Config, x.SecretEnc = bytes.Clone(c.Config), bytes.Clone(c.SecretEnc)
	return &x
}

func (s *Store) CreateChannel(_ context.Context, c *store.NotificationChannel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.channels[c.ID]; dup {
		return store.ErrConflict
	}
	c.CreatedAt = time.Now().UTC()
	s.channels[c.ID] = copyChannel(c)
	return nil
}

func (s *Store) GetChannel(_ context.Context, id string) (*store.NotificationChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyChannel(c), nil
}

func (s *Store) ListChannels(_ context.Context, orgID string) ([]store.NotificationChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.NotificationChannel{}
	for _, c := range s.channels {
		if c.OrganizationID == orgID {
			out = append(out, *copyChannel(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) UpdateChannel(_ context.Context, c *store.NotificationChannel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.channels[c.ID]
	if !ok {
		return store.ErrNotFound
	}
	c.OrganizationID, c.Kind, c.CreatedBy, c.CreatedAt = old.OrganizationID, old.Kind, old.CreatedBy, old.CreatedAt
	s.channels[c.ID] = copyChannel(c)
	return nil
}

func (s *Store) DeleteChannel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.channels, id)
	for _, r := range s.rules {
		r.ChannelIDs = slices.DeleteFunc(r.ChannelIDs, func(x string) bool { return x == id })
	}
	return nil
}

func copyRule(r *store.AlertRule) *store.AlertRule {
	x := *r
	x.ChannelIDs = slices.Clone(r.ChannelIDs)
	if x.ChannelIDs == nil {
		x.ChannelIDs = []string{}
	}
	return &x
}

func (s *Store) CreateAlertRule(_ context.Context, r *store.AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.rules[r.ID]; dup {
		return store.ErrConflict
	}
	if r.GroupID != nil {
		if _, ok := s.groups[*r.GroupID]; !ok {
			return store.ErrNotFound
		}
	}
	if r.ServerID != nil {
		if _, ok := s.servers[*r.ServerID]; !ok {
			return store.ErrNotFound
		}
	}
	r.CreatedAt = time.Now().UTC()
	s.rules[r.ID] = copyRule(r)
	return nil
}

func (s *Store) GetAlertRule(_ context.Context, id string) (*store.AlertRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rules[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyRule(r), nil
}

func (s *Store) ListAlertRules(_ context.Context, orgID string) ([]store.AlertRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.AlertRule{}
	for _, r := range s.rules {
		if orgID == "" || r.OrganizationID == orgID {
			out = append(out, *copyRule(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) UpdateAlertRule(_ context.Context, r *store.AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.rules[r.ID]
	if !ok {
		return store.ErrNotFound
	}
	if r.GroupID != nil {
		if _, ok := s.groups[*r.GroupID]; !ok {
			return store.ErrNotFound
		}
	}
	r.OrganizationID, r.CreatedBy, r.CreatedAt = old.OrganizationID, old.CreatedBy, old.CreatedAt
	s.rules[r.ID] = copyRule(r)
	return nil
}

func (s *Store) DeleteAlertRule(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.rules, id)
	for k := range s.alertStates {
		if k[0] == id {
			delete(s.alertStates, k)
		}
	}
	return nil
}

func (s *Store) GetAlertState(_ context.Context, ruleID, serverID string) (*store.AlertState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.alertStates[[2]string{ruleID, serverID}]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *st
	return &c, nil
}

func (s *Store) PutAlertState(_ context.Context, st *store.AlertState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[st.RuleID]; !ok {
		return store.ErrNotFound
	}
	c := *st
	s.alertStates[[2]string{st.RuleID, st.ServerID}] = &c
	return nil
}

func (s *Store) InsertAlertEvent(_ context.Context, e *store.AlertEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.CreatedAt = time.Now().UTC()
	c := *e
	s.alertEvents = append(s.alertEvents, &c)
	return nil
}

func (s *Store) SetAlertEventDelivery(_ context.Context, id string, delivered, failed int, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.alertEvents {
		if e.ID == id {
			e.Delivered, e.Failed, e.DeliveryError = delivered, failed, errMsg
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *Store) RecordAlertDelivery(_ context.Context, id string, ok bool, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.alertEvents {
		if e.ID == id {
			if e.Pending > 0 {
				e.Pending--
			}
			if ok {
				e.Delivered++
				return nil
			}
			e.Failed++
			e.DeliveryError = store.AppendDeliveryError(e.DeliveryError, errMsg)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *Store) ListAlertEvents(_ context.Context, orgID string, limit int) ([]store.AlertEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.AlertEvent{}
	for i := len(s.alertEvents) - 1; i >= 0; i-- {
		e := s.alertEvents[i]
		if e.OrganizationID != orgID {
			continue
		}
		c := *e
		if c.RuleID != nil {
			if _, ok := s.rules[*c.RuleID]; !ok {
				c.RuleID = nil
			}
		}
		out = append(out, c)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *Store) UpsertRollup(_ context.Context, r *store.MetricRollup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[r.ServerID]; !ok {
		return store.ErrNotFound
	}
	list := s.rollups[r.ServerID]
	for i := range list {
		if list[i].Hour.Equal(r.Hour) {
			list[i].Data = bytes.Clone(r.Data)
			return nil
		}
	}
	list = append(list, store.MetricRollup{ServerID: r.ServerID, Hour: r.Hour, Data: bytes.Clone(r.Data)})
	sort.Slice(list, func(i, j int) bool { return list[i].Hour.Before(list[j].Hour) })
	s.rollups[r.ServerID] = list
	return nil
}

func (s *Store) ListRollups(_ context.Context, serverID string, since time.Time) ([]store.MetricRollup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.MetricRollup{}
	for _, r := range s.rollups[serverID] {
		if !r.Hour.Before(since) {
			out = append(out, store.MetricRollup{ServerID: r.ServerID, Hour: r.Hour, Data: bytes.Clone(r.Data)})
		}
	}
	return out, nil
}

func (s *Store) PruneRollups(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, list := range s.rollups {
		kept := list[:0]
		for _, r := range list {
			if r.Hour.Before(before) {
				n++
				continue
			}
			kept = append(kept, r)
		}
		s.rollups[id] = kept
	}
	return n, nil
}

func (s *Store) PruneAudit(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.audit[:0]
	n := 0
	for _, e := range s.audit {
		if e.CreatedAt.Before(before) {
			n++
			continue
		}
		kept = append(kept, e)
	}
	s.audit = kept
	return n, nil
}

func (s *Store) GetAlertEvent(_ context.Context, id string) (*store.AlertEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.alertEvents {
		if e.ID == id {
			c := *e
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}
