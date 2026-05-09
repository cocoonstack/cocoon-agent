//go:build windows

// Windows transport for cocoon-agent: AF_VSOCK over the viosock Winsock
// provider that ships with virtio-win >= 0.1.285. The address family
// number (40) and sockaddr_vm layout match Linux's AF_VSOCK 1:1, so the
// wire-level protocol cocoon-agent speaks is identical on both guests.
//
// We bypass golang.org/x/sys/windows.Bind (its Sockaddr interface has an
// unexported method, so we can't add AF_VSOCK from outside the package)
// and call the ws2_32.dll procs directly with a sockaddr_vm pointer.

package cmd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// afVsock matches Linux AF_VSOCK so the wire protocol is identical
	// across guests; viosock registers the same number on Windows.
	afVsock = 40

	// host-only filter: only host-originated peers may drive the agent.
	vmAddrCidHost = 2
	vmAddrCidAny  = 0xFFFFFFFF

	sockaddrVMSize = int32(unsafe.Sizeof(sockaddrVM{}))
	socketError    = ^uintptr(0) // winsock SOCKET_ERROR
)

var (
	modws2_32      = windows.NewLazySystemDLL("ws2_32.dll")
	procBind       = modws2_32.NewProc("bind")
	procListen     = modws2_32.NewProc("listen")
	procAccept     = modws2_32.NewProc("accept")
	procConnect    = modws2_32.NewProc("connect")
	procRecv       = modws2_32.NewProc("recv")
	procSend       = modws2_32.NewProc("send")
	procWSAStartup = modws2_32.NewProc("WSAStartup")

	// LazyProc.Call returns GetLastError as its third value, captured by
	// the Go runtime on the same OS thread before the goroutine can be
	// rescheduled — so we never need a separate WSAGetLastError dance.
	wsaInit = sync.OnceValue(func() error {
		var d windows.WSAData
		// 0x0202 = MAKEWORD(2,2); WSAStartup returns 0 on success.
		ret, _, _ := procWSAStartup.Call(0x0202, uintptr(unsafe.Pointer(&d))) //nolint:gosec // WSAStartup output param requires unsafe.Pointer
		if ret != 0 {
			return fmt.Errorf("wsastartup: %d", ret)
		}
		return nil
	})

	errDeadlineUnsupported = errors.New("vsock: deadline unsupported on windows")

	_ net.Listener = (*vsockListener)(nil)
	_ net.Conn     = (*vsockConn)(nil)
	_ net.Addr     = (*vsockAddr)(nil)
)

// sockaddrVM matches `struct sockaddr_vm` exactly (16 bytes). Field order
// and padding are load-bearing — the kernel/driver reads these offsets.
type sockaddrVM struct {
	Family    uint16
	Reserved1 uint16
	Port      uint32
	CID       uint32
	Flags     uint8
	_         [3]uint8
}

func listenVsock(port uint32) (net.Listener, error) {
	if err := wsaInit(); err != nil {
		return nil, err
	}
	h, err := windows.Socket(afVsock, windows.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	sa := sockaddrVM{Family: afVsock, Port: port, CID: vmAddrCidAny}
	r, _, callErr := procBind.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(sockaddrVMSize)) //nolint:gosec // winsock bind requires raw pointer
	if r == socketError {
		_ = windows.Closesocket(h)
		return nil, fmt.Errorf("vsock bind port=%d: %w", port, callErr)
	}
	r, _, callErr = procListen.Call(uintptr(h), 32)
	if r == socketError {
		_ = windows.Closesocket(h)
		return nil, fmt.Errorf("vsock listen: %w", callErr)
	}
	return &vsockListener{h: h, port: port}, nil
}

func dialVsock(cid, port uint32) (io.ReadWriteCloser, error) {
	if err := wsaInit(); err != nil {
		return nil, err
	}
	h, err := windows.Socket(afVsock, windows.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	sa := sockaddrVM{Family: afVsock, Port: port, CID: cid}
	r, _, callErr := procConnect.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(sockaddrVMSize)) //nolint:gosec // winsock connect requires raw pointer
	if r == socketError {
		_ = windows.Closesocket(h)
		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, callErr)
	}
	return &vsockConn{
		h:        h,
		peerCID:  cid,
		peerPort: port,
	}, nil
}

type vsockListener struct {
	h      windows.Handle
	port   uint32
	closed atomic.Bool
}

// Accept blocks until a host-originated connection arrives. Connections from
// non-host CIDs (guest-local processes via VMADDR_CID_LOCAL etc.) are dropped.
func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		var sa sockaddrVM
		salen := sockaddrVMSize
		r, _, callErr := procAccept.Call(uintptr(l.h), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&salen))) //nolint:gosec // winsock accept requires raw pointers
		if r == socketError {
			if l.closed.Load() {
				return nil, net.ErrClosed
			}
			return nil, fmt.Errorf("vsock accept: %w", callErr)
		}
		conn := &vsockConn{
			h:         windows.Handle(r),
			localPort: l.port,
			peerCID:   sa.CID,
			peerPort:  sa.Port,
		}
		if sa.CID != vmAddrCidHost {
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

func (l *vsockListener) Close() error {
	if l.closed.Swap(true) {
		return nil
	}
	return windows.Closesocket(l.h)
}

func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{cid: vmAddrCidAny, port: l.port}
}

type vsockConn struct {
	h         windows.Handle
	localPort uint32
	peerCID   uint32
	peerPort  uint32
	closed    atomic.Bool
}

func (c *vsockConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r, _, callErr := procRecv.Call(uintptr(c.h), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)), 0) //nolint:gosec // winsock recv requires raw buffer pointer
	if r == socketError {
		if c.closed.Load() {
			return 0, net.ErrClosed
		}
		return 0, fmt.Errorf("vsock recv: %w", callErr)
	}
	if r == 0 {
		return 0, io.EOF
	}
	return int(r), nil
}

func (c *vsockConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for total < len(p) {
		r, _, callErr := procSend.Call(uintptr(c.h), uintptr(unsafe.Pointer(&p[total])), uintptr(len(p)-total), 0) //nolint:gosec // winsock send requires raw buffer pointer
		if r == socketError {
			if c.closed.Load() {
				return total, net.ErrClosed
			}
			return total, fmt.Errorf("vsock send: %w", callErr)
		}
		if r == 0 {
			// Guard against the undocumented send() == 0 case so we don't spin.
			return total, io.ErrShortWrite
		}
		total += int(r)
	}
	return total, nil
}

func (c *vsockConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return windows.Closesocket(c.h)
}

func (c *vsockConn) LocalAddr() net.Addr {
	return &vsockAddr{cid: vmAddrCidHost, port: c.localPort}
}

func (c *vsockConn) RemoteAddr() net.Addr {
	return &vsockAddr{cid: c.peerCID, port: c.peerPort}
}

// Deadlines are unsupported: agent shutdown uses Closesocket to unblock
// recv/send, which is sufficient. Switch to overlapped I/O if real
// per-call timeouts ever become a requirement.
func (c *vsockConn) SetDeadline(_ time.Time) error      { return errDeadlineUnsupported }
func (c *vsockConn) SetReadDeadline(_ time.Time) error  { return errDeadlineUnsupported }
func (c *vsockConn) SetWriteDeadline(_ time.Time) error { return errDeadlineUnsupported }

type vsockAddr struct {
	cid, port uint32
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string  { return fmt.Sprintf("vsock://%d:%d", a.cid, a.port) }
