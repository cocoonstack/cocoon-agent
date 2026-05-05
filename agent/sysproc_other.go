//go:build !linux

package agent

import "os/exec"

// setProcessGroup is a no-op on non-Linux; production targets Linux guests.
func setProcessGroup(_ *exec.Cmd) {}
