package monitor

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Rollup is one hour of a server's metrics. Averages are of the raw samples in
// the hour; values a platform does not report stay nil.
type Rollup struct {
	Hour        time.Time `json:"ts"`
	Samples     int       `json:"samples"`
	CPUAvg      *float64  `json:"cpu_percent,omitempty"`
	CPUMax      *float64  `json:"cpu_max,omitempty"`
	MemUsedAvg  *uint64   `json:"mem_used_bytes,omitempty"`
	MemTotal    *uint64   `json:"mem_total_bytes,omitempty"`
	Load1Avg    *float64  `json:"load1,omitempty"`
	NetRxAvg    *float64  `json:"net_rx_bps,omitempty"`
	NetTxAvg    *float64  `json:"net_tx_bps,omitempty"`
	DiskUsedPct *float64  `json:"disk_used_max_percent,omitempty"` // fullest disk, highest value in the hour
}

type avg struct {
	sum float64
	n   int
}

func (a *avg) add(v *float64) {
	if v != nil {
		a.sum += *v
		a.n++
	}
}

func (a avg) value() *float64 {
	if a.n == 0 {
		return nil
	}
	v := math.Round(a.sum/float64(a.n)*100) / 100
	return &v
}

// BuildRollup aggregates raw samples of one hour.
func BuildRollup(hour time.Time, samples []metrics.Sample) Rollup {
	r := Rollup{Hour: hour, Samples: len(samples)}
	var cpu, load, rx, tx avg
	var memSum, memN uint64
	for i := range samples {
		m := &samples[i]
		cpu.add(m.CPUPercent)
		load.add(m.Load1)
		rx.add(m.NetRxBps)
		tx.add(m.NetTxBps)
		if m.CPUPercent != nil && (r.CPUMax == nil || *m.CPUPercent > *r.CPUMax) {
			v := *m.CPUPercent
			r.CPUMax = &v
		}
		if m.MemUsedBytes != nil {
			memSum += *m.MemUsedBytes
			memN++
		}
		if m.MemTotalBytes != nil {
			t := *m.MemTotalBytes
			r.MemTotal = &t
		}
		for _, d := range m.Disks {
			if d.TotalBytes > 0 {
				p := math.Round(float64(d.UsedBytes)*10000/float64(d.TotalBytes)) / 100
				if r.DiskUsedPct == nil || p > *r.DiskUsedPct {
					r.DiskUsedPct = &p
				}
			}
		}
	}
	r.CPUAvg, r.Load1Avg, r.NetRxAvg, r.NetTxAvg = cpu.value(), load.value(), rx.value(), tx.value()
	if memN > 0 {
		v := memSum / memN
		r.MemUsedAvg = &v
	}
	return r
}

// RunRollups writes the rollups of the last 23 complete hours that are missing
// (raw samples are kept for 24 h) and prunes rollups older than 90 days.
func (s *Service) RunRollups(ctx context.Context, now time.Time) {
	servers, err := s.allServers(ctx)
	if err != nil {
		return
	}
	current := now.UTC().Truncate(time.Hour)
	for _, x := range servers {
		have := map[int64]bool{}
		existing, err := s.store.ListRollups(ctx, x.ID, current.Add(-23*time.Hour))
		if err != nil {
			continue
		}
		for _, r := range existing {
			have[r.Hour.Unix()] = true
		}
		rows, err := s.store.ListMetrics(ctx, x.ID, current.Add(-23*time.Hour), 0)
		if err != nil {
			continue
		}
		byHour := map[int64][]metrics.Sample{}
		for _, row := range rows {
			var m metrics.Sample
			if json.Unmarshal(row.Data, &m) != nil {
				continue
			}
			h := row.TS.UTC().Truncate(time.Hour)
			if !h.Before(current) || have[h.Unix()] {
				continue // the current hour is not complete yet
			}
			byHour[h.Unix()] = append(byHour[h.Unix()], m)
		}
		for unix, samples := range byHour {
			h := time.Unix(unix, 0).UTC()
			data, _ := json.Marshal(BuildRollup(h, samples))
			if err := s.store.UpsertRollup(ctx, &store.MetricRollup{ServerID: x.ID, Hour: h, Data: data}); err != nil {
				s.log.Warn("metric rollup", "server", x.ID, "err", err)
			}
		}
	}
	if _, err := s.store.PruneRollups(ctx, now.Add(-rollupRetention)); err != nil {
		s.log.Warn("prune rollups", "err", err)
	}
}

func (s *Service) allServers(ctx context.Context) ([]store.Server, error) {
	return s.store.ListServers(ctx, store.DefaultOrganizationID)
}

// RunAuditRetention deletes audit entries older than the configured retention
// and records how many were removed.
func (s *Service) RunAuditRetention(ctx context.Context, now time.Time) {
	days, err := s.id.AuditRetentionDays(ctx)
	if err != nil {
		return
	}
	n, err := s.store.PruneAudit(ctx, now.Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		s.log.Warn("audit retention", "err", err)
		return
	}
	if n > 0 {
		s.id.Audit(ctx, nil, "audit.pruned", "", "", store.OutcomeSuccess, map[string]any{"deleted": n, "retention_days": days})
	}
}

// Rollups returns hourly rollups for long windows (beyond the 24 h of raw samples).
func (s *Service) Rollups(ctx context.Context, serverID string, since time.Time) ([]json.RawMessage, error) {
	list, err := s.store.ListRollups(ctx, serverID, since)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(list))
	for i := range list {
		out[i] = list[i].Data
	}
	return out, nil
}

// Run evaluates alerts every tick, writes rollups every 5 minutes and applies
// audit retention every hour, until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastRollup, lastRetention time.Time
	for {
		now := s.id.Now()
		s.Evaluate(ctx, now)
		if now.Sub(lastRollup) >= 5*time.Minute {
			s.RunRollups(ctx, now)
			lastRollup = now
		}
		if now.Sub(lastRetention) >= time.Hour {
			s.RunAuditRetention(ctx, now)
			lastRetention = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
