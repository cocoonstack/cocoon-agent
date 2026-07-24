//go:build linux

package cmd

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/mdlayher/vsock"
	"github.com/projecteru2/core/log"
)

func listenVsock(ctx context.Context, port uint32) (net.Listener, error) {
	l, err := vsock.Listen(port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &hostOnlyListener{
		Listener: l,
		ctx:      ctx,
		logger:   log.WithFunc("cmd.hostOnlyListener.Accept"),
	}, nil
}

func dialVsock(cid, port uint32) (io.ReadWriteCloser, error) {
	conn, err := vsock.Dial(cid, port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	return conn, nil
}

var _ net.Listener = (*hostOnlyListener)(nil)

// Peers other than VMADDR_CID_HOST are rejected: VMADDR_CID_LOCAL loopback would let an unprivileged guest process run commands as root.
type hostOnlyListener struct {
	net.Listener
	// ctx is the serve ctx, stashed for Accept-loop diagnostic logging.
	ctx    context.Context
	logger *log.Fields
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
		l.logger.Warnf(l.ctx, "rejecting non-host vsock peer %s", conn.RemoteAddr())
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
