//go:build linux

package cmd

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/mdlayher/vsock"
)

func TestIsHostPeer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		addr net.Addr
		want bool
	}{
		{
			name: "host CID is accepted",
			addr: &vsock.Addr{ContextID: vsock.Host, Port: 1024},
			want: true,
		},
		{
			name: "guest-local CID is rejected",
			addr: &vsock.Addr{ContextID: vsock.Local, Port: 1024},
			want: false,
		},
		{
			name: "non-vsock RemoteAddr is rejected",
			addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1024},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isHostPeer(&staticAddrConn{addr: tc.addr}); got != tc.want {
				t.Errorf("isHostPeer = %v, want %v", got, tc.want)
			}
		})
	}
}

type staticAddrConn struct {
	addr net.Addr
}

func (c *staticAddrConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *staticAddrConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *staticAddrConn) Close() error                       { return nil }
func (c *staticAddrConn) LocalAddr() net.Addr                { return c.addr }
func (c *staticAddrConn) RemoteAddr() net.Addr               { return c.addr }
func (c *staticAddrConn) SetDeadline(_ time.Time) error      { return nil }
func (c *staticAddrConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *staticAddrConn) SetWriteDeadline(_ time.Time) error { return nil }
