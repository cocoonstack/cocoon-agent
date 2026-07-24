//go:build !linux && !windows

package cmd

import (
	"context"
	"errors"
	"io"
	"net"
)

// Stub kept so `make build` works on darwin dev machines; production guests are Linux and Windows only.
var errVsockUnsupported = errors.New("vsock is only supported on linux and windows; cross-build with GOOS=linux or GOOS=windows for production")

func listenVsock(_ context.Context, _ uint32) (net.Listener, error) {
	return nil, errVsockUnsupported
}

func dialVsock(_, _ uint32) (io.ReadWriteCloser, error) {
	return nil, errVsockUnsupported
}
