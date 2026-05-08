//go:build !linux && !windows

package agent

import "os/exec"

// setupProcess is a no-op on development platforms that are not guest targets.
func setupProcess(_ *exec.Cmd) (processController, error) {
	return processController{}, nil
}
