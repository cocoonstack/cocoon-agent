//go:build !linux

package agent

import "os/exec"

// setProcessGroup is a darwin/dev-build no-op. The agent runs in a Linux
// guest in production; this stub keeps `make build` working everywhere.
func setProcessGroup(_ *exec.Cmd) {}
