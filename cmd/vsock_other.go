//go:build !linux

package cmd

import (
	"errors"

	"github.com/cocoonstack/cocoon-agent/agent"
)

// listenVsock returns a clear error on non-Linux hosts. cocoon-agent is
// intended to run inside a Linux guest VM; this stub keeps `make build`
// working on darwin developer machines without dragging in a vsock fake.
func listenVsock(_ uint32) (agent.Listener, error) {
	return nil, errors.New("vsock is only supported on linux; cross-build with GOOS=linux for production")
}
