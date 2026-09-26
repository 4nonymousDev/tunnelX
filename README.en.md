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

For an existing installation, read the [upgrade instructions](UPGRADE_GUIDE.md). Account login requires updated server and client programs; existing keys, IDs, and tunnel configurations are retained. Already authorized v2 clients outside account management may continue under their manual authorization rules.

## Quick Start

### 1. Deploy the server

You need a Linux server with a public IP and systemd.

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

### 2. An administrator creates an account

Open **Account management (账号管理) → Create account** in the admin console. Set a username, password, device limit, and audit reason. The default limit is 10 devices, configurable from 1 to 50. Give the user the server address and credentials through a trusted channel. There is no public self-registration.

### 3. Log in from the client

Open **Account login (账号登录)** in the updated desktop client. Enter the server address, such as `your-server.com:2222`, device name, username, and password. Verify the server fingerprint when first prompted. The client enrolls the device and connects automatically: no `.pub` upload or manual device ID is needed.

Existing keys, device IDs, and tunnels are retained. A missing device key is generated automatically. Passwords are used only for the current login and are not saved to configuration or logs. These are not Linux accounts. Ordinary accounts cannot use the admin console, which requires explicitly provisioned administrator privileges and retains login across refreshes. See the [admin login guide](ADMIN_LOGIN_GUIDE.md).

Normal restarts and brief outages reconnect using the local device credential. Disabling an account or resetting its password closes all its sessions. Devices must enter the current password again, including after re-enabling; they do not recover automatically. See the [account login guide](ACCOUNT_LOGIN_GUIDE.md).

<details>
<summary>Advanced: existing public keys and device identities</summary>

Already authorized v2 devices outside account management can continue using their original `authorized_keys` and trusted identity binding. **Devices and advanced management (设备与高级管理) → Existing public keys and identities** retains manual key registration, original-ID binding, rotation, and revocation. Adding a key to the file alone does not establish a trusted ID. Historical self-reported IDs are not automatically trusted.

The authorization file reloads about every two seconds and rejects unsupported options such as `from=` and `command=`. Avoid concurrent manual edits and management imports. After an existing device logs into an account, that account controls admission; a remaining file entry cannot bypass account disabling, password reset, or device revocation. Never reuse a private key that grants system access to the server.

</details>

### 4. Create tunnels

On the **Exporter** (the machine sharing a service), click **Add** on the **Export** tab and enter a local address and port such as `127.0.0.1:80`.

On the **Importer** (the machine accessing it), click **Add from online list** on the **Import** tab and select the peer.

Then open `http://localhost:<your-local-port>` to reach the peer service.

## Configuration File

Desktop configuration defaults to Electron's `userData/config.json`; the CLI uses its selected configuration file. Manual editing is normally unnecessary.

Client configuration remains at schema 2; the server database upgrades to schema 5. Migrations preserve existing identities, keys, and tunnels. Back up before upgrading; see the [upgrade instructions](UPGRADE_GUIDE.md).

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

### Account login

Configure the server address, run the core, and log in from a second terminal using the same configuration path:

```bash
./tunnelx-cli run --confirm-via-api
# In a second terminal:
./tunnelx-cli login --username alice
```

The password is read without terminal echo and is never passed as a command-line argument. Existing device keys are reused; a missing key is generated. No `.pub` submission is required. Verify the first server fingerprint as described in the [account login guide](ACCOUNT_LOGIN_GUIDE.md).

### Advanced: generate a key for manual device management

The CLI generates a tunnel-specific key without depending on the system `ssh-keygen`:

```bash
./tunnelx-cli keygen \
  --username alice \
  --email alice@example.com \
  --output tunnel_key
```

`--output` defaults to `tunnel_key` in the current directory. The command never overwrites an existing private key or `.pub` file. Username and email are optional; supplied metadata is plaintext in the public-key comment, and the hostname is not collected automatically. This is an advanced manual-management tool, not a prerequisite for account login.

### Portable execution

Configure the server address and retain existing configuration and keys. A new device can generate its key during account login:

```bash
chmod +x tunnelx-cli
./tunnelx-cli run
```

`run` keeps the core in the foreground; press Ctrl-C to stop. The first connection displays the host fingerprint and asks for confirmation.

After completing the first host-key confirmation, you can keep the client running after
the terminal closes without creating a systemd service by using `nohup`:

```bash
nohup ./tunnelx-cli run > /dev/null 2>&1 < /dev/null &
echo $! > tunnelx-cli.pid
```

Standard output and standard error are discarded, while TunnelX continues writing its
internal log to `tunnelx.log` in the configuration directory. Use
`tail -f tunnelx.log` to follow it and `kill "$(cat tunnelx-cli.pid)"` to stop the process.
This method does not start automatically after a reboot or restart the process after a
crash; use systemd when those capabilities are required.

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
| [ACCOUNT_LOGIN_GUIDE.md](ACCOUNT_LOGIN_GUIDE.md) | Administrator-created accounts, automatic device enrollment, and logging in again |
| [BUILD.en.md](BUILD.en.md) | Build instructions, directory layout, tests, and troubleshooting |
| [DEPLOY.en.md](DEPLOY.en.md) | Complete deployment guide, key planning, firewall, and audit logs |
| [deploy/README.en.md](deploy/README.en.md) | One-click installer usage |

## Comparison

| | TunnelX | frp / ngrok | Dev Tunnels |
|---|---|---|---|
| Transport | SSH | Custom protocol / HTTP | HTTPS |
| Authentication | Account enrollment, then device public key | Token | Microsoft account |
| Server | Self-hosted | Self-hosted / hosted | Hosted |
| Client | Headless core + desktop/CLI front ends | CLI + configuration file | VS Code extension / CLI |
| Dependencies | None | None | VS Code / .NET |

`golang.org/x/crypto/ssh` supplies SSH transport encryption and public-key verification. Account passwords are used for restricted device enrollment; the server stores salted password hashes rather than plaintext. See the [account login guide](ACCOUNT_LOGIN_GUIDE.md).

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
- Password authentication permits device enrollment only. Business connections use device keys; old authorization-file entries cannot bypass account disabling or password resets.
- The management API may bind only to an explicit loopback address and requires administrator account login. An HttpOnly session cookie retains login; writes require matching origin and CSRF validation. Legacy Bearer tokens are not accepted.
- Online client state is memory-only; profiles, blacklist entries, and new audit events use pure-Go SQLite.
- Clients validate private-key permissions with Windows ACLs or Unix file modes.
- Clients display the server host fingerprint on first connection and enforce it thereafter to prevent man-in-the-middle attacks.

These boundaries are enforced by `internal/server/security_test.go`; loosening them later causes the tests to fail.

## Status

The complete path has been tested between Windows 11 and a Linux server. Core functionality is usable and details continue to be refined. Issues are welcome.

Security upgrade: account login requires updated server and client programs. Existing keys, IDs, and tunnels are retained. Previously authorized v2 devices may continue under manual authorization until enrolled into an account; v1 programs must upgrade. The database is schema 5 and client configuration remains schema 2. Preserve matching program and data backups when recovering. See the [account login guide](ACCOUNT_LOGIN_GUIDE.md), [upgrade instructions](UPGRADE_GUIDE.md), and [deployment guide](deploy/README.en.md).
