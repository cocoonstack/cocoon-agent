# Usage

## Smoke test from the host

cocoon-agent ships a `client` subcommand for vsock smoke tests without needing to plumb through cocoon CLI / vk-cocoon. It dials AF_VSOCK by CID, so it only reaches guests of a vhost-vsock hypervisor (cocoon's CH/FC VMs sit behind a per-VM hybrid-vsock Unix socket; reach them with `cocoon vm exec <vm> -- <command>`). Take the guest CID from the hypervisor's vsock config and run:

```bash
cocoon-agent client --cid 3 --port 1024 -- echo "hello from guest"
echo "world" | cocoon-agent client --cid 3 --port 1024 -- cat
```

Exit codes pass through.
