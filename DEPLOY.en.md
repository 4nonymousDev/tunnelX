# Deploying tunnel-server on Linux

The server is a CGO-free single binary containing the SSH tunnel service, the
management REST API, and the management SPA. The tunnel endpoint listens on
`:2222` by default; the management endpoint is forced to listen only on
`127.0.0.1:2223`.

For an existing public server, follow the [upgrade instructions](deploy/README.en.md#existing-installations-and-recovery). Account login requires updated server and client programs while retaining their keys and configuration. Already authorized v2 clients outside account management may continue under the original manual authorization rules.

## Build

Build the management UI before compiling the Go binary that embeds it:

```bash
cd admin-web
npm ci
npm run typecheck
npm run build
cd ..

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-amd64 ./cmd/tunnel-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-arm64 ./cmd/tunnel-server
```

The target machine does not need Node, npm, external web assets, CGO, or a
system SQLite installation.

## One-command installation

```bash
scp deploy/install.sh deploy/upgrade.sh tunnel-server-linux-amd64 <user>@<server>:~/
ssh <user>@<server>
chmod +x install.sh
sudo ./install.sh
```

The installer creates the `tunnel` nologin account and prepares:

- `/etc/tunnel-server/host_key`, with
  directory mode `0700` and file mode `0600`;
- `/var/lib/tunnel-server/authorized_keys`, mode `0600`, for atomic management updates;
- `/var/lib/tunnel-server`, mode `0700`, for the database, WAL, and SHM files;
- a systemd service with `StateDirectory=tunnel-server`,
  `StateDirectoryMode=0700`, and `UMask=0077`.

Administrator accounts live in the database and must be explicitly initialized below. Existing `admin.token` files remain untouched but no longer authenticate requests; new installations do not create tokens. The installer no longer
creates JSONL or logrotate configuration. Upgrades also leave a legacy
`/var/log/tunnel-server/audit.jsonl` file untouched.

## Important systemd settings

```ini
[Service]
User=tunnel
Group=tunnel
StateDirectory=tunnel-server
StateDirectoryMode=0700
UMask=0077
ExecStart=/usr/local/bin/tunnel-server \
  -addr :2222 \
  -hostkey /etc/tunnel-server/host_key \
  -auth /var/lib/tunnel-server/authorized_keys \
  -admin-addr 127.0.0.1:2223 \
  -data-dir /var/lib/tunnel-server
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
RestrictNamespaces=true
```

The legacy `-audit` option is no longer part of the main flow. Online state is
kept in memory, while client records, the blacklist, connection audits, access
audits, and administrative actions are persisted in
`/var/lib/tunnel-server/tunnel-server.db`.

## Network and management access

Only the TunnelX SSH port (2222 by default) needs the appropriate firewall or
cloud security-group rule. Never expose port 2223; the management server also
rejects non-loopback listen addresses.

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

After installation or upgrading from tokens, create an administrator in the server's terminal. For custom deployments, use the actual service user and data directory:

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

The password is entered privately at the terminal, never as a command-line argument. Keep the SSH forward open, browse to `http://127.0.0.1:2223`, and log in with the administrator account. An HttpOnly cookie retains login across page refreshes. Logout, server restart, session expiry, account disabling or a password reset requires login again. Sessions expire after 7 days or 12 idle hours. Existing and newly created client accounts do not automatically receive administrator privileges. Run the same local command to recover an administrator password; this also requires that account's devices to log in again. See the [admin login guide](ADMIN_LOGIN_GUIDE.md).

## Accounts and client access

Open **Account management (账号管理) → Create account** in the admin console. Set the username, password, device limit, and audit reason. The default device limit is 10, configurable from 1 to 50.

The user selects **Account login (账号登录)** in the client, enters the server address and credentials, and verifies the first server fingerprint. The existing device key enrolls automatically; a missing key is generated. No `.pub` upload or manual device ID is needed. See the [account login guide](ACCOUNT_LOGIN_GUIDE.md).

Disabling an account or resetting its password closes all of its sessions and invalidates existing device credentials. Devices must log in with the current password again, including after re-enabling; this is distinct from ordinary outage recovery. Passwords are not stored by the client. Existing keys, IDs, tunnels, and server trust are retained.

### Advanced: existing manual authorization

Already authorized v2 devices outside account management retain their original public-key and identity-binding rules. **Devices and advanced management (设备与高级管理) → Existing public keys and identities** keeps manual `.pub`/original-ID registration, binding changes, and revocation. A matching active binding is retained; historical self-reported IDs are not automatically trusted.

`authorized_keys` is hot-reloaded, but adding a key manually does not create its
device ID binding. An empty file can revoke the last key; invalid contents or
unsupported key options deny new admissions. Management-side blocking uses
the fingerprint computed by the server and does not modify this file.

After a device joins an account, a remaining `authorized_keys` entry cannot bypass account disabling, password reset, blacklisting, or device revocation.

## Operations

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

Before a backup, briefly stop the service and copy all of
`/var/lib/tunnel-server`. Database timestamps are UTC Unix microseconds; the
boundary for “rejected today” uses the server operating system's timezone.

The current database is schema 5, including explicit administrator privileges, accounts and device ownership; client configuration remains schema 2. Recovery requires matching program and complete database backups. Version 1 clients must upgrade to v2, which routes to the exact exporter session and publication generation without relay ports. Unsupported authorization-file options or invalid contents fail closed. See the [upgrade instructions](deploy/README.en.md#existing-installations-and-recovery) and [deployment guide](deploy/README.en.md).
