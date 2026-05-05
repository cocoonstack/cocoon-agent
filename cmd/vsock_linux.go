//go:build linux

package cmd

import (
	"fmt"

	"github.com/mdlayher/vsock"

	"github.com/cocoonstack/cocoon-agent/agent"
)

// listenVsock binds to AF_VSOCK on the given port across all CIDs. On a
// guest with virtio-vsock, this is reachable from the host via the guest's
// CID (allocated by the hypervisor at start time).
func listenVsock(port uint32) (agent.Listener, error) {
	l, err := vsock.Listen(port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return l, nil
}
