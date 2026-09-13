# SSH Tunnel Manager — Design Document

[中文](DESIGN.md)

> This document records the architecture decisions and their rationale. The
> implementation has been completed and validated on real machines.
>
> See [README.en.md](README.en.md) for usage and [BUILD.en.md](BUILD.en.md) for
> build instructions.

## 1. Goals and constraints

Replace a set of PowerShell scripts with a UI application that can quickly
create port mappings on arbitrary target machines for remote development and
debugging. It is an operations tool in the same broad category as Dev Tunnels,
frp, and ngrok: internal services are exposed through a public server only to
authorized remote developers.

### Hard constraints

| Constraint | Meaning |
|---|---|
| Single file | One executable contains all code and dependencies |
| No installation | Copy it to a machine and run it |
| No runtime dependencies | No .NET, Node, OpenSSH, or WebView required on the target |
| Small | Keep the binary as compact as practical |

The target may be a temporary or third-party development machine, so no
preinstalled software can be assumed.

### Soft requirements

- A UI for configuration.
- Diagnosable failure logs rather than opaque exit codes.
- A future server component capable of exchanging information such as the
  currently available ports.

## 2. Technology selection

### Original conclusion: Go + Fyne

| Layer | Choice | Rationale |
|---|---|---|
| Language | Go | True static single-file builds with no runtime |
| SSH | `golang.org/x/crypto/ssh` | Pure Go; target machines need no OpenSSH |
| GUI | Fyne | Standalone native-style window without a browser or WebView |
| Configuration | JSON beside the executable | Portable with the executable |

Electron was originally rejected for size, self-contained .NET for its runtime
footprint, Tauri/Wails for their WebView2 dependency, and a browser-based local
UI because browser and process lifetimes do not align. Fyne's accepted costs
were a CGO toolchain on the Windows build machine, a Material-style appearance,
and a smaller widget set. These historical choices describe the original
client design; the current repository also contains the newer Electron/Vue
desktop client described in [desktop/README.en.md](desktop/README.en.md).

## 3. System architecture

Three parties participate:

```text
Machine A (Exporter)       Public server S       Machine B (Importer)
local service :80  --SSH--> loopback :dynamic <--SSH-- localhost :80
       control channel --> online registry <-- control channel
```

One program supports two tunnel types:

| Mode | Purpose | SSH primitive | Go API |
|---|---|---|---|
| Exporter | Push a local port to the server | Remote forwarding (`-R`) | `client.Listen()` |
| Importer | Pull a server port to localhost | Local forwarding (`-L`) | `net.Listen()` + `client.Dial()` |

They share the SSH connection, UI, and logging system; only the traffic
direction differs.

## 4. Key design decisions

### 4.1 Carry control traffic in an SSH channel

Registry queries and mapping publication use a custom channel on the existing
SSH connection. This reuses SSH public-key authentication, transport encryption,
and multiplexing instead of exposing and securing a separate protocol port.
The client opens `tunnel-ctrl@devhelper`; the server recognizes that channel
type.

Every accepted channel must be handled in its own goroutine. A synchronous
control handler blocks the channel-accept loop and causes later `direct-tcpip`
requests on the same connection to hang. `internal/server/sameconn_test.go`
guards this invariant.

#### Independent SSH server

`tunnel-server` listens independently (2222 by default) using
`x/crypto/ssh`; it does not extend the system sshd. A system sshd cannot handle
the custom channel or provide the required server push. Port 22 remains for
normal administration, while TunnelX uses its own host key and
`authorized_keys`. The additional port still speaks authenticated, encrypted
SSH—not a custom unauthenticated protocol.

#### Dedicated tunnel keys

Tunnel clients use a dedicated `tunnel_key`, never a cloud/root login key.
System sshd and TunnelX read separate authorization files, limiting the tunnel
key to forwarding. The server accepts only its control and forwarding channel
types and rejects `session`, shell, exec, and SFTP access. Security tests also
reject non-loopback forwarding targets and unregistered keys.

#### Multiplexing model

SSH multiplexing means one authenticated TCP connection carries multiple
logical streams identified by channel IDs. Each has open/confirm messages,
window-based flow control, data messages, and an independent close lifecycle as
defined by RFC 4254. It is distinct from multiple clients merely sharing one
TCP listen port.

#### Why not WebSocket inside SSH?

SSH channels already provide a long-lived encrypted, authenticated, full-duplex
stream with server push. WebSocket would add HTTP Upgrade and framing without
providing a missing capability. JSON Lines supplies the only needed framing.

### 4.2 One SSH connection, many channels

An Importer does not need a preliminary SSH connection to discover ports.
After one `ssh.Dial`, it opens the control channel, receives the registry,
starts local listeners, and uses `client.Dial` to open forwarding channels on
that same connection. This avoids duplicate authentication, stale discovery,
unrelated reconnect state machines, and ambiguous auditing.

### 4.3 Server-assigned remote ports

Exporters request remote port zero. The server atomically chooses an available
port and returns it; only then does the client publish the mapping. This uses
native SSH behavior and avoids check-then-bind races.

### 4.4 Editable Importer local port

The source port is the default local listen port because it is intuitive, but
users may change it when the port is occupied or privileged.

### 4.5 Registry combines client metadata with server validation

The client supplies useful names and descriptions; the server verifies that a
reported port is genuinely forwarded by that authenticated session. Entries
contain client identity, service/tunnel name, source port, allocated remote
port, online time, and version. They disappear immediately when the client
disconnects.

### 4.6 Independent tunnel goroutines

Each tunnel has an independent goroutine, start/stop lifecycle, failure state,
and backoff. One failed forward must not tear down unrelated forwards.

### 4.7 Keep remote forwards loopback-only

Remote listeners remain on loopback (`GatewayPorts no` semantics). Internal
services must never become directly reachable from the public Internet; every
access path must first pass SSH authentication.

## 5. Control protocol

The protocol runs over the custom SSH channel.

### 5.1 JSON Lines encoding

Each message is one JSON object followed by `\n`. `json.Encoder.Encode` writes
messages and a scanner reads them. This format is easy to inspect in diagnostic
logs, evolves through optional fields, and requires no generated code. Explicit
framing is required because an SSH channel, like TCP, is a byte stream rather
than a message stream.

### 5.2 Full-snapshot synchronization

Exporter publications and server registry broadcasts are complete snapshots,
never deltas. A lost delta can leave peers permanently inconsistent; a later
snapshot naturally repairs state. Registry sizes are small enough that the
extra bytes are irrelevant.

### 5.3 Message shapes

Every message contains protocol version `v` and `type`.

```jsonc
// Client to server
{"v":1,"type":"hello","role":"exporter","id":"a3f8...","name":"Office PC","client_version":"0.1.0"}
// Server to client
{"v":1,"type":"hello_ok","server_version":"0.1.0"}
```

After a remote forward is allocated, an Exporter sends its full tunnel list:

```jsonc
{"v":1,"type":"publish","tunnels":[
  {"src_port":80,"remote_port":8080,"name":"Web service"}
]}
{"v":1,"type":"publish_ok"}
```

After hello and whenever state changes, the server broadcasts a complete
registry snapshot containing stable ID, display name, source port, allocated
remote port, tunnel name, online time, and client version.

Errors are structured:

```json
{"v":1,"type":"error","code":"duplicate_id","msg":"UUID is already registered by another connection"}
```

### 5.4 Acknowledgements

Every client request receives either `*_ok` or `error`. Transport delivery does
not mean the server accepted a request, so explicit acknowledgement is needed
for useful diagnostics.

### 5.5 Machine-readable error codes

`version_mismatch`, `duplicate_id`, and `bad_request` are fatal until a user or
software update intervenes. `port_alloc_failed`, `server_busy`, and
`internal_error` are retryable. Clients classify by code, never localized error
text.

### 5.6 Version negotiation

The first exchange includes protocol versions. A mismatch stops immediately
with an upgrade instruction instead of producing later, ambiguous failures.

### 5.7 Importers also identify themselves

Importers send role, stable ID, and name even though they consume rather than
publish registry data. This enables online-client visibility and future audit
features. They do not publish which peer they use; instead, every registry
change is broadcast and each Importer compares its selected peer locally.

## 6. UI design

Export and Import use separate tabs because their actions differ fundamentally:
Export is configured manually and receives an allocated remote port; Import is
selected from the live registry and may have a peer-offline state. A global
connection banner is separate from per-tunnel state.

The original Fyne design uses a virtualized list rather than a multiline Entry
for colored, bounded logs (about 2,000 entries). Background goroutines publish
events; only the UI-thread consumer mutates widgets.

## 7. Problems corrected from the scripts

The Go architecture removes dependencies on `ssh.exe`, preserves structured
errors, closes only its own tunnels, isolates forwarding failures, uses
exponential backoff, rotates rather than truncates logs, persists editable
configuration, verifies host keys, and diagnoses unsafe private-key ACLs.

On first contact the user explicitly confirms the server fingerprint; later
mismatches are fatal man-in-the-middle warnings. On Windows, broad private-key
ACLs produce a warning with an automatic `icacls` repair option and an explicit
“ignore and continue” escape hatch. Keys remain on disk, so this ACL protection
is mandatory rather than cosmetic.

## 8. Reconnection state machine

### 8.0 Two layers

There is one global SSH connection and many tunnel channels. A broken SSH
connection invalidates all channels and must reconnect first. A failed tunnel
while SSH remains healthy affects only that tunnel.

### 8.1 Heartbeat

An SSH keepalive runs every 15 seconds. Three consecutive missing responses
declare the connection dead, normally within about 45 seconds.

### 8.2 Failure classification

The sole criterion is whether waiting can repair the condition. Network loss,
server downtime, heartbeat timeout, peer offline, and transient allocation or
server failures retry. Missing/invalid keys, rejected authentication, host-key
mismatch, occupied local ports, privileged-port errors, malformed requests,
and incompatible versions stop with actionable errors.

### 8.3 Backoff

Retryable failures use 5s, 10s, 20s, 40s, 80s, and so on up to five minutes,
without a retry limit. Fatal failures do not retry.

### 8.4 Recovery

After reconnect, restart tunnels whose user intent is “running” and whose last
failure was retryable. Keep user-stopped and fatally failed tunnels stopped.

### 8.5 Local listener lifecycle

Importer listeners close when SSH disconnects, making failure visible as a
connection refusal rather than a hanging request. If rebinding later fails, the
Windows error should name the process and PID occupying the port.

### 8.6 Peer offline

When an Exporter disappears, the server broadcasts the changed registry. The
Importer marks that tunnel peer-offline, closes its listener, retries, and
recreates the tunnel automatically after the peer returns.

### 8.7 Stable peer matching

Allocated server ports can change after reconnect. Importers therefore remember
the Exporter identity plus stable tunnel/source identity, then resolve the new
remote port from the latest registry.

### 8.8 Exporter identity

Each machine has an editable display name and a generated UUID stored in
configuration. The name is readable; the UUID is the stable matching key and
prevents collisions from common or cloned computer names.

### 8.9 UI states

Per-tunnel states are running (green), reconnecting (yellow with countdown),
stopped by user (gray), and fatal error (red with cause). A global SSH failure
appears once in the top banner rather than being duplicated on every row.

## 9. Component structure

The client contains the UI, `x/crypto/ssh` connection layer, one multiplexed
control channel, Exporter and Importer goroutines, an event path back to the UI,
configuration, and known_hosts. The independent server owns SSH admission,
forwarding, online registry state, control requests, and registry broadcasts.
See [BUILD.en.md](BUILD.en.md) for the current package layout.

### 9.1 Online registry is memory-only

The registry answers “who is online now” and is tied to active SSH sessions.
Persisting it would restore stale entries after a restart. Historical audit is
a separate concern and must not drive online status.

### 9.2 Concurrent Importers are allowed

Several Importers may access one Exporter. Each accepted connection has its own
SSH channel, just like concurrent visitors to a website. No protocol complexity
is added merely to show the Exporter who is visiting it.

### 9.3 System tray behavior

Closing the original desktop window minimizes it to the tray; quitting is an
explicit tray action. The first close explains this behavior. Tray color can
summarize healthy, reconnecting, or failed state.

### 9.4 Single instance

A second launch activates the existing window and exits. Multiple instances
would compete for local ports and connect with the same UUID. One instance
connects to one server.

## 10. Resolved and reserved items

The document resolves reconnect behavior, protocol format, online-registry
persistence, concurrent Importers, tray behavior, and single-instance
enforcement. Confirmed values include protocol `v:1`, channel name
`tunnel-ctrl@devhelper`, a roughly 2,000-line log ring, and five-minute maximum
backoff. Private keys remain on disk, making ACL validation required.

The only explicitly reserved item in the original design is historical
server-side audit, kept separate from live registry decisions. The current
server implementation now persists client and audit history in SQLite while
still keeping live session/registry state in memory.
