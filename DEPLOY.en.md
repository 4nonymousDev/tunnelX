# Deploying tunnel-server on Linux

The server is a CGO-free single binary containing the SSH tunnel service, the
management REST API, and the management SPA. The tunnel endpoint listens on
`:2222` by default; the management endpoint is forced to listen only on
`127.0.0.1:2223`.

For an existing public server, follow the [installation instructions](deploy/README.en.md#reinstallation-and-data-retention). Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Device registrations in the current database layout are retained, along with existing keys, IDs and tunnels. See the [client access guide](CLIENT_ACCESS_GUIDE.md).

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
scp deploy/install.sh tunnel-server-linux-amd64 <user>@<server>:~/
ssh <user>@<server>
chmod +x install.sh
sudo ./install.sh tunnel-server-linux-amd64
```

The installer creates the `tunnel` nologin account and prepares:

- `/etc/tunnel-server/host_key`, with
  directory mode `0700` and file mode `0600`;
- `/var/lib/tunnel-server/authorized_keys`, mode `0600`, for atomic management updates;
- `/var/lib/tunnel-server`, mode `0700`, for the database, WAL, and SHM files;
- a systemd service with `StateDirectory=tunnel-server`,
  `StateDirectoryMode=0700`, and `UMask=0077`.

Administrator accounts live in the database and must be explicitly initialized below. Audits are stored in SQLite.

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

Online state is
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

When the first installation has no administrator, create an administrator in the server's terminal. For custom deployments, use the actual service user and data directory:

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

The password is entered privately at the terminal, never as a command-line argument. Keep the SSH forward open, browse to `http://127.0.0.1:2223`, and log in with the administrator account. An HttpOnly cookie retains login across page refreshes. Logout, server restart, session expiry, account disabling or a password reset requires login again. Sessions expire after 7 days or 12 idle hours. Client devices do not use console accounts. The same local command recovers an administrator password without changing device authorization. See the [admin login guide](ADMIN_LOGIN_GUIDE.md).

## Client public-key access

Clients retain or generate a dedicated key in Connection settings. Administrators register the public key and device ID together under Device management. Verify the server fingerprint on first connection.

Clients retain their keys, IDs and configuration. Device authorizations in the current database layout are retained; disabled or revoked devices remain restricted. Administrator password changes do not affect devices. Block, revoke or restore devices through Device management. See the [client access guide](CLIENT_ACCESS_GUIDE.md).

Manual authorized_keys edits affect only file authorization; they neither establish an identity binding nor change database device authorizations. Use console blocking or identity revocation to stop device access immediately.

## Operations

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

### Handshake diagnostics and suspicious sources

Each TCP connection records an outcome when its SSH handshake completes or
admission rejects it. Logs include `remote` (source IP and port), `duration_ms`,
`result`, `reason` and the actual `error`. Once authentication starts, they can
also include `client_version`, `user`, `method`, `fingerprint`, `key_status` and
`auth_error`; unavailable fields are empty. Client versions, usernames and offered
key fingerprints are peer-supplied claims until authentication succeeds. No
passwords, signatures or raw packets are logged. External text is escaped and
truncated to 512 bytes.

Reasons distinguish `timeout`, `peer_closed`, `network_error`, `protocol_error`,
`authentication_failed`, `unauthorized_key`, `blocked_key`, and admission limits.
`result=success reason=authenticated` means SSH authentication succeeded;
subsequent device admission can still fail. Each failed or rejected connection
increments rejection totals once; normal SSH `none` method discovery is not
counted separately.

An IP reaching 10 failures in its one-minute counting window emits
`event=alert suspicious=true rule=frequent_failures`. Ports do not split an IP's
statistics. Each IP's window starts at its first recorded outcome and resets on
the first outcome after expiry; `window_start` identifies it. Handshake failures
and per-source concurrency/rate rejections count toward suspicion; global
capacity, shutdown and local setup failures do not. This is a frequency heuristic,
not proof of malicious intent; no IP is automatically banned. Append
`-handshake-failure-window 1m -handshake-failure-threshold 10` to systemd's
`ExecStart` to customize the defaults.

Low-volume outcomes retain details. Routine source details are sampled once per
five seconds, with early alerts at the threshold. The global budget is 20 detail
or source-summary lines per five seconds. Suppressed outcomes still count;
`suppressed` reports how many outcomes were previously withheld from output.
Periodic `event=summary` records retain the latest sample and counts even when
a source stops connecting. Tracking defaults to at most 4096 source IPs; when
full, an additional `event=overflow untracked_attempts=...` line reports outcomes
that could not be tracked individually. Graceful shutdown flushes pending source
records without the normal output rate limit. In-memory suspicion statistics
reset on restart. Details go to stdout (journal under systemd); SQLite retains
aggregate rejection counts without adding anonymous per-attempt audit rows.

```bash
sudo journalctl -u tunnel-server --since "1 hour ago" --no-pager -o short-iso > tunnel-server-log.txt
sudo journalctl -u tunnel-server --since "1 hour ago" --no-pager | grep 'suspicious=true'
```

Before a backup, briefly stop the service and copy all of
`/var/lib/tunnel-server`. Database timestamps are UTC Unix microseconds; the
boundary for “rejected today” uses the server operating system's timezone.

The server maintains one current database layout, including administrator accounts and independent device authorizations, with no historical migrations; client configuration uses the current structure. Recovery requires matching program and complete database backups. Forwarding uses the exact exporter session and publication generation without relay ports. Unsupported authorization-file options or invalid contents fail closed. See the [installation instructions](deploy/README.en.md#reinstallation-and-data-retention) and [deployment guide](deploy/README.en.md).
