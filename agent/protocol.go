// Package agent implements the cocoon-agent server: a vsock-listening
// daemon that runs commands for host-side clients. Wire protocol + runner
// live here so they're testable without vsock.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
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

	// frameMaxBuf caps a single frame so a malformed peer can't OOM us.
	frameInitBuf = 64 * 1024
	frameMaxBuf  = 8 * 1024 * 1024
)

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

type Decoder struct {
	scanner *bufio.Scanner
}

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

// Encoder is not goroutine-safe; serialize via an external mutex when
// multiple writers share one (see framedWriter / sendLocked).
type Encoder struct {
	w io.Writer
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

func (e *Encoder) Encode(m Message) error {
	buf, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	buf = append(buf, '\n')
	if _, err := e.w.Write(buf); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// framedWriter adapts io.Writer onto framed messages so it can drive
// exec.Cmd.Stdout/Stderr. First encode failure is captured and fires
// cancel so the runner can kill the child.
type framedWriter struct {
	msgType string
	enc     *Encoder
	mu      *sync.Mutex
	cancel  context.CancelFunc

	errMu   sync.Mutex
	lastErr error
}

func newFramedWriter(msgType string, enc *Encoder, mu *sync.Mutex, cancel context.CancelFunc) *framedWriter {
	return &framedWriter{msgType: msgType, enc: enc, mu: mu, cancel: cancel}
}

func (w *framedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	payload := make([]byte, len(p))
	copy(payload, p)
	if err := sendLocked(w.enc, w.mu, Message{Type: w.msgType, Data: payload}); err != nil {
		w.errMu.Lock()
		if w.lastErr == nil {
			w.lastErr = err
			if w.cancel != nil {
				w.cancel()
			}
		}
		w.errMu.Unlock()
		return 0, err
	}
	return len(p), nil
}

func (w *framedWriter) err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.lastErr
}
