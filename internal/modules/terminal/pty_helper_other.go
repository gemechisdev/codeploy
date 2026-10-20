//go:build !linux

package terminal

import "errors"

type HelperPolicy struct{ Passwd, Group, AllowRootFile, ServiceUser string }

func DefaultHelperPolicy() HelperPolicy { return HelperPolicy{} }

// ServeHelperStdin refuses: the privileged PTY helper is Linux-only.
func ServeHelperStdin(HelperPolicy) error {
	return errors.New("the host shell helper is only available on Linux")
}
