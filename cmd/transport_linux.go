//go:build linux

package cmd

import (
	"fmt"
	"io"
	"net"

	"github.com/mdlayher/vsock"
)

// listenVsock binds AF_VSOCK on the given port across all CIDs. On a
// guest with virtio-vsock the host reaches it via the guest's CID
// (allocated by the hypervisor at start time).
func listenVsock(port uint32) (net.Listener, error) {
	l, err := vsock.Listen(port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return l, nil
}

// dialVsock opens an AF_VSOCK connection to (cid, port). cid is allocated
// by the hypervisor — for cocoon-managed VMs read it from `cocoon vm
// inspect`.
func dialVsock(cid, port uint32) (io.ReadWriteCloser, error) {
	conn, err := vsock.Dial(cid, port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	return conn, nil
}
