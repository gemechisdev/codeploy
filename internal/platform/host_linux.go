//go:build linux

// Package platform: OS detection and host facts (no shell string building anywhere).
package platform

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type HostInfo struct {
	Hostname, OS                       string
	RAMTotalMB, RAMUsedMB, DaemonRSSMB int
	DiskUsedGB, DiskTotalGB            float64
}

func kb(line string) int {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n / 1024
}

func Host() HostInfo {
	h := HostInfo{OS: "linux"}
	h.Hostname, _ = os.Hostname()
	if f, err := os.Open("/etc/os-release"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
				h.OS = strings.Trim(v, `"`)
			}
		}
		f.Close()
	}
	var avail int
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			switch l := sc.Text(); {
			case strings.HasPrefix(l, "MemTotal:"):
				h.RAMTotalMB = kb(l)
			case strings.HasPrefix(l, "MemAvailable:"):
				avail = kb(l)
			}
		}
		f.Close()
	}
	h.RAMUsedMB = h.RAMTotalMB - avail
	if f, err := os.Open("/proc/self/status"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "VmRSS:") {
				h.DaemonRSSMB = kb(l)
			}
		}
		f.Close()
	}
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) == nil {
		total := float64(st.Blocks) * float64(st.Bsize) / (1 << 30)
		free := float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
		h.DiskTotalGB, h.DiskUsedGB = total, total-free
	}
	return h
}
