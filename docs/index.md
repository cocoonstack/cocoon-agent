# cocoon-agent

In-VM exec agent for [Cocoon](https://github.com/cocoonstack/cocoon)-managed
VMs. Listens on virtio-vsock and runs commands on behalf of host-side callers
(vk-cocoon, the cocoon CLI, anything else with vsock access on the same
node), replacing SSH for control-plane operations like `kubectl exec`.

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

## Guides

- [Architecture](architecture.md) — why cocoon-agent exists, current
  status, the wire protocol, and the roadmap
- [Installation](install.md) — baking the Linux binary into an image,
  the Windows service installer, and pipe-mode limitations
- [Usage](usage.md) — smoke-testing an agent from the host with the
  `client` subcommand
- [Configuration](configuration.md) — environment variables and CLI flags
- [Build](build.md) — cross-compiling for Linux and Windows guests
