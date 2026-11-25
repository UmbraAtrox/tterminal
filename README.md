Note: Shared as-is for personal workflows; not actively maintained.


# tterminal

`tterminal` is a persistent terminal service for coding agents. It exposes an MCP stdio server that talks to a local Unix-socket daemon. Sessions, output streams, rendered screens, and waiters are owned by the daemon, so long-running commands, background processes, serial devices, and tmux panes survive beyond a single shell invocation.

The default backend is a native Linux PTY. Serial and tmux are explicit alternative backends. There is no remote transport in v1: `tterminal mcp` is stdio-only, and sharing is local through the Unix socket.

## Build

```sh
go build -o tterminal ./cmd/tterminal
```

## MCP client setup

Point your MCP client at the binary with the `mcp` subcommand:

```json
{
  "mcpServers": {
    "tterminal": {
      "command": "/absolute/path/to/tterminal",
      "args": ["mcp"]
    }
  }
}
```

If the daemon is not already running, `tterminal mcp` starts it detached. Daemon logs go to `$XDG_RUNTIME_DIR/tterminal/daemon.log`. Run `tterminal doctor` to inspect config, socket, tmux, and serial device availability.

## Tools

| Tool | Purpose |
| --- | --- |
| `term_open` | Open a PTY, serial, or tmux session |
| `term_write` | Write bytes with `utf8`, `base64`, or `hex` encoding |
| `term_read` | Read from a byte cursor as `raw`, `text`, `hex`, `screen`, or `both` |
| `term_wait` | Wait for `data`, `exit`, `regex`, `quiet`, or a command marker |
| `term_exec` | Run a shell command inside a session and capture its exit code |
| `term_screen` | Read the rendered VT screen |
| `term_signal` | Send a signal; serial supports `BRK` |
| `term_control` | Backend/session control actions such as DTR/RTS or ring clear |
| `term_resize` | Resize the terminal |
| `term_close` | Close and remove a session |
| `term_list` | List sessions |
| `term_info` | Describe a session |
| `term_devices` | List serial devices |

Sessions are explicit: callers keep `session_id` and byte cursors. The daemon does not maintain per-client cursors.

## Backends

### PTY

```json
{
  "backend": "pty",
  "pty": {
    "argv": ["/bin/bash", "-i"]
  },
  "cols": 120,
  "rows": 36
}
```

If `argv` is omitted, `shell` is used with an interactive shell. Shell integration injects OSC 133 command markers and OSC 7 cwd updates. Process groups are isolated with `setsid`; close kills the full process tree.

### Serial

```json
{
  "backend": "serial",
  "serial": {
    "device": "/dev/ttyUSB0",
    "baud_rate": 115200,
    "data_bits": 8,
    "parity": "none",
    "stop_bits": "1",
    "flow_control": "none"
  }
}
```

Serial sessions are raw byte streams. They have no pid, exit status, or POSIX signals. Use `term_wait until=quiet|regex|data`, `term_signal signal=BRK`, and `term_control` for modem lines and buffer flushes. Use `format=hex` or `encoding=base64` for non-UTF-8 traffic.

### tmux

```json
{
  "backend": "tmux",
  "tmux": {
    "socket_name": "tterminal-1000",
    "session": "build",
    "create": true,
    "command": "bash -i",
    "remain_on_exit": true
  },
  "cols": 120,
  "rows": 36
}
```

tmux uses a private socket and `remain-on-exit` so pane state can be reattached after daemon restarts. Output is streamed by polling `capture-pane`; input uses `send-keys`.

## Configuration

Config is loaded from `TTERMINAL_CONFIG`, then `$XDG_CONFIG_HOME/tterminal/config.json`, then `~/.config/tterminal/config.json`. Missing config uses safe defaults.

```json
{
  "daemon": {
    "socket_path": "/run/user/1000/tterminal/daemon.sock",
    "pid_file": "/run/user/1000/tterminal/daemon.pid",
    "token": "",
    "log_level": "info"
  },
  "limits": {
    "max_sessions": 64,
    "max_ring_bytes": 8388608,
    "max_chunk_bytes": 1048576,
    "default_cols": 120,
    "default_rows": 36,
    "read_timeout_ms": 1000,
    "wait_timeout_ms": 30000,
    "session_ttl_ms": 3600000,
    "quiet_poll_ms": 25
  },
  "security": {
    "default_role": "writer",
    "roles": {},
    "allowed_roots": [],
    "allowed_devices": [],
    "allowed_executables": [],
    "deny_env": ["AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "OPENAI_API_KEY"],
    "allow_shell_integration": true
  }
}
```

The daemon UID always has the `owner` role. Other local UIDs receive `default_role` or an explicit role from `security.roles`, keyed by numeric UID. Roles are `deny`, `reader`, `writer`, `controller`, and `owner`. Local peers are identified with `SO_PEERCRED`; `TTERMINAL_TOKEN` can add a shared local token.

## Protocol notes

The MCP server advertises `2026-07-28` and falls back to `2025-11-25`. It uses the official Go SDK and newline-delimited JSON over a local Unix socket. It does not depend on Tasks or Events extensions. Tool errors are returned as tool results with `isError`, while protocol errors remain JSON-RPC errors.

## Development

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./... -count=1
```

Tests cover the ring buffer, PTY lifecycle and signals, tmux lifecycle and MCP flow, session waits, command markers, rendered screens, and the in-memory MCP tool surface.

## Credits

Implemented with OpenCode, powered by `big-pickle`.
