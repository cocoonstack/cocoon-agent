//go:build linux

package cmd

import (
	"net"
	"testing"

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
	net.Conn
	addr net.Addr
}

func (c *staticAddrConn) RemoteAddr() net.Addr { return c.addr }
