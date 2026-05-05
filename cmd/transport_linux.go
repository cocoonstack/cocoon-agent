//go:build linux

package cmd

import (
	"fmt"
	"io"
	"net"

	"github.com/mdlayher/vsock"
)

func listenVsock(port uint32) (net.Listener, error) {
	l, err := vsock.Listen(port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return l, nil
}

func dialVsock(cid, port uint32) (io.ReadWriteCloser, error) {
	conn, err := vsock.Dial(cid, port, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	return conn, nil
}
