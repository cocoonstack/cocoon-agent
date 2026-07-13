# Architecture

## Why

Cocoon's host components (vk-cocoon, cocoon CLI) and the VMs they manage always sit on the same node. SSH inside the guest carries a lot of weight for that scenario:

- requires sshd installed and running
- requires the guest network stack to be up + an IP to be reachable
- requires per-VM credentials to be provisioned and rotated
- comes with the full SSH protocol surface (handshake, key exchange, host key trust)

vsock is host↔guest only, has no IP layer, and the kernel `vhost_vsock` module is the auth boundary — anything that can connect from the host is already privileged on that host. cocoon-agent leverages this for a small focused daemon that gives kubectl-exec semantics (stdin / stdout / stderr / exit code) with none of those dependencies.

## Status

v0.1.x — Linux + Windows guests supported. PTY mode planned; see [Roadmap](#roadmap).

## Design

```
host (cocoon node)                                  guest VM
+------------------+                            +------------------------+
| vk-cocoon        |                            | systemd                |
|                  |                            |   |                    |
| Provider.Run-    |  vsock://<cid>:<port>      |   v                    |
| InContainer ---> |--------------------------->| cocoon-agent serve     |
|                  |       (kubectl exec)       |   |                    |
| (eventually via  |                            |   v                    |
|  cocoon vm exec) |                            | exec.Command(argv)     |
+------------------+                            +------------------------+
```

The wire protocol is line-delimited JSON, one frame per line, both directions. The first frame is `MsgExec` carrying argv; subsequent frames are stdin chunks (`MsgStdin` / `MsgStdinClose`) from the client and stdout/stderr chunks plus the final exit code (`MsgStdout` / `MsgStderr` / `MsgExit`) from the agent.

See [`agent/protocol.go`](../agent/protocol.go) for the complete schema.

## Roadmap

| Milestone | Status | Scope |
|---|---|---|
| MVP | v0.1.0 | exec, stdin streaming, stdout/stderr, exit code; vsock listener; cobra CLI (Linux) |
| Windows guests | v0.1.1+ (current) | AF_VSOCK via viosock; SCM-registered Windows service; PowerShell installer |
| PTY mode | planned | `tty: true`, window resize, signal forwarding — interactive shells, `vim`/`top` |
| Streaming host adapter | planned | subprocess-friendly hand-off into vk-cocoon `RunInContainer` |
