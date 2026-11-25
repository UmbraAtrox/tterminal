# AGENTS.md

## Project

`tterminal` is a Linux-first Go service that gives agents persistent terminals over MCP. `tterminal mcp` is a stdio-only proxy; the real work happens in a local Unix-socket daemon. Sessions own a byte ring, a VT screen, waiters, and a backend. There is no HTTP, TLS, OAuth, or remote transport in v1.

Do not add server-side per-client cursors; callers pass explicit `session_id` and cursors.

## Commands

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./... -count=1 -timeout 180s
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o tterminal ./cmd/tterminal
```

Run all three checks before finishing a change. Do not commit unless explicitly asked. Do not add code comments unless explicitly asked.

## Layout

- `cmd/tterminal/main.go`: CLI entrypoint: `daemon`, `mcp`, `doctor`.
- `internal/types`: shared backend kinds, capabilities, exit status, roles, identity.
- `internal/backend`: `ByteStreamBackend` plus PTY, serial, and tmux implementations.
- `internal/buffer`: byte ring with cursors and change waits.
- `internal/screen`: `vt-go` adapter for rendered screen snapshots.
- `internal/session`: `Manager`, `Session`, reader loop, OSC 133/7 parsing, waits, `term_exec`.
- `internal/daemon`: UDS listener, `SO_PEERCRED`, token handshake, MCP server, tool handlers.
- `internal/mcpproxy`: stdio-to-UDS raw JSON-RPC forwarding and detached daemon startup.
- `internal/config`: JSON config, defaults, env overrides, allowlists.

## Invariants

- The daemon owns sessions; proxy processes are stateless.
- Auto-spawned daemons must not inherit the caller's stdout/stderr. Logs go to `$XDG_RUNTIME_DIR/tterminal/daemon.log`.
- MCP protocol versions: `2026-07-28` and `2025-11-25`. Use the official Go SDK.
- Session IDs and cursors are explicit in tool arguments and results.
- `ByteStreamBackend` is the only backend contract. Unsupported operations return `unsupported_for_backend` errors.
- Native PTY is the default. tmux is opt-in. tmux streams output by polling `capture-pane`; it does not rely on control-mode `attach-session`.
- Serial sessions are raw bytes: no pid, exit status, POSIX signals, or process-tree kill. `BRK` maps to a serial break.
- Non-UTF-8 output must be returned as `base64` or `hex`.
- Daemon UID is always `owner`; other local UIDs use `security.roles` or `default_role`.
- Secret-like env vars are filtered before process start and never logged.

## Tool surface

`term_open`, `term_write`, `term_read`, `term_wait`, `term_exec`, `term_screen`, `term_signal`, `term_control`, `term_resize`, `term_close`, `term_list`, `term_info`, `term_devices`.

Tool input is nested by backend: `{ "backend": "pty", "pty": {...} }`, `{ "backend": "serial", "serial": {...} }`, or `{ "backend": "tmux", "tmux": {...} }`.

## Tests

Tests must not require external configuration. Skip tmux tests when `tmux` is absent; skip serial tests when no device is present. Prefer in-memory MCP transports for tool tests. When changing behavior, update the test that maps closest to the user-visible tool or backend contract.
