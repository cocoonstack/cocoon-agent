package client_test

import (
	"net"
	"strings"
	"testing"

	"github.com/cocoonstack/cocoon-agent/agent"
	"github.com/cocoonstack/cocoon-agent/client"
)

func TestReseedSuccess(t *testing.T) {
	t.Parallel()
	got, err := reseedAgainstReply(t, []byte("host-entropy"), true, agent.Message{Type: agent.MsgExit, ExitCode: 0})
	if err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if got.Type != agent.MsgReseed || string(got.Data) != "host-entropy" || !got.RegenMachineID {
		t.Errorf("agent saw frame %+v, want reseed with entropy and regen_machine_id", got)
	}
}

func TestReseedErrorFrame(t *testing.T) {
	t.Parallel()
	_, err := reseedAgainstReply(t, nil, false, agent.Message{Type: agent.MsgError, Message: "boom"})
	if err == nil {
		t.Fatal("expected error for MsgError reply")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected wrapped agent error, got %v", err)
	}
}

func TestReseedNonZeroExit(t *testing.T) {
	t.Parallel()
	_, err := reseedAgainstReply(t, []byte("e"), false, agent.Message{Type: agent.MsgExit, ExitCode: 1})
	if err == nil {
		t.Fatal("expected error for non-zero exit code")
	}
}

// reseedAgainstReply runs client.Reseed against a fake agent answering with reply; returns the frame the agent saw.
func reseedAgainstReply(t *testing.T, entropy []byte, regenMachineID bool, reply agent.Message) (agent.Message, error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	var got agent.Message
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		frame, err := agent.NewDecoder(serverConn).Decode()
		if err != nil {
			return
		}
		got = frame
		_ = agent.NewEncoder(serverConn).Encode(reply)
	}()

	err := client.Reseed(t.Context(), clientConn, entropy, regenMachineID)
	<-serverDone
	return got, err
}
