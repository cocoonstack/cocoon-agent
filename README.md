# cocoon-agent

In-VM exec agent for [Cocoon](https://github.com/cocoonstack/cocoon)-managed VMs. Listens on virtio-vsock and runs commands on behalf of host-side callers (vk-cocoon, the cocoon CLI, anything else with vsock access on the same node), replacing SSH for control-plane operations like `kubectl exec`.

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

v0.1.x — Linux + Windows guests supported. PTY mode planned; see
[Roadmap](docs/architecture.md#roadmap).

## Quick start

```bash
cocoon-agent client --cid 3 --port 1024 -- echo "hello from guest"
```

Full steps in [Usage](docs/usage.md).

## Documentation

- [Architecture](docs/architecture.md) — why cocoon-agent exists, current status, the wire protocol, and the roadmap
- [Installation](docs/install.md) — baking the Linux binary into an image, the Windows service installer, and pipe-mode limitations
- [Usage](docs/usage.md) — smoke-testing an agent from the host with the `client` subcommand
- [Configuration](docs/configuration.md) — environment variables and CLI flags
- [Build](docs/build.md) — cross-compiling for Linux and Windows guests

## Development

```bash
make all          # tidy + fmt + lint + test + build
make test         # go test -race -cover
make lint         # golangci-lint on linux + darwin + windows
make fmt-check    # gofumpt + goimports check
make help         # full target list
```

## Related

- [cocoon](https://github.com/cocoonstack/cocoon) — VM engine
- [vk-cocoon](https://github.com/cocoonstack/vk-cocoon) — virtual-kubelet provider; the planned primary consumer of cocoon-agent
- [cocoon-common](https://github.com/cocoonstack/cocoon-common) — shared metadata / annotation contract

## License

[MIT](LICENSE)
