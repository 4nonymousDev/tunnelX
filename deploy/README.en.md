# One-command server deployment

Place `install.sh` and the `tunnel-server` binary for the target architecture
in the same directory, then run:

```bash
chmod +x install.sh
sudo ./install.sh
```

The default tunnel port is `2222`; use `--port 3333` to change it. For an ARM64
build, select the file with `--binary tunnel-server-linux-arm64`. The installer
is idempotent: upgrades do not overwrite the host key, `authorized_keys`, the
management token, or SQLite data.

| Path | Purpose | Mode |
|---|---|---|
| `/usr/local/bin/tunnel-server` | Single server binary with embedded management UI | `0755` |
| `/etc/tunnel-server/host_key` | SSH host private key | `0600` |
| `/etc/tunnel-server/authorized_keys` | TunnelX client public keys | `0600` |
| `/etc/tunnel-server/admin.token` | 32-byte random management token (64 hex characters) | `0600` |
| `/var/lib/tunnel-server` | SQLite database, WAL, and SHM files | `0700` |

The management service listens only on `127.0.0.1:2223`. Do not add a firewall
or cloud security-group rule for port 2223. Access it through a system SSH
forward:

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

Then open `http://127.0.0.1:2223` and enter the contents of
`/etc/tunnel-server/admin.token`. The token is kept only in the current page's
memory.

Connection, access, and management audits are now stored in SQLite; the new
version does not create JSONL or logrotate configuration. If a legacy
`/var/log/tunnel-server/audit.jsonl` exists during an upgrade, the installer
keeps it for manual archival.

Uninstall with:

```bash
sudo ./install.sh --uninstall          # Keep configuration and data
sudo ./install.sh --uninstall --purge  # Delete configuration and data
```

See [DEPLOY.en.md](../DEPLOY.en.md) for the complete guide.
