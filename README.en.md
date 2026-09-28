# TunnelX

[Chinese](README.md)

Securely expose private-network services to authorized remote machines through a public server.

TunnelX is an SSH port-forwarding tool for remote development and debugging. It publishes services from an office network and makes them available from home or while traveling as if they were local.

```text
   Office PC                    Public server                    Laptop
┌──────────────┐            ┌─────────────┐            ┌──────────────┐
│ nginx :80    │            │             │            │ Browser      │
│      ▲       │            │             │            │ localhost:80 │
│      │       │            │             │            │      │       │
│  TunnelX  ═══╪═══ SSH ═══▶│ verified    ┼═══ SSH ═══╪══ TunnelX    │
│  [Export]    │            │ SSH routing │            │  [Import]     │
└──────────────┘            └─────────────┘            └──────────────┘
```

## Features

- The client core is independent of its UI and can be used by the desktop app, CLI, or a future Web UI.
- The Windows desktop application uses Electron and Vue and talks to the standalone Go core through a local API.
- The client, CLI, and server can all be built as single files and do not depend on the system OpenSSH installation.
- The server embeds a management REST API and Vue admin console in one binary, with history and audit data persisted in SQLite.
- Importers bind a verified device, public key, and tunnel ID, checking the current session and publication generation on each connection.
- The server provides no shell and accepts no arbitrary server-address or TCP-port forwarding.

For an existing installation, read the [upgrade instructions](deploy/README.en.md#existing-installations-and-recovery). Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Existing keys, IDs and tunnels are preserved; valid account-enrolled devices migrate to independent key authorization, while restricted devices remain restricted. See the [client access guide](CLIENT_ACCESS_GUIDE.md).

## Quick Start

### 1. Deploy the server

You need a Linux server with a public IP and systemd.

Tagged GitHub releases build Linux amd64 / arm64 archives containing the CLI, server and deployment scripts. Download the matching architecture and follow the [Linux release instructions](deploy/README.en.md#linux-release-downloads), or compile from source below.

```bash
# Build the embedded admin console first
cd admin-web && npm ci && npm run typecheck && npm run build && cd ..

# Cross-compile on the development machine (CGO is not required)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-amd64 ./cmd/tunnel-server

# Upload and install
scp deploy/install.sh deploy/upgrade.sh tunnel-server-linux-amd64 user@your-server:~/
ssh user@your-server 'chmod +x install.sh && sudo ./install.sh'
```

The installer creates the service account, generates the host key, configures systemd, and enables startup at boot. See [deploy/README.en.md](deploy/README.en.md).

Allow the tunnel port (2222 by default) through the firewall and, for a cloud server, its security group. Never expose management port 2223 through either. Access it through system SSH forwarding:

```bash
ssh -L 2223:127.0.0.1:2223 user@your-server
# Browse to http://127.0.0.1:2223
```

### 2. Prepare a device key

Open Connection settings and retain the existing private key, or generate one for a new device. Send the public key and the device ID shown in settings to the administrator.

### 3. Register and connect

Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Existing keys, IDs and tunnels are preserved; valid account-enrolled devices migrate to independent key authorization, while restricted devices remain restricted. See the [client access guide](CLIENT_ACCESS_GUIDE.md).

After registration, save settings and connect. Verify the server fingerprint on first connection. Keep private keys on the client.

### 4. Create tunnels

On the **Exporter** (the machine sharing a service), click **Add** on the **Export** tab and enter a local address and port such as `127.0.0.1:80`.

On the **Importer** (the machine accessing it), click **Add from online list** on the **Import** tab and select the peer.

Then open `http://localhost:<your-local-port>` to reach the peer service.

## Configuration File

Desktop configuration defaults to Electron's `userData/config.json`; the CLI uses its selected configuration file. Manual editing is normally unnecessary.

Client configuration remains at schema 2; the server database upgrades to schema 7. Migrations preserve existing identities, keys, and tunnels. Back up before upgrading; see the [upgrade instructions](deploy/README.en.md#existing-installations-and-recovery).

See [config.example.json](config.example.json) for a complete annotated example.

### Connection settings

| Field | Description |
|---|---|
| `id` | Local machine identity. **When empty, it is generated on first start and never changes.** Other machines use it as `peer_id`. **Never copy one ID to multiple machines**; the server rejects the later connection. |
| `name` | Display name; defaults to the computer name and may be changed at any time. |
| `server_addr` | `host:port`. The port belongs to tunnel-server (2222 by default), not the system sshd on port 22. |
| `key_path` | Private-key path. Relative paths are resolved from the configuration directory. |

The `server_user` field in legacy configurations is ignored; connections always use TunnelX's internal protocol value.

### Export tunnels

```json
{
  "kind": "export",
  "name": "nginx",
  "enabled": true,
  "local_host": "127.0.0.1",
  "local_port": 80
}
```

`local_host` is **not limited to the current machine**. Use another private-network address to export a NAS, printer, or router administration page.

Each export has a unique `id`, so one client can export the same port from different addresses, such as `127.0.0.1:80` and `192.168.1.50:80`. The client generates an empty ID automatically; manually assigned IDs must be unique.

The server routes directly to the verified exporter's SSH connection; no relay port is allocated or configured.

### Import tunnels

```json
{
  "kind": "import",
  "name": "Office nginx",
  "enabled": true,
  "peer_id": "peer id",
  "peer_tunnel_id": "peer export tunnel id",
  "peer_name": "Office PC",
  "peer_src_port": 80,
  "listen_port": 8080
}
```

| Field | Description |
|---|---|
| `peer_id` | The peer's `id` from `config.json`. A typo looks like an offline peer, but logs list the actual registry values for comparison. |
| `peer_tunnel_id` | The peer export's `id`. Together with `peer_id`, it distinguishes identical ports on different addresses. Prefer **Add from online list**. |
| `peer_name` | Display only. Renaming the peer does not affect matching, which uses `peer_id + peer_tunnel_id`. |
| `peer_src_port` | The peer's `local_port`, used for display, the default local port, and compatibility with old configurations lacking `peer_tunnel_id`. It is **not** the server-allocated port. |
| `listen_port` | Local listening port, which may differ from `peer_src_port`. Binding below 1024 on Windows requires administrator privileges. |

Prefer **Add from online list** because manually entering `peer_id` is error-prone.

`enabled: false` temporarily disables a tunnel without deleting its configuration.

An old import without `peer_tunnel_id` is completed only when its `peer_id + peer_src_port` uniquely matches the trusted registry. Ambiguous matches are rejected. Version 1 programs must upgrade to v2.

## Linux CLI Client

`tunnelx-cli` is a CGO-free portable Linux client that does not depend on Debian or systemd. A static ELF for an architecture works across Debian, Ubuntu, RHEL, Fedora, Arch, Alpine, and other mainstream distributions.

### Public-key access

Generate a dedicated key for a new device, then give its `.pub` file and the `id` from `config.json` to the administrator. Authorized devices run with their existing configuration; no account password is required.

### Generate a device key

The CLI generates a tunnel-specific key without depending on the system `ssh-keygen`:

```bash
./tunnelx-cli keygen \
  --username alice \
  --email alice@example.com \
  --output tunnel_key
```

`--output` defaults to `tunnel_key` in the current directory. The command never overwrites an existing private key or `.pub` file. Username and email are optional; supplied metadata is plaintext in the public-key comment, and the hostname is not collected automatically. Register the public key and device ID together in the console.

### Portable execution

Configure the server address and retain existing configuration and keys. Generate and register a key before connecting a new device:

```bash
chmod +x tunnelx-cli
./tunnelx-cli run
```

`run` keeps the core in the foreground; press Ctrl-C to stop. The first connection displays the host fingerprint and asks for confirmation.

Use `--bg` to detach from the terminal on both Windows and Linux:

```bash
./tunnelx-cli run --bg
./tunnelx-cli status
./tunnelx-cli stop
```

Use `./tunnelx-cli.exe` in Windows PowerShell. Windows runs without a console;
Linux creates a separate session with `setsid`. All standard streams are detached.
The launcher returns a PID after local initialization succeeds, or an error if startup
fails. Successful startup does not imply a successful server connection. Logs continue
to rotate in `tunnelx.log` under the state directory; follow them with `logs --follow`.
`stop` shuts down the core, while `disconnect` leaves the process running.

Confirm the host key in the foreground first and reuse the same configuration and
state directory, or pass `--accept-host-key SHA256:verified-fingerprint`. Without a
fingerprint, background mode handles confirmations through the local API: inspect
pending requests with `status`, verify them, then use `confirm --id N --accept`.
Use matching `--config`, `--state-dir`, and custom `--endpoint` options, if supplied,
for startup and control commands. `--bg` only applies to `run`; the shorthand
`tunnelx-cli --bg` also works.

This mode provides neither startup at boot nor automatic crash recovery. It does not
guarantee survival across user logout, system sleep, or cleanup by an external process
manager. Use Windows Task Scheduler or Linux systemd for system-managed execution.

### System service execution

Configuration and runtime state may be separated:

```bash
./tunnelx-cli run \
  --config /etc/tunnelx/config.json \
  --state-dir /var/lib/tunnelx
```

Relative `key_path` values resolve from the configuration directory. `known_hosts`, logs, and local-control credentials are written under `--state-dir`, or under the configuration directory when it is omitted.

Unattended runs cannot confirm fingerprints interactively. Verify the fingerprint through an independent channel first:

```bash
./tunnelx-cli run \
  --config /etc/tunnelx/config.json \
  --state-dir /var/lib/tunnelx \
  --accept-host-key 'SHA256:verified-fingerprint'
```

Control that instance from another terminal with the same `--config` and `--state-dir`:

```bash
./tunnelx-cli status --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli tunnels --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli logs --follow --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli disconnect --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli connect --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
```

`tunnels` shows each export's `id`; enter it as `peer_tunnel_id` in a Linux Importer configuration. The desktop **Add from online list** action fills it automatically.

For a pending confirmation, get its ID from command output and then run:

```bash
./tunnelx-cli confirm --id 1 --accept \
  --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
```

## Architecture

```text
Electron + Vue desktop ─┐
                         ├─> Local API v1 ─> core.Service ─> manager ─> SSH ─> tunnel-server
tunnelx-cli commands ───┘          ▲
                                     │
                            tunnelx-cli run
```

- `core.Service` manages configuration, connection lifecycle, tunnels, logs, events, and confirmation requests.
- The Electron main process starts or attaches to the core and manages the tray and single-instance behavior; the Vue renderer may call only allowlisted IPC.
- `tunnelx-cli run` hosts the headless core and exposes its local API on a random `127.0.0.1` port.
- CLI commands and the desktop application reuse the same interface; closing the UI does not interrupt the core.
- The API address and random token are stored in `.tunnelx-control.json` under the state directory, with Unix mode `0600`.
- See [`desktop/README.en.md`](desktop/README.en.md) for Electron development, packaging, and path overrides.

## Documentation

| Document | Contents |
|---|---|
| [CLIENT_ACCESS_GUIDE.md](CLIENT_ACCESS_GUIDE.md) | Public-key registration, device migration, and preserved data |
| [BUILD.en.md](BUILD.en.md) | Build instructions, directory layout, tests, and troubleshooting |
| [DEPLOY.en.md](DEPLOY.en.md) | Complete deployment guide, key planning, firewall, and audit logs |
| [deploy/README.en.md](deploy/README.en.md) | One-click installer usage |

## Comparison

| | TunnelX | frp / ngrok | Dev Tunnels |
|---|---|---|---|
| Transport | SSH | Custom protocol / HTTP | HTTPS |
| Authentication | Device public key and ID binding | Token | Microsoft account |
| Server | Self-hosted | Self-hosted / hosted | Hosted |
| Client | Headless core + desktop/CLI front ends | CLI + configuration file | VS Code extension / CLI |
| Dependencies | None | None | VS Code / .NET |

`golang.org/x/crypto/ssh` supplies SSH transport encryption and public-key verification. Password authentication is limited to the administration console; SSH clients use public keys only.

## System Requirements

| | Client | Server |
|---|---|---|
| System | Windows 10/11 desktop; mainstream Linux amd64/arm64 CLI | Any Linux with systemd |
| Dependencies | None | None |
| Size | Desktop about 26 MB; CLI about 7–8 MB | About 4.7 MB |

> **Known issue:** On Windows Server 2019 the tray icon works but the main window does not appear. Windows 10/11 work normally.

## Security

- The server routes to an exact device, key, tunnel, session, and publication generation, without relay ports or shell access.
- Arbitrary server-address forwarding and legacy TCP forwarding are rejected.
- SSH password authentication is disabled. Device public keys require administrator approval and a matching device ID; migrated keys preserve existing restrictions.
- The management API may bind only to an explicit loopback address and requires administrator account login. An HttpOnly session cookie retains login; writes require matching origin and CSRF validation. Legacy Bearer tokens are not accepted.
- Online client state is memory-only; profiles, blacklist entries, and new audit events use pure-Go SQLite.
- Clients validate private-key permissions with Windows ACLs or Unix file modes.
- Clients display the server host fingerprint on first connection and enforce it thereafter to prevent man-in-the-middle attacks.

These boundaries are enforced by `internal/server/security_test.go`; loosening them later causes the tests to fail.

## Status

The complete path has been tested between Windows 11 and a Linux server. Core functionality is usable and details continue to be refined. Issues are welcome.

Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Existing keys, IDs and tunnels are preserved; valid account-enrolled devices migrate to independent key authorization, while restricted devices remain restricted. See the [client access guide](CLIENT_ACCESS_GUIDE.md). The server database is schema 7 and client configuration remains schema 2. Existing authorized v2 clients can connect directly; v1 programs must upgrade. Recovery requires matching program and database backups.
