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
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// afVsock is the address family registered by the viosock Winsock
	// provider. Same numeric value as Linux AF_VSOCK so cocoon's host-side
	// hybrid-vsock framing works unchanged.
	afVsock = 40

	// vmAddrCidHost / vmAddrCidAny mirror the Linux constants. We only
	// accept connections originating from the host (CID=2) — guest-local
	// callers shouldn't be able to drive the agent.
	vmAddrCidHost = 2
	vmAddrCidAny  = 0xFFFFFFFF

	// sockaddrVMSize is the on-wire byte size of struct sockaddr_vm (16).
	sockaddrVMSize = int32(unsafe.Sizeof(sockaddrVM{}))

	// socketError is the winsock SOCKET_ERROR sentinel (== -1 cast to unsigned).
	socketError = ^uintptr(0)
)

var (
	modws2_32           = windows.NewLazySystemDLL("ws2_32.dll")
	procBind            = modws2_32.NewProc("bind")
	procListen          = modws2_32.NewProc("listen")
	procAccept          = modws2_32.NewProc("accept")
	procConnect         = modws2_32.NewProc("connect")
	procRecv            = modws2_32.NewProc("recv")
	procSend            = modws2_32.NewProc("send")
	procWSAStartup      = modws2_32.NewProc("WSAStartup")
	procWSAGetLastError = modws2_32.NewProc("WSAGetLastError")

	wsaInitOnce sync.Once
	wsaInitErr  error

	errDeadlineUnsupported = errors.New("vsock: deadline unsupported on windows")
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

// wsaLastError reads the per-thread Winsock error and returns it as a
// syscall.Errno. Wrapped instead of x/sys/windows.WSAGetLastError to stay
// portable across x/sys/windows API revisions.
func wsaLastError() error {
	r, _, _ := procWSAGetLastError.Call()
	if r == 0 {
		return nil
	}
	return syscall.Errno(r)
}

func wsaInit() error {
	wsaInitOnce.Do(func() {
		var d windows.WSAData
		// 0x0202 = MAKEWORD(2,2); ws2_32.WSAStartup returns 0 on success.
		ret, _, _ := procWSAStartup.Call(0x0202, uintptr(unsafe.Pointer(&d))) //nolint:gosec // WSAStartup output param requires unsafe.Pointer
		if ret != 0 {
			wsaInitErr = fmt.Errorf("WSAStartup: %d", ret)
		}
	})
	return wsaInitErr
}

func listenVsock(port uint32) (net.Listener, error) {
	if err := wsaInit(); err != nil {
		return nil, err
	}
	h, err := windows.Socket(afVsock, windows.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w (is the viosock driver loaded? requires virtio-win >= 0.1.285)", err)
	}
	sa := sockaddrVM{Family: afVsock, Port: port, CID: vmAddrCidAny}
	r, _, _ := procBind.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(sockaddrVMSize)) //nolint:gosec // winsock bind requires raw pointer
	if r == socketError {
		_ = windows.Closesocket(h)
		return nil, fmt.Errorf("vsock bind port=%d: %w", port, wsaLastError())
	}
	r, _, _ = procListen.Call(uintptr(h), 32)
	if r == socketError {
		_ = windows.Closesocket(h)
		return nil, fmt.Errorf("vsock listen: %w", wsaLastError())
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
	r, _, _ := procConnect.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(sockaddrVMSize)) //nolint:gosec // winsock connect requires raw pointer
	if r == socketError {
		_ = windows.Closesocket(h)
		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, wsaLastError())
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
		r, _, _ := procAccept.Call(uintptr(l.h), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&salen))) //nolint:gosec // winsock accept requires raw pointers
		if r == socketError {
			if l.closed.Load() {
				return nil, net.ErrClosed
			}
			return nil, fmt.Errorf("vsock accept: %w", wsaLastError())
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
	r, _, _ := procRecv.Call(uintptr(c.h), uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)), 0) //nolint:gosec // winsock recv requires raw buffer pointer
	if r == socketError {
		if c.closed.Load() {
			return 0, net.ErrClosed
		}
		return 0, fmt.Errorf("vsock recv: %w", wsaLastError())
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
		r, _, _ := procSend.Call(uintptr(c.h), uintptr(unsafe.Pointer(&p[total])), uintptr(len(p)-total), 0) //nolint:gosec // winsock send requires raw buffer pointer
		if r == socketError {
			if c.closed.Load() {
				return total, net.ErrClosed
			}
			return total, fmt.Errorf("vsock send: %w", wsaLastError())
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

// SetDeadline / SetReadDeadline / SetWriteDeadline are best-effort no-ops:
// blocking winsock recv/send is interrupted by Closesocket, which is what the
// agent's shutdown path uses. If we ever need real per-call timeouts we can
// switch to overlapped I/O via WSARecv/WSASend.
func (c *vsockConn) SetDeadline(_ time.Time) error      { return errDeadlineUnsupported }
func (c *vsockConn) SetReadDeadline(_ time.Time) error  { return errDeadlineUnsupported }
func (c *vsockConn) SetWriteDeadline(_ time.Time) error { return errDeadlineUnsupported }

type vsockAddr struct {
	cid, port uint32
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string  { return fmt.Sprintf("vsock://%d:%d", a.cid, a.port) }
