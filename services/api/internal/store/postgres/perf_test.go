package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// TestPerformance measures the store paths that grow with the fleet. It asserts
// correctness only (timings depend on the machine) and logs the numbers; set
// OSRIVA_PERF=1 and, optionally, OSRIVA_PERF_OUT=file.json.
func TestPerformance(t *testing.T) {
	if os.Getenv("OSRIVA_PERF") != "1" {
		t.Skip("set OSRIVA_PERF=1 to run the performance test")
	}
	const servers, samplesPerServer, workers = 300, 60, 32
	ctx := context.Background()
	s := freshStore(t)
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	results := map[string]any{"servers": servers, "samples_per_server": samplesPerServer, "workers": workers}

	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{5}, 32))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := identity.New(identity.Options{Store: s, Keys: keys, Log: quiet,
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	if err := id.SyncRBAC(ctx); err != nil {
		t.Fatal(err)
	}

	// Enrolled, reporting servers.
	ids := make([]string, servers)
	start := time.Now()
	for i := range ids {
		token, hash, _ := auth.NewSessionToken()
		_ = token
		exp := now.Add(time.Hour)
		x := &store.Server{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Name: fmt.Sprintf("perf-%03d", i), EnrollmentTokenHash: hash, EnrollmentExpiresAt: &exp}
		if err := s.CreateServer(ctx, x); err != nil {
			t.Fatal(err)
		}
		cert := &store.AgentCertificate{Serial: fmt.Sprintf("%032x", i), ServerID: x.ID, Fingerprint: fmt.Sprintf("sha256:%d", i), NotAfter: now.Add(720 * time.Hour)}
		if err := s.CompleteEnrollment(ctx, x.ID, hash, now, cert); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordReport(ctx, x.ID, store.ServerFacts{OSFamily: "linux"}, "10.0.0.1", now, nil); err != nil {
			t.Fatal(err)
		}
		ids[i] = x.ID
	}
	results["enrol_and_report_ms_per_server"] = float64(time.Since(start).Milliseconds()) / servers

	// Metrics ingestion: each agent uploads its hour in one batch, many at once.
	run := func(n int, fn func(i int) error) (time.Duration, error) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var first error
		next := make(chan int)
		t0 := time.Now()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					if err := fn(i); err != nil {
						mu.Lock()
						if first == nil {
							first = err
						}
						mu.Unlock()
					}
				}
			}()
		}
		for i := 0; i < n; i++ {
			next <- i
		}
		close(next)
		wg.Wait()
		return time.Since(t0), first
	}
	d, err := run(servers, func(i int) error {
		batch := make([]store.MetricSample, samplesPerServer)
		for j := range batch {
			ts := now.Add(-time.Duration(samplesPerServer-j) * time.Minute)
			cpu, mem, total := float64(30+i%60), float64(2<<30), float64(8<<30)
			data, _ := json.Marshal(metrics.Sample{TS: ts, CPUPercent: &cpu, MemUsedBytes: ptrU(uint64(mem)), MemTotalBytes: ptrU(uint64(total))})
			batch[j] = store.MetricSample{ServerID: ids[i], TS: ts, Data: data}
		}
		return s.InsertMetrics(ctx, batch)
	})
	if err != nil {
		t.Fatal(err)
	}
	results["metrics_insert_samples_per_second"] = int(float64(servers*samplesPerServer) / d.Seconds())

	// Reading one server's last hour (the metrics chart).
	var lat []time.Duration
	for _, sid := range ids {
		t0 := time.Now()
		rows, err := s.ListMetrics(ctx, sid, now.Add(-time.Hour-time.Minute), 9000)
		if err != nil || len(rows) != samplesPerServer {
			t.Fatalf("ListMetrics: %d rows, %v", len(rows), err)
		}
		lat = append(lat, time.Since(t0))
	}
	results["metrics_read_1h_p50_ms"], results["metrics_read_1h_p95_ms"] = pct(lat, 50), pct(lat, 95)

	// Task polling: one task per server, every agent polls at the same moment.
	for _, sid := range ids {
		if err := s.CreateTask(ctx, &store.Task{ID: auth.NewID(), ServerID: sid, Type: "services.list", ExpiresAt: now.Add(10 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	var claimed sync.Map
	d, err = run(servers, func(i int) error {
		got, err := s.ClaimTasks(ctx, ids[i], now, 10)
		for _, x := range got {
			if _, dup := claimed.LoadOrStore(x.ID, ids[i]); dup {
				return fmt.Errorf("task %s handed out twice", x.ID)
			}
		}
		if err == nil && len(got) != 1 {
			return fmt.Errorf("server %d got %d tasks", i, len(got))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	results["task_claim_ms_per_poll"] = float64(d.Microseconds()) / 1000 / servers * workers

	// Listing the fleet.
	lat = lat[:0]
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		list, err := s.ListServers(ctx, store.DefaultOrganizationID)
		if err != nil || len(list) != servers {
			t.Fatalf("ListServers: %d %v", len(list), err)
		}
		lat = append(lat, time.Since(t0))
	}
	results["list_servers_p50_ms"] = pct(lat, 50)

	// One alert evaluation over the whole fleet with three fleet-wide rules.
	mon := monitor.New(s, id, keys, quiet, nil)
	owner, _ := id.BootstrapUser(ctx, "perf@example.com", "P", "a long enough password", rbac.RoleOwner, "perf")
	_ = owner
	for _, r := range []store.AlertRule{
		{Name: "cpu", Metric: "cpu", Threshold: 80, DurationMinutes: 5},
		{Name: "memory", Metric: "memory", Threshold: 90, DurationMinutes: 5},
		{Name: "offline", Metric: "offline", Threshold: 5, DurationMinutes: 1},
	} {
		r.ID, r.OrganizationID, r.Enabled, r.ChannelIDs = auth.NewID(), store.DefaultOrganizationID, true, []string{}
		if err := s.CreateAlertRule(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now()
	mon.Evaluate(ctx, now)
	results["alert_evaluation_ms_300_servers_3_rules"] = time.Since(t0).Milliseconds()
	t0 = time.Now()
	mon.Evaluate(ctx, now.Add(time.Second))
	results["alert_evaluation_ms_second_pass"] = time.Since(t0).Milliseconds()
	events, _ := s.ListAlertEvents(ctx, store.DefaultOrganizationID, 1000)
	firing := 0
	for _, e := range events {
		if e.State == "firing" {
			firing++
		}
	}
	// cpu 30+i%60 > 80 for i%60 >= 51: 9 of every 60 servers.
	if want := servers / 60 * 9; firing != want {
		t.Fatalf("firing events: %d, want %d", firing, want)
	}
	results["alerts_firing"] = firing

	out, _ := json.MarshalIndent(results, "", "  ")
	t.Logf("performance results:\n%s", out)
	if p := os.Getenv("OSRIVA_PERF_OUT"); p != "" {
		_ = os.WriteFile(p, out, 0o644)
	}
}

func ptrU(v uint64) *uint64 { return &v }

func pct(d []time.Duration, p int) float64 {
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	i := (len(c) - 1) * p / 100
	return float64(c[i].Microseconds()) / 1000
}
