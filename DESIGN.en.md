# TunnelX Architecture

[中文](DESIGN.md)

This document describes the current implementation. See [README](README.en.md) for usage, [BUILD](BUILD.en.md) for development, and [DEPLOY](DEPLOY.en.md) and the [installation guide](deploy/README.en.md#reinstallation-and-data-retention) for deployment and updates.

## Components

| Component | Responsibility |
|---|---|
| Go client core | Configuration and device identity, SSH connections, publishing, importing, and reconnection |
| Electron desktop app | Vue interface controlling the core through restricted IPC and a local API |
| Go server | SSH termination, authentication, online sessions, publications, and forwarding |
| Administration console | Embedded Vue SPA and an HTTP API bound only to loopback |
| SQLite | Administrator accounts, device authorizations, identity bindings, blocks, audit records, and administrative operation state |

The server and CLI use pure Go builds. The desktop app bundles Electron and a separate Go core. Neither tunnel endpoint needs system OpenSSH; system SSH is used for operations and forwarding the administration interface to the operator's computer.

## Client identity and public keys

Clients use public-key authentication only. SSH password login and the enrollment channel are removed. Administrators submit the key and device ID together; business access requires a trusted binding. Existing keys, IDs and tunnel configuration are retained.

Device authorizations in device_keys are independent of administrator accounts. Disabled, revoked or conflicting devices cannot fall back to an authorized_keys entry. Blacklists remain independent. Manually registered devices use authorized_keys plus identity bindings. Administrator password changes affect only console sessions.

## v2 routing and forwarding

An exporter publishes its local services with stable tunnel IDs. The server associates each publication with the authenticated device, SSH session, and publication version. Importers select targets from the registry rather than locating them through reusable relay listener ports on the server.

Every business channel checks the target key, device ID, tunnel ID, current session, and publication version. The server opens a forwarding channel on the exact exporter SSH connection; the exporter connects to its local service. Stale target information cannot keep routing after a session or publication changes.

A client can import and export simultaneously. Request cancellation, channel closure, and reconnection have distinct lifecycles. A forward consumes capacity on both endpoints; an outstanding open retains its reservation until it actually finishes.

The server and clients use one current business protocol. Replacing the program retains registered public keys, device IDs and data.

## Administrator authentication

The first administrator is provisioned or recovered using the local server `-admin-account` command. It reads a hidden password from a terminal and must run while the service is stopped, as the original service user with the original data directory. The console creates administrators and permits password resets and enabling/disabling.

The console uses account passwords to establish sessions. A random credential is placed in an HttpOnly, SameSite=Strict cookie; server memory indexes sessions by a hash of that credential. Reloading the page restores login through the session endpoint. HTTPS also sets Secure. HTTP administration requests are restricted to localhost or loopback hosts and are accessed through system SSH forwarding; proxy headers do not replace origin checks.

Writes require both matching origin and a CSRF credential. Every protected request checks the account's current enabled state, administrator role, and credential generation. Event streams revalidate before writing. Logout, password reset, disabling invalidates previous access.

Sessions last at most seven days and expire after twelve hours without management requests. An event stream alone does not extend that idle deadline. Limits are eight sessions per account and 256 globally. Restarting the server requires logging in again.

See the [administrator login guide](ADMIN_LOGIN_GUIDE.md) and [client access guide](CLIENT_ACCESS_GUIDE.md) for procedures.

## Security and resource boundaries

- The SSH service provides no shell, exec, or SFTP and permits no arbitrary host or port forwarding.
- Pre-authentication handshakes have deadlines and global/source limits. Sessions, publications, channels, and request rates are bounded.
- Registry limits use actual encoded size. Slow readers are disconnected without blocking broadcast indefinitely.
- Passwords use salted Argon2id hashes, with bounded hashing concurrency and login attempts.
- Administrative audit records identify the acting account. Critical persistence failures pause admission; uncertain commits require inspection and restart.
- Database and WAL size, audit retention, and background queues have budgets. Host resource limits are still required.
- Desktop locking and IPC authorization are checked in the main process; the renderer cannot directly access core credentials.
- Windows releases support in-app updates for unsigned packages after user confirmation, retaining download hash checks. Builds with a configured signing publisher also verify Authenticode.

Device keys govern admission. Authorized devices still share visibility and access to published tunnels. This version does not implement isolation between devices or per-tunnel access controls; local business services should expose only the intended access scope.

## Data and upgrades

Client configuration uses the current structure. The server maintains one current database layout: first startup creates tables, and subsequent startups retain existing records without historical migrations. The deployment script backs up the full data directory and keys before replacing the program.

Identity bindings, device authorization, and administrative operations awaiting reconciliation remain persistent. Ordinary audit history is maintained under retention and space budgets. Recovery uses the program and complete database backup saved by the deployment script.


## Code map

| Path | Contents |
|---|---|
| [cmd/tunnel-server](cmd/tunnel-server) | Server startup, flags, and local administrator recovery |
| [cmd/tunnelx-cli](cmd/tunnelx-cli) | CLI and key generation |
| [internal/server](internal/server) | SSH admission, v2 control, forwarding, and management operations |
| [internal/session](internal/session) | Online sessions and publications |
| [internal/store](internal/store) | SQLite initialization, accounts, identities, and audit |
| [internal/adminapi](internal/adminapi) | Browser sessions, HTTP API, and embedded assets |
| [internal/manager](internal/manager) | Client runtime and configuration management |
| [internal/sshconn](internal/sshconn) | SSH establishment and host trust |
| [desktop](desktop) | Electron main process, preload, and Vue interface |
| [admin-web](admin-web) | Administration frontend |

Automated tests cover identity conflicts, real SSH forwarding, revocation, resource limits, data persistence, and storage failures. Build and deployment documents describe the checks. A successful cross-build does not replace business traffic and recovery validation in the target environment.
