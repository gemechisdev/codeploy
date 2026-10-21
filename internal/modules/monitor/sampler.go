package monitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/platform"
)

// SampleOnce takes one reading of the host and of every managed container, stores it, and returns the host metric
// so the caller can publish it on the event bus (the contract's metrics.sample event).
//
// The host and its containers are stamped with the same instant, which is what lets a chart overlay them.
//
// Nothing here is estimated. When a source is unavailable the field is zero and the reason is logged: a chart that
// goes flat at the moment Docker stopped answering is a true picture of the last hour, and one that carried on
// waving using the previous reading is not.
func (s *Service) SampleOnce(ctx context.Context) (Metric, error) {
	at := s.now()
	h := s.host()
	snapshot := s.counters()

	m := Metric{
		MemoryUsedMB:  h.RAMUsedMB,
		MemoryTotalMB: h.RAMTotalMB,
		DaemonRSSMB:   float64(h.DaemonRSSMB),
		DiskUsedGB:    h.DiskUsedGB,
		DiskTotalGB:   h.DiskTotalGB,
		At:            s.ts(at),
	}

	// CPU and network are counters, not gauges: the panel's number is the change since the last reading over the
	// elapsed time. A fresh daemon has no previous reading, so its first sample reports zero for both and the second
	// one is real. Pretending otherwise — repeating the last rate, or guessing from /proc load — would put a value
	// on a chart that nothing measured.
	if !s.prevAt.IsZero() {
		if dt := at.Sub(s.prevAt).Seconds(); dt > 0 {
			m.CPUPercent = cpuPercent(s.prev, snapshot, dt)
			m.NetRxKbps, m.NetTxKbps = netKbps(s.prev, snapshot, dt)
		}
	}
	s.prev, s.prevAt = snapshot, at

	states, err := s.containers(ctx)
	if err != nil {
		slog.Warn("metrics: container list unavailable; recording zero", "err", err)
	}
	m.ContainerCnt = countRunning(states)

	if err := s.Insert(ctx, s.DB, ScopeHost, "", m); err != nil {
		return m, err
	}
	if err := s.sampleContainers(ctx, at, states); err != nil {
		// The host point is already stored, and losing the per-container series for one tick does not make the host
		// chart wrong. Reported, not fatal.
		slog.Warn("metrics: container samples failed", "err", err)
	}
	return m, nil
}

// sampleContainers writes one resource-scoped point per running app container.
//
// Two containers can carry the same resource id during a blue/green switch, and the resource has one chart, so the
// readings are summed: the resource is what the operator asked about, and its real footprint during a switch is
// both processes.
func (s *Service) sampleContainers(ctx context.Context, at time.Time, states []runtime.State) error {
	if s.Runtime == nil || len(states) == 0 {
		return nil
	}
	type usage struct {
		cpu   float64
		used  int64
		limit int64
	}
	byResource := map[string]*usage{}
	var order []string
	for _, st := range states {
		if !st.Running || st.Labels[runtime.LabelRole] == runtime.RoleRelease {
			// A release command is a one-off migration container: counting it as the resource's usage would make a
			// chart spike for a process that is not the app.
			continue
		}
		id := st.Labels[runtime.LabelResource]
		if id == "" {
			continue
		}
		stats, err := s.Runtime.Stats(ctx, st.ID)
		if err != nil {
			slog.Warn("metrics: container stats failed", "container", st.ID, "err", err)
			continue
		}
		u := byResource[id]
		if u == nil {
			u = &usage{}
			byResource[id] = u
			order = append(order, id)
		}
		u.cpu += stats.CPUPercent
		u.used += stats.MemoryUsedMB
		if stats.MemoryLimitMB > u.limit {
			u.limit = stats.MemoryLimitMB
		}
	}
	for _, id := range order {
		u := byResource[id]
		m := Metric{
			CPUPercent:    round1(u.cpu),
			MemoryUsedMB:  int(u.used),
			MemoryTotalMB: int(u.limit),
			At:            s.ts(at),
		}
		if err := s.Insert(ctx, s.DB, ScopeResource, id, m); err != nil {
			return err
		}
	}
	return nil
}

// containers lists what this panel owns. It is the managed label, which the engine puts on every container it
// creates, so the count is this panel's workloads rather than everything on the host.
func (s *Service) containers(ctx context.Context) ([]runtime.State, error) {
	if s.Runtime == nil {
		return nil, nil
	}
	return s.Runtime.ListContainers(ctx, runtime.LabelManaged)
}

func countRunning(states []runtime.State) int {
	n := 0
	for _, st := range states {
		if st.Running {
			n++
		}
	}
	return n
}

// cpuPercent is the share of all online CPU time used between two readings: busy ticks over total ticks. The idle
// figure /proc/stat reports as field 4 plus field 5 (idle plus iowait), because a core waiting on I/O is not a free
// core, and the kernel counts the two separately only for accounting.
func cpuPercent(prev, now platform.Snapshot, dtSeconds float64) float64 {
	totalDelta := int64(now.CPUTotal) - int64(prev.CPUTotal)
	idleDelta := int64(now.CPUIdle) - int64(prev.CPUIdle)
	if totalDelta <= 0 || idleDelta < 0 {
		return 0
	}
	busy := totalDelta - idleDelta
	if busy < 0 {
		return 0
	}
	return round1(float64(busy) / float64(totalDelta) * 100)
}

// netKbps returns (rx, tx) in kilobytes per second, the unit docker stats uses. A negative delta means the counters
// went backwards, which happens when a counter wraps or an interface disappears; reporting zero is the honest
// answer and reporting the wrap as a spike is not.
func netKbps(prev, now platform.Snapshot, dtSeconds float64) (float64, float64) {
	if dtSeconds <= 0 {
		return 0, 0
	}
	rx := float64(now.RxBytes-prev.RxBytes) / 1024 / dtSeconds
	tx := float64(now.TxBytes-prev.TxBytes) / 1024 / dtSeconds
	if rx < 0 {
		rx = 0
	}
	if tx < 0 {
		tx = 0
	}
	return round1(rx), round1(tx)
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
