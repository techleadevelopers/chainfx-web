---
name: Go toolchain mismatch in this repl
description: go.mod requires a newer Go than the pre-installed module; how to fix build failures that look like network/checksum errors.
---

This repo's `go.mod` pins `go 1.25.0`. The base environment only ships `go-1.21`
as a Replit module, so `go build` fails with a confusing error:

```
go: downloading go1.25.0 (linux/amd64)
go: download go1.25.0: golang.org/toolchain@...: verifying module: checksum database disabled by GOSUMDB=off
```

**Why:** Go's auto-toolchain-switch tries to fetch the go1.25 toolchain as a
module, but the Replit package firewall's GOPROXY doesn't serve toolchain
tarballs the same way, and `GOSUMDB=off` masks the real cause.

**How to apply:** Don't fight the proxy/env vars. Use the package-management
skill to install the matching module (`installProgrammingLanguage({ language: "go-1.25" })`,
found via `listAvailableModules({ language: "go" })`) — this puts a real
go1.25 binary on PATH and `go build ./...` works immediately.
