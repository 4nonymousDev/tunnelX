# Deploying tunnel-server on Linux

The server is a CGO-free single binary containing the SSH tunnel service, the
management REST API, and the management SPA. The tunnel endpoint listens on
`:2222` by default; the management endpoint is forced to listen only on
`127.0.0.1:2223`.

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
sudo ./install.sh
```

The installer creates the `tunnel` nologin account and prepares:

- `/etc/tunnel-server/host_key`, `authorized_keys`, and `admin.token`, with
  directory mode `0700` and file mode `0600`;
- `/var/lib/tunnel-server`, mode `0700`, for the database, WAL, and SHM files;
- a systemd service with `StateDirectory=tunnel-server`,
  `StateDirectoryMode=0700`, and `UMask=0077`.

`admin.token` is a 64-character hexadecimal representation of 32 random bytes
and is never overwritten when it already exists. The installer no longer
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
  -auth /etc/tunnel-server/authorized_keys \
  -admin-addr 127.0.0.1:2223 \
  -admin-token-file /etc/tunnel-server/admin.token \
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

Keep that system SSH session open, then browse to `http://127.0.0.1:2223`.
Read the management token on the server with:

```bash
sudo cat /etc/tunnel-server/admin.token
```

## Client authorization

Use a separate SSH key for each client and append its public key as one line:

```bash
cat tunnel_key.pub | ssh <user>@<server> \
  'sudo tee -a /etc/tunnel-server/authorized_keys > /dev/null'
```

`authorized_keys` is hot-reloaded. An empty file can revoke the last key. An
invalid line rejects that reload and preserves the previous valid set.
Management-side blocking uses the SSH SHA256 fingerprint computed by the
server and does not modify this file.

## Operations

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

Before a backup, briefly stop the service and copy all of
`/var/lib/tunnel-server`. Database timestamps are UTC Unix microseconds; the
boundary for “rejected today” uses the server operating system's timezone.
