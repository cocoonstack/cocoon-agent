# Configuration

| Env var | Default | Notes |
|---|---|---|
| `AGENT_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` (projecteru2/core/log levels) |

CLI flags:

| Flag | Default | Notes |
|---|---|---|
| `serve --port` | `1024` | vsock port to bind on |
| `client --cid` | required | target VM CID (host-side) |
| `client --port` | `1024` | matches `serve --port` |
