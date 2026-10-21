package deploy

import (
	"fmt"
)

const (
	gib = uint64(1) << 30
	mib = uint64(1) << 20
	// ramGuardMin is the smallest build budget (50% of available RAM) that an on-host build is allowed to start with.
	ramGuardMin = 256 * mib
)

// PreflightResult mirrors the OpenAPI Deployment.preflight object.
type PreflightResult struct {
	OK             bool    `json:"ok"`
	Detail         string  `json:"detail"`
	DiskFreeGB     float64 `json:"disk_free_gb"`
	DiskRequiredGB float64 `json:"disk_required_gb"`
	RAMOK          bool    `json:"ram_ok"`
	// BuildMemory is the memory limit to give the build (<= 50% of available RAM); zero for pull deployments.
	BuildMemory int64 `json:"-"`
}

// RequiredDisk is max(thresholdGiB, 10% of the filesystem), the plan's pre-flight rule.
func RequiredDisk(total uint64, thresholdGiB int) uint64 {
	req := uint64(thresholdGiB) * gib
	if tenth := total / 10; tenth > req {
		req = tenth
	}
	return req
}

// preflight checks free disk and, for builds, the RAM guard. It does no side effects.
func (e *Engine) preflight(build bool, thresholdGiB int) PreflightResult {
	r := PreflightResult{OK: true, RAMOK: true}
	free, total, err := e.cfg.Disk(e.cfg.WorkDir)
	if err != nil {
		// an unreadable filesystem is not a reason to block a deploy, but the log says so
		r.Detail = "disk check unavailable: " + err.Error()
	} else {
		req := RequiredDisk(total, thresholdGiB)
		r.DiskFreeGB, r.DiskRequiredGB = round1(float64(free)/float64(gib)), round1(float64(req)/float64(gib))
		if free < req {
			r.OK = false
			r.Detail = fmt.Sprintf("Not enough free disk space: %.1f GB free, %.1f GB required (the larger of %d GB and 10%% of the disk). Run cleanup or free space, then redeploy.", r.DiskFreeGB, r.DiskRequiredGB, thresholdGiB)
			return r
		}
	}
	if build {
		avail, err := e.cfg.MemAvailable()
		if err == nil {
			budget := avail / 2
			if budget < ramGuardMin {
				r.OK, r.RAMOK = false, false
				r.Detail = fmt.Sprintf("Not enough free memory to build on this host (%d MB available, a build needs at least %d MB of headroom). Use a prebuilt image or stop something first.", avail/mib, 2*ramGuardMin/mib)
				return r
			}
			r.BuildMemory = int64(budget)
		}
	}
	if r.Detail == "" {
		r.Detail = "Disk and RAM guard passed"
	}
	return r
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
