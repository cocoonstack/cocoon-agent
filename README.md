# cocoon-agent

In-VM exec agent for [Cocoon](https://github.com/cocoonstack/cocoon)-managed VMs. Listens on virtio-vsock and runs commands on behalf of host-side callers (vk-cocoon, the cocoon CLI, anything else with vsock access on the same node), replacing SSH for control-plane operations like `kubectl exec`.

## Why

Cocoon's host components (vk-cocoon, cocoon CLI) and the VMs they manage always sit on the same node. SSH inside the guest carries a lot of weight for that scenario:

- requires sshd installed and running
- requires the guest network stack to be up + an IP to be reachable
- requires per-VM credentials to be provisioned and rotated
- comes with the full SSH protocol surface (handshake, key exchange, host key trust)

vsock is host↔guest only, has no IP layer, and the kernel `vhost_vsock` module is the auth boundary — anything that can connect from the host is already privileged on that host. cocoon-agent leverages this for a small focused daemon that gives kubectl-exec semantics (stdin / stdout / stderr / exit code) with none of those dependencies.

## Status

v0.1 — first working version. See [Roadmap](#roadmap) for what's coming.

## Architecture

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

See [`agent/protocol.go`](agent/protocol.go) for the complete schema.

## Build

```bash
make build                                # local build
GOOS=linux GOARCH=amd64 make build        # linux guest, x86_64
GOOS=linux GOARCH=arm64 make build        # linux guest, arm64
GOOS=windows GOARCH=amd64 make build      # windows guest → cocoon-agent-windows-amd64.exe
```

## Install in a Linux guest

cocoon-agent is baked into Cocoon-managed images alongside the existing OCI bundles in [cocoon/os-image](https://github.com/cocoonstack/cocoon/tree/master/os-image):

```dockerfile
COPY cocoon-agent /usr/local/bin/cocoon-agent
COPY cocoon-agent.service /etc/systemd/system/cocoon-agent.service
RUN systemctl enable cocoon-agent.service
```

The systemd unit is in [`packaging/cocoon-agent.service`](packaging/cocoon-agent.service).

## Install in a Windows guest

Windows support requires the `viosock` driver shipped with **virtio-win >= 0.1.285** (Microsoft-attestation signed for Windows 10+). Cocoon's stock Windows images include it. The agent uses the same vsock port (1024) and wire protocol as the Linux build — host-side callers don't need to know which guest OS they're talking to.

```powershell
# Run elevated. Idempotent.
.\install-cocoon-agent.ps1
```

The script copies `cocoon-agent.exe` to `C:\Program Files\Cocoon\`, registers a Windows service (`cocoon-agent`, `LocalSystem`, auto-start, restart-on-crash), and starts it. See [`packaging/install-cocoon-agent.ps1`](packaging/install-cocoon-agent.ps1).

Verify it's running:

```powershell
Get-Service cocoon-agent           # should show Running
```

### Pipe-mode limitations on Windows

Until ConPTY support lands (planned v0.3), the agent runs child processes
with pipe stdin/stdout/stderr only. Windows console programs that bypass
stdout pipes via the Console API (`WriteConsoleW` etc.) won't have their
output captured. In practice:

- ✅ `cmd /c "<thing>"`, batch scripts, and most CLI tools that write via
  the C runtime work normally.
- ✅ `powershell.exe -Command "<X> | Out-File <path>"` works.
- ⚠️ `powershell.exe -Command "<X>"` may produce partial output for cmdlets
  that render directly to the console.
- ❌ TUI programs (`vim`, `far`, `htop`-style) and the interactive
  `powershell.exe` REPL prompt are not visible — wait for v0.3 PTY mode.

Pipe mode is sufficient for automation and scripted tasks; interactive
shells need v0.3.

## Smoke test from the host

cocoon-agent ships a `client` subcommand for vsock smoke tests without needing to plumb through cocoon CLI / vk-cocoon. Get the VM's CID from `cocoon vm inspect` and run:

```bash
cocoon-agent client --cid 3 --port 1024 -- echo "hello from guest"
echo "world" | cocoon-agent client --cid 3 --port 1024 -- cat
```

Exit codes pass through.

## Configuration

| Env var | Default | Notes |
|---|---|---|
| `AGENT_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` (projecteru2/core/log levels) |

CLI flags:

| Flag | Default | Notes |
|---|---|---|
| `serve --port` | `1024` | vsock port to bind on |
| `client --cid` | required | target VM CID (host-side) |
| `client --port` | `1024` | matches `serve --port` |

## Roadmap

| Version | Scope |
|---|---|
| v0.1 | exec, stdin streaming, stdout/stderr, exit code; vsock listener; cobra CLI (linux only) |
| v0.2 (current) | Windows guest support (AF_VSOCK via viosock); PowerShell service installer |
| v0.3 | PTY mode (`tty: true`), window resize messages, signal forwarding |
| v0.4 | Streaming `cocoon vm exec` host-side adapter (subprocess-friendly for vk-cocoon `RunInContainer`) |

## Related

- [cocoon](https://github.com/cocoonstack/cocoon) — VM engine
- [vk-cocoon](https://github.com/cocoonstack/vk-cocoon) — virtual-kubelet provider; the planned primary consumer of cocoon-agent
- [cocoon-common](https://github.com/cocoonstack/cocoon-common) — shared metadata / annotation contract

## Development

```bash
make all          # tidy + fmt + lint + test + build
make test         # go test -race -cover
make lint         # golangci-lint on linux + darwin + windows
make fmt-check    # gofumpt + goimports check
make help         # full target list
```

## License

[MIT](LICENSE)
