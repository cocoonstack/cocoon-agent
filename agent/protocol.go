// Package agent implements the cocoon-agent server: a vsock-listening
// daemon that runs commands for host-side clients. Wire protocol + runner
// live here so they're testable without vsock.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Frame types. MsgExit and MsgError are terminal — clients must treat
// both as session-closed; MsgError is never followed by MsgExit.
const (
	MsgExec       = "exec"
	MsgStdin      = "stdin"
	MsgStdinClose = "stdin_close"

	MsgStarted = "started"
	MsgStdout  = "stdout"
	MsgStderr  = "stderr"
	MsgExit    = "exit"
	MsgError   = "error"

	frameInitBuf = 64 * 1024
	// frameMaxBuf caps a single frame so a malformed peer can't OOM us.
	frameMaxBuf = 8 * 1024 * 1024
)

var errTerminalFrameSent = errors.New("terminal frame already sent")

// Message is the union of all frames. Only fields relevant to Type are populated.
type Message struct {
	Type     string            `json:"type"`
	Argv     []string          `json:"argv,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	PID      int               `json:"pid,omitempty"`
	ExitCode int               `json:"exit_code,omitempty"`
	Message  string            `json:"message,omitempty"`
}

// Decoder reads line-delimited JSON frames off a reader.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder wraps r with a frame-bounded scanner.
func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, frameInitBuf), frameMaxBuf)
	return &Decoder{scanner: s}
}

// Decode returns io.EOF cleanly at end of stream.
func (d *Decoder) Decode() (Message, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return Message{}, fmt.Errorf("read frame: %w", err)
		}
		return Message{}, io.EOF
	}
	var m Message
	if err := json.Unmarshal(d.scanner.Bytes(), &m); err != nil {
		return Message{}, fmt.Errorf("decode frame: %w", err)
	}
	return m, nil
}

// Encoder serializes Encode calls so multiple writers (stdout/stderr pumps,
// stdin protocol-error path) can share one without an external mutex.
type Encoder struct {
	mu       sync.Mutex
	w        io.Writer
	terminal bool
}

// NewEncoder returns an Encoder writing newline-delimited JSON frames to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode marshals m as a single newline-terminated JSON frame.
func (e *Encoder) Encode(m Message) error {
	buf, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	buf = append(buf, '\n')
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.terminal {
		return errTerminalFrameSent
	}
	if _, err := e.w.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	if isTerminalFrame(m.Type) {
		e.terminal = true
	}
	return nil
}

// SendErrorf encodes a MsgError frame with a formatted message body.
func (e *Encoder) SendErrorf(format string, args ...any) error {
	return e.Encode(Message{Type: MsgError, Message: fmt.Sprintf(format, args...)})
}

func isTerminalFrame(msgType string) bool {
	return msgType == MsgExit || msgType == MsgError
}

// framedWriter adapts io.Writer onto framed messages for exec.Cmd's
// stdout/stderr; the first encode failure fires cancel to kill the child.
type framedWriter struct {
	msgType string
	enc     *Encoder
	cancel  context.CancelFunc
	lastErr atomic.Pointer[error]
}

func newFramedWriter(msgType string, enc *Encoder, cancel context.CancelFunc) *framedWriter {
	return &framedWriter{msgType: msgType, enc: enc, cancel: cancel}
}

func (w *framedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// p is safe to alias: Encode → json.Marshal copies Data before returning;
	// exec.Cmd's I/O pump doesn't reuse p until this Write returns.
	err := w.enc.Encode(Message{Type: w.msgType, Data: p})
	if err == nil {
		return len(p), nil
	}
	// Skip errTerminalFrameSent — it's a session-ended race signal, not
	// a write failure; let runExec's ctx-cause check handle cleanup.
	if errors.Is(err, errTerminalFrameSent) {
		return 0, err
	}
	errCopy := err
	if w.lastErr.CompareAndSwap(nil, &errCopy) && w.cancel != nil {
		w.cancel()
	}
	return 0, err
}

func (w *framedWriter) err() error {
	if e := w.lastErr.Load(); e != nil {
		return *e
	}
	return nil
}
