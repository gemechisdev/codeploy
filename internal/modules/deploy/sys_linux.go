//go:build linux

package deploy

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// DiskFree returns free and total bytes of the filesystem holding path (unprivileged blocks only).
func DiskFree(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), nil
}

// MemAvailable returns MemAvailable from /proc/meminfo in bytes.
func MemAvailable() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "MemAvailable:") {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 2 {
				kb, err := strconv.ParseUint(fs[1], 10, 64)
				return kb * 1024, err
			}
		}
	}
	return 0, os.ErrNotExist
}
