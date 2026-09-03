# Usage

## Smoke test from the host

cocoon-agent ships a `client` subcommand for vsock smoke tests without needing to plumb through cocoon CLI / vk-cocoon. It dials AF_VSOCK by CID, so take the guest CID from the hypervisor's vsock config (cocoon itself reaches the agent over the hybrid-vsock Unix socket that `cocoon vm inspect` reports as `vsock_socket`, not by CID) and run:

```bash
cocoon-agent client --cid 3 --port 1024 -- echo "hello from guest"
echo "world" | cocoon-agent client --cid 3 --port 1024 -- cat
```

Exit codes pass through.
