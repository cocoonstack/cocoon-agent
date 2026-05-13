package agent

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  Message
	}{
		{
			name: "exec with argv and env",
			msg: Message{
				Type: MsgExec,
				Argv: []string{"sh", "-c", "echo hello"},
				Env:  map[string]string{"FOO": "bar"},
			},
		},
		{
			name: "stdout with binary payload",
			msg: Message{
				Type: MsgStdout,
				Data: []byte{0x00, 0x01, 0xff, 0xfe, '\n', 0x00},
			},
		},
		{
			name: "exit with non-zero",
			msg:  Message{Type: MsgExit, ExitCode: 42},
		},
		{
			name: "error",
			msg:  Message{Type: MsgError, Message: "kaboom"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := NewEncoder(&buf)
			if err := enc.Encode(tc.msg); err != nil {
				t.Fatalf("encode: %v", err)
			}
			dec := NewDecoder(&buf)
			got, err := dec.Decode()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got, tc.msg) {
				t.Errorf("round-trip mismatch:\n  want: %#v\n  got:  %#v", tc.msg, got)
			}
		})
	}
}

func TestDecodeMultipleFrames(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	frames := []Message{
		{Type: MsgStarted, PID: 1234},
		{Type: MsgStdout, Data: []byte("hello")},
		{Type: MsgStdout, Data: []byte("world")},
		{Type: MsgExit, ExitCode: 0},
	}
	for _, f := range frames {
		if err := enc.Encode(f); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}

	dec := NewDecoder(&buf)
	for i, want := range frames {
		got, err := dec.Decode()
		if err != nil {
			t.Fatalf("frame %d: decode: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("frame %d: got %#v want %#v", i, got, want)
		}
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF after last frame, got %v", err)
	}
}

func TestEncodeRejectsFramesAfterTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(Message{Type: MsgError, Message: "boom"}); err != nil {
		t.Fatalf("encode terminal error: %v", err)
	}
	if err := enc.Encode(Message{Type: MsgExit, ExitCode: 1}); !errors.Is(err, errTerminalFrameSent) {
		t.Fatalf("encode after terminal = %v, want %v", err, errTerminalFrameSent)
	}

	dec := NewDecoder(&buf)
	frame, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode terminal frame: %v", err)
	}
	if frame.Type != MsgError {
		t.Fatalf("terminal frame type = %q, want %q", frame.Type, MsgError)
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after single terminal frame, got %v", err)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	dec := NewDecoder(strings.NewReader("not-json\n"))
	_, err := dec.Decode()
	if err == nil {
		t.Fatal("expected decode error on malformed JSON")
	}
}

func TestDecodeHandlesLargeFrame(t *testing.T) {
	t.Parallel()

	// A 1 MiB stdout chunk should round-trip without exceeding the 8 MiB
	// scanner cap.
	payload := bytes.Repeat([]byte{'A'}, 1024*1024)
	msg := Message{Type: MsgStdout, Data: payload}

	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(msg); err != nil {
		t.Fatalf("encode large: %v", err)
	}
	got, err := NewDecoder(&buf).Decode()
	if err != nil {
		t.Fatalf("decode large: %v", err)
	}
	if !bytes.Equal(got.Data, payload) {
		t.Errorf("payload mismatch: got %d bytes, want %d", len(got.Data), len(payload))
	}
}

// TestFramedWriterAfterTerminal exercises the errTerminalFrameSent skip path
// in framedWriter.Write: once the encoder has emitted a terminal frame, the
// child's I/O pump may still call Write — these post-terminal writes must
// surface errTerminalFrameSent and must NOT poison lastErr or trip cancel,
// otherwise the post-Wait err()-join would mask the legitimate exit path.
func TestFramedWriterAfterTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(Message{Type: MsgError, Message: "kaboom"}); err != nil {
		t.Fatalf("encode terminal: %v", err)
	}

	var cancelCalled bool
	cancel := func() { cancelCalled = true }
	w := newFramedWriter(MsgStdout, enc, cancel)

	n, err := w.Write([]byte("late stdout chunk"))
	if !errors.Is(err, errTerminalFrameSent) {
		t.Fatalf("post-terminal Write err = %v, want %v", err, errTerminalFrameSent)
	}
	if n != 0 {
		t.Errorf("post-terminal Write n = %d, want 0", n)
	}
	if cancelCalled {
		t.Error("post-terminal Write must not call cancel")
	}
	if got := w.err(); got != nil {
		t.Errorf("post-terminal Write must not poison lastErr, got %v", got)
	}
}
