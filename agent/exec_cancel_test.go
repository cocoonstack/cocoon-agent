package agent

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestExecDisconnectCancelsQuietChild(t *testing.T) {
	tests := []struct {
		name       string
		closeStdin bool
	}{
		{name: "stdin open"},
		{name: "stdin closed", closeStdin: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			serverConn, peer := net.Pipe()
			done := make(chan struct{})
			srv := NewServer(nil)
			go func() {
				defer close(done)
				srv.handleConn(ctx, serverConn)
			}()
			t.Cleanup(func() {
				cancel()
				_ = peer.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("handler did not stop after server cancellation")
				}
			})
			if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatalf("set deadline: %v", err)
			}
			enc := NewEncoder(peer)
			if err := enc.Encode(Message{
				Type: MsgExec,
				Argv: []string{os.Args[0], "-test.run=^TestExecQuietChild$"},
				Env:  map[string]string{"COCOON_AGENT_TEST_QUIET_CHILD": "1"},
			}); err != nil {
				t.Fatalf("send exec: %v", err)
			}
			if tt.closeStdin {
				if err := enc.Encode(Message{Type: MsgStdinClose}); err != nil {
					t.Fatalf("close stdin: %v", err)
				}
			}
			dec := NewDecoder(peer)
			var sawStarted, sawReady bool
			for range 2 {
				frame, err := dec.Decode()
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				switch {
				case frame.Type == MsgStarted:
					sawStarted = true
				case frame.Type == MsgStdout && string(frame.Data) == "ready":
					sawReady = true
				default:
					t.Fatalf("unexpected frame %+v", frame)
				}
			}
			if !sawStarted || !sawReady {
				t.Fatalf("started=%v ready=%v: both frames must arrive before the disconnect", sawStarted, sawReady)
			}
			if err := peer.Close(); err != nil {
				t.Fatalf("disconnect: %v", err)
			}
			select {
			case <-done:
				if ctx.Err() != nil {
					t.Errorf("server context ended: %v", ctx.Err())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("disconnect did not reap the child and release the handler")
			}
		})
	}
}

func TestExecQuietChild(t *testing.T) {
	if os.Getenv("COCOON_AGENT_TEST_QUIET_CHILD") != "1" {
		return
	}
	if _, err := os.Stdout.WriteString("ready"); err != nil {
		t.Fatalf("signal readiness: %v", err)
	}
	time.Sleep(time.Minute)
}
