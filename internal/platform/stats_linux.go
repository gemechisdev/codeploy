//go:build linux

// Package platform: the counters a metrics sampler needs between two ticks. Nothing here computes a rate;
// differences are the sampler's job, because a rate needs two readings and the daemon must survive a restart
// between them.
package platform

import (
	"os"
	"strconv"
	"strings"
)

// Snapshot is one read of the host's cumulative counters.
type Snapshot struct {
	// CPUTotal and CPUIdle are jiffies from /proc/stat's aggregate line, idle including iowait; the percent
	// between two snapshots is (1 - didt/dtotal).
	CPUTotal, CPUIdle uint64
	// RxBytes and TxBytes are summed over every interface except loopback, where "interface" means the host's
	// real NICs and the docker bridges; a container's traffic crosses both, which is why this is a host-level
	// number and is never attributed to a resource.
	RxBytes, TxBytes int64
}

// Sample reads the host counters. A field that cannot be read stays zero, so a missing /proc entry degrades to
// "unknown" rather than failing the whole tick.
func Sample() Snapshot {
	var s Snapshot
	s.CPUTotal, s.CPUIdle = readCPU()
	s.RxBytes, s.TxBytes = readNet()
	return s
}

func readCPU() (total, idle uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		for i, v := range f[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			total += n
			// fields 4 and 5 are idle and iowait; both mean "the CPU was not doing work".
			if i == 3 || i == 4 {
				idle += n
			}
		}
		return total, idle
	}
	return 0, 0
}

func readNet() (rx, tx int64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, counters, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" {
			continue
		}
		f := strings.Fields(counters)
		if len(f) < 10 {
			continue
		}
		r, e1 := strconv.ParseInt(f[1], 10, 64)
		t, e2 := strconv.ParseInt(f[9], 10, 64)
		if e1 == nil {
			rx += r
		}
		if e2 == nil {
			tx += t
		}
	}
	return rx, tx
}
