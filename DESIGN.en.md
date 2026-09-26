# TunnelX Architecture

[中文](DESIGN.md)

This document describes the current implementation. See [README](README.en.md) for usage, [BUILD](BUILD.en.md) for development, and [DEPLOY](DEPLOY.en.md) and the [upgrade guide](deploy/README.en.md#existing-installations-and-recovery) for deployment and migration.

## Components

| Component | Responsibility |
|---|---|
| Go client core | Configuration and device identity, SSH connections, publishing, importing, and reconnection |
| Electron desktop app | Vue interface controlling the core through restricted IPC and a local API |
| Go server | SSH termination, authentication, online sessions, publications, and forwarding |
| Administration console | Embedded Vue SPA and an HTTP API bound only to loopback |
| SQLite | Accounts, device ownership, identity bindings, blocks, audit records, and administrative operation state |

The server and CLI use pure Go builds. The desktop app bundles Electron and a separate Go core. Neither tunnel endpoint needs system OpenSSH; system SSH is used for operations and forwarding the administration interface to the operator's computer.

## Client identity and accounts

An administrator creates accounts. The client verifies the server's host identity before submitting its account password over SSH. Existing private keys are preserved; a missing key is generated automatically. Passwords are not written to configuration, and subsequent connections use the local device key.

A password-authenticated connection permits only device enrollment, not a business session, shell, or arbitrary forwarding. The client signs enrollment data containing the current SSH session, account, device ID, and public key to prove possession of the private key. The server transactionally records ownership, identity, and audit information; the client then opens a normal public-key-authenticated connection.

Device IDs are bound to keys. Claiming an existing identity requires its original key. Enrollment cannot overwrite another account, a conflicting key, revocation, or a block. Disabling an account, resetting its password, or changing its role advances its credential generation and invalidates older device credentials.

Manually authorized keys without account ownership continue to use the authorization file and identity rules. Once owned by an account, a key cannot use that file to bypass account state. Copying a private key copies the device identity.

## v2 routing and forwarding

An exporter publishes its local services with stable tunnel IDs. The server associates each publication with the authenticated device, SSH session, and publication version. Importers select targets from the registry rather than locating them through reusable relay listener ports on the server.

Every business channel checks the target key, device ID, tunnel ID, current session, and publication version. The server opens a forwarding channel on the exact exporter SSH connection; the exporter connects to its local service. Stale target information cannot keep routing after a session or publication changes.

A client can import and export simultaneously. Request cancellation, channel closure, and reconnection have distinct lifecycles. A forward consumes capacity on both endpoints; an outstanding open retains its reservation until it actually finishes.

Only the v2 business protocol is implemented. Compatibility preserves keys, IDs, configuration, and data. v1 programs must upgrade; existing v2 programs may keep connecting under their established authorization.

## Administrator authentication

The first administrator is provisioned or recovered using the local server `-admin-account` command. It reads a hidden password from a terminal and must run while the service is stopped, as the original service user with the original data directory. Ordinary and migrated accounts have no administrative privilege by default. Existing administrators can explicitly grant or revoke that privilege in the console.

The console uses account passwords to establish sessions. A random credential is placed in an HttpOnly, SameSite=Strict cookie; server memory indexes sessions by a hash of that credential. Reloading the page restores login through the session endpoint. HTTPS also sets Secure. HTTP administration requests are restricted to localhost or loopback hosts and are accessed through system SSH forwarding; proxy headers do not replace origin checks.

Writes require both matching origin and a CSRF credential. Every protected request checks the account's current enabled state, administrator role, and credential generation. Event streams revalidate before writing. Logout, password reset, disabling, or demotion invalidates previous access.

Sessions last at most seven days and expire after twelve hours without management requests. An event stream alone does not extend that idle deadline. Limits are eight sessions per account and 256 globally. Restarting the server requires logging in again. Legacy management Bearer tokens no longer authenticate; their old startup option is accepted only to preserve service configuration compatibility.

See the [administrator login guide](ADMIN_LOGIN_GUIDE.md) and [client account guide](ACCOUNT_LOGIN_GUIDE.md) for procedures.

## Security and resource boundaries

- The SSH service provides no shell, exec, or SFTP and permits no arbitrary host or port forwarding.
- Pre-authentication handshakes have deadlines and global/source limits. Sessions, publications, channels, and request rates are bounded.
- Registry limits use actual encoded size. Slow readers are disconnected without blocking broadcast indefinitely.
- Passwords use salted Argon2id hashes, with bounded hashing concurrency and login attempts.
- Administrative audit records identify the acting account. Critical persistence failures pause admission; uncertain commits require inspection and restart.
- Database and WAL size, audit retention, and background queues have budgets. Host resource limits are still required.
- Desktop locking and IPC authorization are checked in the main process; the renderer cannot directly access core credentials.
- Production desktop distribution requires code signing. Unsigned builds do not perform automatic updates.

Accounts govern admission. Authorized devices still share visibility and access to published tunnels. This version does not implement isolation between accounts or per-tunnel access controls; local business services should expose only the intended access scope.

## Data and upgrades

Client configuration uses schema 2 and the server database uses schema 6. Migrations create backups and retain device keys, stable IDs, tunnel relationships, authorizations, and audit semantics rather than regenerating identities.

Identity bindings, account ownership, and administrative operations awaiting reconciliation remain persistent. Ordinary audit history is maintained under retention and space budgets. Rollback must restore matching program and database versions; an old program must not open an already migrated database.

The minimum-client-version policy is off by default and persists a minimum CLI version, fixed deadline and generation. Policy changes and audits commit together before refreshing the in-memory policy. Optional metadata in existing v2 handshake, registry and error messages preserves compatibility during the grace period. Handshake, publication and forwarding admission enforce the deadline; periodic checks close existing unsupported sessions. Self-reported versions do not replace identity or binary-integrity verification.

## Code map

| Path | Contents |
|---|---|
| [cmd/tunnel-server](cmd/tunnel-server) | Server startup, flags, and local administrator recovery |
| [cmd/tunnelx-cli](cmd/tunnelx-cli) | CLI and client login |
| [internal/server](internal/server) | SSH admission, v2 control, forwarding, and management operations |
| [internal/session](internal/session) | Online sessions and publications |
| [internal/store](internal/store) | SQLite, migrations, accounts, identities, and audit |
| [internal/adminapi](internal/adminapi) | Browser sessions, HTTP API, and embedded assets |
| [internal/manager](internal/manager) | Client runtime and configuration management |
| [internal/sshconn](internal/sshconn) | SSH establishment, host trust, and enrollment |
| [desktop](desktop) | Electron main process, preload, and Vue interface |
| [admin-web](admin-web) | Administration frontend |

Automated tests cover identity conflicts, real SSH forwarding, revocation, resource limits, migrations, and storage failures. Build and deployment documents describe the checks. A successful cross-build does not replace business traffic and recovery validation in the target environment.
