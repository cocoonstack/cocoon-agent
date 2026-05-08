//go:build linux

package cmd

import (
	"fmt"
	"io"
	"net"

	"github.com/mdlayher/vsock"
)

var _ net.Listener = (*hostOnlyListener)(nil)

func listenVsock(port uint32) (net.Listener, error) {
	l, err := vsock.Listen(port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &hostOnlyListener{Listener: l}, nil
}

func dialVsock(cid, port uint32) (io.ReadWriteCloser, error) {
	conn, err := vsock.Dial(cid, port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	return conn, nil
}

// hostOnlyListener rejects any peer whose CID is not VMADDR_CID_HOST.
// Without this, a guest-local unprivileged process could connect via
// VMADDR_CID_LOCAL (loopback, when the kernel has CONFIG_VSOCKETS_LOOPBACK)
// and trigger root-level command execution.
type hostOnlyListener struct {
	net.Listener
}

func (l *hostOnlyListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if isHostPeer(conn) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func isHostPeer(conn net.Conn) bool {
	addr, ok := conn.RemoteAddr().(*vsock.Addr)
	if !ok {
		return false
	}
	return addr.ContextID == vsock.Host
}
