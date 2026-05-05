// Package agent implements the cocoon-agent server side: a vsock-listening
// daemon that runs commands on behalf of host-side clients (vk-cocoon,
// cocoon CLI, or anyone with vsock access). The wire protocol and the
// process runner live here so they can be tested without spinning up vsock.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Message types exchanged on the framed wire. The protocol is line-delimited
// JSON: one Message per line, both directions.
//
// Server frames are terminal in two cases: MsgExit on a clean run (with
// exit_code), MsgError on a setup or wait failure (no MsgExit will follow).
// Clients must treat both as session-closed.
const (
	MsgExec       = "exec"
	MsgStdin      = "stdin"
	MsgStdinClose = "stdin_close"

	MsgStarted = "started"
	MsgStdout  = "stdout"
	MsgStderr  = "stderr"
	MsgExit    = "exit"
	MsgError   = "error"

	// frameInitBuf is the scanner's starting buffer; tuned for the
	// common 32 KiB stdout/stderr chunk plus base64 + JSON overhead.
	frameInitBuf = 64 * 1024
	// frameMaxBuf caps a single frame at 8 MiB. Generous for protocol
	// growth (env maps, longer argv) but bounded so a malformed peer
	// can't OOM the agent with one giant line.
	frameMaxBuf = 8 * 1024 * 1024
)

// Message is the union of all client and server frames. Only the fields
// relevant to Type are populated; the rest are omitempty so the wire stays
// small. Data carries base64-encoded bytes for stdin/stdout/stderr; argv
// lives in Argv; ExitCode is the child's exit status; Message is the
// human-readable error string for MsgError.
type Message struct {
	Type     string            `json:"type"`
	Argv     []string          `json:"argv,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	PID      int               `json:"pid,omitempty"`
	ExitCode int               `json:"exit_code,omitempty"`
	Message  string            `json:"message,omitempty"`
}

// Decoder reads framed Messages off a stream.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, frameInitBuf), frameMaxBuf)
	return &Decoder{scanner: s}
}

// Decode reads the next Message. Returns io.EOF cleanly at end of stream.
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

// Encoder writes framed Messages to a stream. Each Encode call emits one
// JSON object plus a trailing newline. Encoder is not goroutine-safe; the
// caller must serialize writes with an external mutex when multiple
// goroutines write (typical pattern: framedWriter or sendLocked).
type Encoder struct {
	w io.Writer
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode writes m followed by a newline.
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

// framedWriter adapts an io.Writer surface (so it can be assigned to
// exec.Cmd.Stdout/Stderr and driven by cmd.Wait) onto framed messages.
// A persistent encode failure (host gone) is captured into lastErr and
// also fires the supplied cancel — which the runner uses to terminate
// the child instead of letting it write into a black hole.
type framedWriter struct {
	msgType string
	enc     *Encoder
	mu      *sync.Mutex
	cancel  context.CancelFunc

	errMu   sync.Mutex
	lastErr error
}

// newFramedWriter binds a framedWriter to its target message type and
// the shared encoder/mutex. cancel is invoked once on the first encode
// failure so the runtime can react to a dropped peer.
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
