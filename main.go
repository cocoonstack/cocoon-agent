// Package main is the cocoon-agent entry point. The agent runs inside a
// Cocoon-managed VM and serves command-exec requests over virtio-vsock.
// Trust model: vsock is host-local, so the host UID is the auth boundary.
package main

import (
	"github.com/cocoonstack/cocoon-agent/cmd"
)

func main() {
	cmd.Execute()
}
