//go:build !linux

package cmd

import (
	"errors"
	"io"
	"net"
)

// vsockOnlyOnLinux keeps `make build` working on darwin developer
// machines without dragging in a vsock fake. Cross-build with
// GOOS=linux for production.
var errVsockOnlyOnLinux = errors.New("vsock is only supported on linux; cross-build with GOOS=linux for production")

func listenVsock(_ uint32) (net.Listener, error) {
	return nil, errVsockOnlyOnLinux
}

func dialVsock(_, _ uint32) (io.ReadWriteCloser, error) {
	return nil, errVsockOnlyOnLinux
}
