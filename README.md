# cocoon-agent

In-VM exec agent for [Cocoon](https://github.com/cocoonstack/cocoon)-managed VMs. Listens on virtio-vsock and runs commands on behalf of host-side callers (vk-cocoon, the cocoon CLI, anything else with vsock access on the same node), replacing SSH for control-plane operations like `kubectl exec`. It also handles post-clone **reseed** — injecting host entropy into the guest CRNG and regenerating `/etc/machine-id` so cloned VMs don't stay correlated.

**Documentation: [cocoonstack.github.io/cocoon-agent](https://cocoonstack.github.io/cocoon-agent/)** (source in [`docs/`](docs/)).

## Highlights

- Linux and Windows guest exec with stdin, stdout, stderr, and exit-code forwarding.
- Host access through `cocoon vm exec`; direct AF_VSOCK client for smoke tests.
- Linux clone/restore reseed with optional machine-ID regeneration.

PTY mode is planned; see
[Roadmap](docs/architecture.md#roadmap).

## Quick start

```bash
cocoon-agent client --cid 3 --port 1024 -- echo "hello from guest"
```

Full steps in [Usage](docs/usage.md).

## Related projects

- [cocoon](https://github.com/cocoonstack/cocoon) — VM engine
- [vk-cocoon](https://github.com/cocoonstack/vk-cocoon) — virtual-kubelet provider; the primary consumer of cocoon-agent
- [cocoon-common](https://github.com/cocoonstack/cocoon-common) — shared metadata / annotation contract

## Development

```bash
make all          # tidy + fmt + lint + test + build
make build        # build the local binary
make test         # vet + tests with race detection and coverage
make lint         # golangci-lint on linux + darwin + windows
make fmt          # gofumpt + goimports
make fmt-check    # check formatting without changing files
```

## License

[MIT](LICENSE)
