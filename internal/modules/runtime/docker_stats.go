package runtime

import (
	"context"
	"fmt"
)

// ContainerStats is one non-streaming stats read of a single container. Docker computes the CPU percent from the
// delta between its last two samples, so a container that has been idle for a minute reports the busy fraction of
// that minute, not of the interval the panel sampled over. That is the number the charts and the alert thresholds
// are written against.
type ContainerStats struct {
	CPUPercent    float64
	MemoryUsedMB  int64
	MemoryLimitMB int64
}

// statsBody is the subset of /containers/{id}/stats the panel reads. Every field is a float because Docker sends
// nanosecond counters and byte counts that overflow an int on a large host.
type statsBody struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage float64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage float64 `json:"system_cpu_usage"`
		OnlineCPUs     float64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage float64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage float64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage float64 `json:"usage"`
		Limit float64 `json:"limit"`
	} `json:"memory_stats"`
}

// Stats reads one container's CPU and memory usage. stream=false is what makes this a single sample rather than a
// subscription: the sampler runs once a minute and must not hold a connection open between ticks.
func (d *Docker) Stats(ctx context.Context, container string) (ContainerStats, error) {
	if err := checkID(container); err != nil {
		return ContainerStats{}, err
	}
	var b statsBody
	// The query string belongs to the path here rather than to get()'s signature; stats has exactly one option
	// that matters and an unencoded "?" is how the rest of this package addresses the socket.
	if err := d.get(ctx, "/containers/"+container+"/stats?stream=false", &b); err != nil {
		return ContainerStats{}, fmt.Errorf("container stats: %w", err)
	}
	var s ContainerStats
	cpuDelta := b.CPUStats.CPUUsage.TotalUsage - b.PreCPUStats.CPUUsage.TotalUsage
	sysDelta := b.CPUStats.SystemCPUUsage - b.PreCPUStats.SystemCPUUsage
	if cpuDelta > 0 && sysDelta > 0 {
		cpus := b.CPUStats.OnlineCPUs
		if cpus <= 0 {
			cpus = 1
		}
		s.CPUPercent = cpuDelta / sysDelta * cpus * 100
	}
	const mb = 1 << 20
	s.MemoryUsedMB = int64(b.MemoryStats.Usage / mb)
	s.MemoryLimitMB = int64(b.MemoryStats.Limit / mb)
	return s, nil
}
