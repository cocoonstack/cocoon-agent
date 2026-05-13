//go:build !linux && !windows

package cmd

import (
	"context"
	"errors"
	"io"
	"net"
)

// errVsockUnsupported keeps `make build` working on darwin dev machines.
// Production targets are Linux + Windows guests; vsock on darwin/freebsd
// makes no sense.
var errVsockUnsupported = errors.New("vsock is only supported on linux and windows; cross-build with GOOS=linux or GOOS=windows for production")

func listenVsock(_ context.Context, _ uint32) (net.Listener, error) {
	return nil, errVsockUnsupported
}

func dialVsock(_, _ uint32) (io.ReadWriteCloser, error) {
	return nil, errVsockUnsupported
}
