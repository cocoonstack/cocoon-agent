//go:build !linux && !windows

package agent

import "os/exec"

func setupProcess(_ *exec.Cmd) (processController, error) {
	return processController{}, nil
}
