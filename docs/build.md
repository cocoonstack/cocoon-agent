# Build

```bash
make build                                # local build
GOOS=linux GOARCH=amd64 make build        # linux guest, x86_64
GOOS=linux GOARCH=arm64 make build        # linux guest, arm64
GOOS=windows GOARCH=amd64 make build      # windows guest → cocoon-agent-windows-amd64.exe
```

See [Development](../README.md#development) for the full contributor loop (tidy, fmt, lint, test).
