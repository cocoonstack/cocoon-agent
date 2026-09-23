# Usage

## Smoke test from the host

The host reaches the agent through cocoon: `cocoon vm exec` dials the VM's hybrid-vsock Unix socket and speaks the agent protocol.

```bash
cocoon vm exec <vm> -- echo "hello from guest"
echo "world" | cocoon vm exec -i <vm> -- cat
```

Stdin is attached only with `-i`; exit codes pass through.
