//go:build !linux

package cmd

import (
	"errors"
	"io"
)

func dialVsock(_, _ uint32) (io.ReadWriteCloser, error) {
	return nil, errors.New("vsock is only supported on linux; cross-build with GOOS=linux for production")
}
