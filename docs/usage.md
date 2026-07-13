# Usage

## Smoke test from the host

cocoon-agent ships a `client` subcommand for vsock smoke tests without needing to plumb through cocoon CLI / vk-cocoon. Get the VM's CID from `cocoon vm inspect` and run:

```bash
cocoon-agent client --cid 3 --port 1024 -- echo "hello from guest"
echo "world" | cocoon-agent client --cid 3 --port 1024 -- cat
```

Exit codes pass through.
