//go:build !linux

package platform

// Snapshot is the host's cumulative counters. A development machine has no /proc, so every field is zero and the
// sampler records what it can: rates read 0 and the panel's own RSS is what the host page shows.
type Snapshot struct {
	CPUTotal, CPUIdle uint64
	RxBytes, TxBytes  int64
}

func Sample() Snapshot { return Snapshot{} }
