// Package main is the cocoon-agent entry point.
//
// cocoon-agent runs inside a Cocoon-managed VM and exposes a host-to-guest
// command-execution channel over virtio-vsock, replacing SSH for control
// plane operations like kubectl exec. The agent assumes the host (vk-cocoon
// or any other vsock client running as root on the same node) is trusted —
// vsock cannot be reached from the network, so the host UID is the auth
// boundary.
package main

import (
	"github.com/cocoonstack/cocoon-agent/cmd"
)

func main() {
	cmd.Execute()
}
