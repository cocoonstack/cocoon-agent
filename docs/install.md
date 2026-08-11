# Installation

## Release artifacts

Each release publishes, for linux `x86_64` and `arm64` and for windows
`x86_64`:

| Asset | Contents |
|---|---|
| `cocoon-agent_<version>_Linux_<arch>.tar.gz` | stripped `cocoon-agent` |
| `cocoon-agent_<version>_Windows_x86_64.zip` | `cocoon-agent.exe` plus `install-cocoon-agent.ps1` |
| `cocoon-agent_<version>_Linux_<arch>_debug.tar.gz` | unstripped `cocoon-agent.dbg`, for symbol-backed debugging on that node |

Unversioned aliases (`cocoon-agent_Linux_x86_64.tar.gz`,
`cocoon-agent_Windows_x86_64.zip`, …) resolve under
`releases/latest/download/`, so image builds can pin "latest" without
templating a version.

## Linux guest

cocoon-agent is baked into Cocoon-managed images alongside the existing OCI bundles in [cocoon/os-image](https://github.com/cocoonstack/cocoon/tree/master/os-image):

```dockerfile
COPY cocoon-agent /usr/local/bin/cocoon-agent
COPY cocoon-agent.service /etc/systemd/system/cocoon-agent.service
RUN systemctl enable cocoon-agent.service
```

The systemd unit is in [`packaging/cocoon-agent.service`](../packaging/cocoon-agent.service).

## Windows guest

Windows support requires the `viosock` driver shipped with **virtio-win >= 0.1.285** (Microsoft-attestation signed for Windows 10+). Cocoon's stock Windows images include it. The agent uses the same vsock port (1024) and wire protocol as the Linux build — host-side callers don't need to know which guest OS they're talking to.

```powershell
# Run elevated. Idempotent.
.\install-cocoon-agent.ps1
```

The script copies `cocoon-agent.exe` to `C:\Program Files\Cocoon\`, registers a Windows service (`cocoon-agent`, `LocalSystem`, auto-start, restart-on-crash), and starts it. See [`packaging/install-cocoon-agent.ps1`](../packaging/install-cocoon-agent.ps1).

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
  `powershell.exe` REPL prompt are not visible — wait for the planned PTY mode.

Pipe mode is sufficient for automation and scripted tasks; interactive
shells need PTY mode.
