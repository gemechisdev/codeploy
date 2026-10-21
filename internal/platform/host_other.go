//go:build !linux

package platform

import "os"

type HostInfo struct {
	Hostname, OS                       string
	RAMTotalMB, RAMUsedMB, DaemonRSSMB int
	DiskUsedGB, DiskTotalGB            float64
}

// Host returns minimal facts on non-Linux dev machines (production target is Linux).
func Host() HostInfo { h, _ := os.Hostname(); return HostInfo{Hostname: h, OS: "dev"} }

// AppArmorEnabled is false anywhere AppArmor cannot run, so a dev machine never asks Docker for a profile.
func AppArmorEnabled() bool { return false }
