//go:build !linux

package deploy

import "errors"

// The daemon only runs on Linux; these let the package compile (and its fake-driven tests run) elsewhere.
func DiskFree(path string) (free, total uint64, err error) { return 0, 0, errors.New("unsupported OS") }
func MemAvailable() (uint64, error)                        { return 0, errors.New("unsupported OS") }
