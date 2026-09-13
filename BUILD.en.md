# TunnelX — Build and Development

See [README.en.md](README.en.md) for a project overview.

## Prerequisites

| Dependency | Purpose | Verified version |
|---|---|---|
| Go 1.22+ | Compilation | go1.26.5 |
| Node.js + npm | Build the Electron/Vue desktop app and embedded management UI | Node.js 22+ |

The entire project builds without CGO: **`CGO_ENABLED=0` is sufficient**, so
cross-compilation works from any platform.

Verify the toolchain:

```bash
go version     # go1.22 or later
```

## Build

```bash
go mod download
go build ./...      # Compile check
go test ./...       # Run tests
go vet ./...        # Static checks
```

### New client (Windows Electron GUI)

Build the standalone Go core from the repository root, then build the desktop
shell:

```powershell
go build -ldflags "-s -w -X main.Version=0.1.0" -o tunnelx-cli.exe ./cmd/tunnelx-cli
Set-Location desktop
npm install
npm run build
npm run package
```

`npm run package` includes `tunnelx-cli.exe` as an independent core process.
The Electron main process communicates with it over a local API protected by a
random token; the Vue renderer never handles that token. See
[`desktop/README.en.md`](desktop/README.en.md) for more development options.

### Client (generic Linux CLI)

The CLI needs no CGO and can be cross-compiled as a static ELF:

```bash
# x86_64: Debian, Ubuntu, RHEL, Fedora, Arch, Alpine, etc.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnelx-cli-linux-amd64 ./cmd/tunnelx-cli

# ARM64: ARM servers, Raspberry Pi, etc.
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnelx-cli-linux-arm64 ./cmd/tunnelx-cli
```

The program does not depend on systemd. Run it in the foreground or supervise
it with systemd, OpenRC, runit, s6, a container orchestrator, or another process
manager. Use `--config` and `--state-dir` to separate read-only configuration
from writable state.

### Server (Linux)

Build the management UI first so its output is placed in the Go embed-resource
directory, and only then compile the server. Releases and CI must preserve this
order:

```bash
cd admin-web
npm ci
npm run typecheck
npm run build
cd ..
```

```bash
# x86_64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnel-server-linux-amd64 ./cmd/tunnel-server

# ARM64 (Raspberry Pi and some cloud hosts)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnel-server-linux-arm64 ./cmd/tunnel-server
```

The result is one static ELF containing the management REST API and SPA. The
target machine needs no Node, npm, external static assets, or system SQLite.
The pure-Go SQLite driver preserves CGO-free amd64/arm64 cross-compilation.

## Repository layout

```text
cmd/
  tunnelx-cli/       Headless core host and local control commands
  tunnel-server/     Server entry point
internal/
  proto/             Control protocol messages and JSON Lines codec
  config/            config.json I/O and machine identity
  logbuf/            Ring log buffer, file sink, and rotation
  keyperm/           Private-key ACL checks and one-command icacls repair
  sshconn/           The single SSH connection, heartbeat, and host-key checks
  tunnel/            Per-tunnel goroutine, failure classification, and backoff
  control/           Control-channel client
  manager/           Global connection loop driving the layers above
  core/              UI-independent app service, snapshots, events, confirmations
  localapi/          Authenticated, versioned local JSON/NDJSON API
  registry/          Server-side online registry
  session/           Online SSH sessions and forwarding-port ownership
  policy/            Fingerprint blacklist snapshot and authentication gate
  store/             SQLite clients, blacklist, and audit data
  adminapi/          Management REST API, SSE, and embedded SPA
  server/            SSH access, forwarding, and lifecycle orchestration
admin-web/            Vue 3 + TypeScript management UI source
desktop/
  src/main/          Electron main process, core supervision, local API
  src/preload/       Isolated allow-listed IPC bridge
  src/renderer/      Vue 3 interface
deploy/
  install.sh         One-command server installer
```

The package boundary reflects the two reconnect layers: `manager` reconnects
the SSH connection itself, while `tunnel` handles the success or failure of an
individual forward. Individual tunnels do not reconnect independently because
they share the global SSH connection.

## Tests

```bash
go test ./...                          # Everything
go test ./internal/control/ -v         # End-to-end data path
go test ./internal/server/ -v          # Server and security boundaries
```

Integration tests use a **real server, not a mock**, because they verify that
both protocol implementations agree.

Important cases:

| Test | What it verifies |
|---|---|
| `TestEndToEndTunnel` | Full path: echo service ← Exporter ← server ← Importer ← client |
| `TestControlChannelDoesNotBlockForwarding` | The control channel does not block forwarding on the same connection |
| `TestNoShellAccess` | A tunnel key cannot obtain a shell |
| `TestNoArbitraryForwardTarget` | The server cannot become an arbitrary-target relay |
| `TestResolveRemotePort` | Resolution by peer identity and source port |
| `TestAuthKeysHotReload` | Changes to `authorized_keys` take effect immediately |

### Some tests are guardrails, not ceremony

`sameconn_test.go` guards against a serious historical bug: the server once
handled the control channel **synchronously** inside its channel loop. Because
that handler blocks until the connection closes, later forwarding requests on
the same connection queued forever. The observable symptom was a connection
that hung after establishment with no useful error on either side.

Earlier tests missed this because they placed control and forwarding channels
on **different SSH connections**. A real Importer uses both on the same
connection, which is the point of SSH multiplexing.

Likewise, `security_test.go` ensures that if someone later adds session-channel
support for debugging, `TestNoShellAccess` immediately reports the broken
privilege boundary.

## Troubleshooting

The client writes its runtime log beside the executable:

| File | Contents |
|---|---|
| `tunnelx.log` | Full runtime log, including startup environment information (version, paths, machine identity, and tunnel configuration) |

Server logs:

```bash
sudo journalctl -u tunnel-server -f
```
