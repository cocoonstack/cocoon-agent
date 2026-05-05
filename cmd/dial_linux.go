//go:build linux

package cmd

import (
	"fmt"
	"io"

	"github.com/mdlayher/vsock"
)

// dialVsock opens an AF_VSOCK connection to (cid, port). cid is allocated
// by the hypervisor — for cocoon-managed VMs, the host can read it from
// `cocoon vm inspect`. cocoon-agent's `client` subcommand is for smoke
// tests; production callers will go through cocoon CLI / vk-cocoon.
func dialVsock(cid, port uint32) (io.ReadWriteCloser, error) {
	conn, err := vsock.Dial(cid, port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	return conn, nil
}
