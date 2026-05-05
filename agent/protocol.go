// Package agent implements the cocoon-agent server side: a vsock-listening
// daemon that runs commands on behalf of host-side clients (vk-cocoon,
// cocoon CLI, or anyone with vsock access). The wire protocol and the
// process runner live here so they can be tested without spinning up vsock.
package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Message types exchanged on the framed wire. The protocol is line-delimited
// JSON: one Message per line, both directions, until exit/error closes the
// stream. Binary stdin/stdout/stderr payloads are base64-encoded into the
// Data field so we don't have to invent a separate framing for raw bytes.
const (
	MsgExec       = "exec"
	MsgStdin      = "stdin"
	MsgStdinClose = "stdin_close"

	MsgStarted = "started"
	MsgStdout  = "stdout"
	MsgStderr  = "stderr"
	MsgExit    = "exit"
	MsgError   = "error"
)

// Message is the union of all client and server frames. Only the fields
// relevant to Type are populated; the rest are omitempty so the wire stays
// small. Data carries base64-encoded bytes for stdin/stdout/stderr; cobra-
// style argv lives in Argv; ExitCode is the child's exit status; Message is
// the human-readable error string for MsgError.
type Message struct {
	Type     string            `json:"type"`
	Argv     []string          `json:"argv,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	PID      int               `json:"pid,omitempty"`
	ExitCode int               `json:"exit_code,omitempty"`
	Message  string            `json:"message,omitempty"`
}

// Decoder reads framed Messages off a stream. It wraps bufio.Scanner with
// a larger buffer because stdout chunks (default 32 KiB on the writer side)
// would overflow the 64 KiB scanner default once they're base64-expanded.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder returns a Decoder reading from r. The internal buffer caps
// at 8 MiB which comfortably fits a 32 KiB stdout chunk + base64 + JSON
// overhead with room for protocol growth.
func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) //nolint:mnd
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
// caller must serialize writes (typical pattern: a single writer goroutine
// fed by a buffered channel from many producers).
type Encoder struct {
	w io.Writer
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode writes m followed by a newline. Returns the underlying write
// error so callers can surface short writes / closed pipes.
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
