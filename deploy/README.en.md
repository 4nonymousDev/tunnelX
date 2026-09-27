# One-command server deployment

## Linux release downloads

Pushing a new `v*` tag publishes Windows assets and two Linux archives in the same GitHub Release:

- `TunnelX-<release-version>-linux-amd64.tar.gz` for x86_64.
- `TunnelX-<release-version>-linux-arm64.tar.gz` for aarch64 / ARM64.
- `SHA256SUMS-linux.txt` with checksums for both archives.

Each Linux archive contains `tunnel-server` with a freshly built admin UI, `tunnelx-cli`, `install.sh`, `upgrade.sh` and documentation. Choose the architecture matching `uname -m`, extract into an empty directory, then run `sudo bash install.sh --binary tunnel-server`. Existing services use the upgrade path that preserves their arguments and data. Run `./tunnelx-cli --help` for CLI commands. Keep CLI configuration and state in persistent directories and reuse the original `--config` / `--state-dir` and keys when upgrading.

Download both archives and the checksum file, then run `sha256sum -c SHA256SUMS-linux.txt`. When downloading only one architecture, use `sha256sum --ignore-missing -c SHA256SUMS-linux.txt`. Archive names use the Release version; the bundled CLI/server use `CLI_VERSION`. See `VERSION.txt`, `./tunnelx-cli version` or `./tunnel-server -version`.

Linux publication runs after Windows publication succeeds. Packaged amd64 binaries pass a real connection test before upload; arm64 is cross-compiled. This applies to future tags and does not automatically add assets to existing releases.

## Install the service

Place `install.sh`, `upgrade.sh` and the `tunnel-server` binary for the target architecture
in the same directory, then run:

```bash
chmod +x install.sh
sudo ./install.sh
```

The default tunnel port is `2222`; use `--port 3333` to change it. For an ARM64
build, select the file with `--binary tunnel-server-linux-arm64`. The installer
is idempotent: upgrades do not overwrite the host key, `authorized_keys`, the
legacy token file, or SQLite data. Tokens no longer authenticate requests.

| Path | Purpose | Mode |
|---|---|---|
| `/usr/local/bin/tunnel-server` | Single server binary with embedded management UI | `0755` |
| `/etc/tunnel-server/host_key` | SSH host private key | `0600` |
| `/var/lib/tunnel-server/authorized_keys` | TunnelX client public keys, atomically writable by the management API | `0600` |
| `/var/lib/tunnel-server` | SQLite database, WAL, and SHM files | `0700` |

The management service listens only on `127.0.0.1:2223`. Do not add a firewall
or cloud security-group rule for port 2223. Access it through a system SSH
forward:

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

After installation or upgrading from tokens, initialize an administrator in the server terminal:

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

The password is entered privately at the terminal. Custom deployments must use the actual service user and data directory. An existing username has its password reset and gains administrator access; ordinary accounts are never promoted automatically. Keep the SSH forward open, visit `http://127.0.0.1:2223`, and log in with the administrator account. Refreshing retains the cookie session. Logout, server restart or expiry requires login again. See the [admin login guide](../ADMIN_LOGIN_GUIDE.md).

Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Existing keys, IDs and tunnels are preserved; valid account-enrolled devices migrate to independent key authorization, while restricted devices remain restricted. See the [client access guide](../CLIENT_ACCESS_GUIDE.md).

New devices submit their public key and device ID for administrator approval. Authorized v2 clients can connect directly with existing keys; updating the client removes the old login UI. Administrator password changes do not revoke device keys.

Connection, access, and management audits are now stored in SQLite; the new
version does not create JSONL or logrotate configuration. If a legacy
`/var/log/tunnel-server/audit.jsonl` exists during an upgrade, the installer
keeps it for manual archival.
The server database is schema 7; client configuration remains schema 2.

Uninstall with:

```bash
sudo ./install.sh --uninstall          # Keep configuration and data
sudo ./install.sh --uninstall --purge  # Delete configuration and data
```

See [DEPLOY.en.md](../DEPLOY.en.md) for the complete guide.

## Optional and required client updates

Required updates are disabled by default. Publish ordinary updates for users to install when convenient. For a required update, publish a usable client package first, then open **客户端更新** in the admin console, enable the policy, and set a minimum version, deadline, message, and audit reason. The default grace period is **7 days**, and the administrator can adjust it. The minimum refers to the CLI core version; verify the version bundled with the desktop package.

Older clients keep working during the grace period. GUI/CLI 0.2.1 and later display the requirement. After the server's deadline, unsupported versions cannot establish new sessions or forwarding channels, and existing sessions and tunnels are closed, normally within one second (subject to the control write timeout). Supported clients remain connected. The fixed deadline survives server restarts and client reconnects. Administrators can postpone or disable the policy; a disconnected client can then reconnect manually. Keys, administrator accounts, configuration and tunnels are preserved. Database migration does not enable this policy.

Older v2 clients lack the new reminder UI but remain usable during the grace period and are subject to server enforcement afterwards. v1 is incompatible. A future incompatible protocol change requires advance client upgrades while the server still supports the old protocol; a grace period cannot bridge incompatible protocols.

The server never supplies executable update commands or download URLs. Desktop 0.2.4 and later support in-app updates for unsigned releases with download integrity checks; signed builds additionally verify the publisher. Versions 0.2.2 / 0.2.3 need a one-time manual upgrade. Client versions are self-reported compatibility metadata, not proof of binary integrity or vulnerability remediation.

## Existing installations and recovery

Keep the old service running while uploading the new binary and both scripts into a separate directory. Run `sudo bash install.sh`, without `--port`. Before stopping the service, the script verifies the new executable accepts the existing flags and validates the authorization file with `-check-auth`. It reads the running process arguments and preserves addresses, ports, the host key, data directory and other arguments. The legacy `-admin-token-file` option remains accepted but no longer authenticates; a missing token does not block upgrading. Only `-auth` moves to `authorized_keys` inside the writable data directory so the management API can replace it atomically. The host key and any remaining old token stay read-only.

After stopping the service, the script copies the entire database directory and original credentials into `/var/backups/tunnel-server/<timestamp>-<PID>/`, together with the old binary and systemd configuration. This directory is root-only. It then atomically replaces the binary, writes a separate `90-tunnelx-upgrade.conf` and verifies the actual running arguments. A backup failure attempts to restart the original service. A restart interrupts existing business connections; still-authorized v2 clients reconnect according to their retry policy. v1 clients must upgrade; authorized v2 clients can connect with their existing keys. Retain existing configuration, dedicated private keys and known_hosts.

Inactive services, custom launch wrappers, conflicting authorization files or missing host keys/data cause an explicit refusal. For an offline upgrade, inspect the original `ExecStart` and every drop-in during a maintenance window. Back up the full data directory, host key, any existing legacy token, authorization file, binary and all unit configuration before replacement. Validate the authorization file using the new binary's `-check-auth -auth <existing-file>`. Copy authorization to the writable data directory and update only its path and write permissions; preserve all other arguments and never generate replacement device or host keys. The service account must own the data directory (`0700`) and authorization file (`0600`); systemd must allow writing to that directory while keeping the host key and any existing token read-only. Then initialize an administrator as described above.

If startup fails, do not point the old binary at a database already migrated by the new version. Stop the service, block public ingress, retain the failed state, and restore the matching binary, complete database directory and unit/drop-ins using the backup's `paths.txt`, or deploy a corrected build carrying the same security fixes. An old vulnerable version may only be restored for isolated recovery, never reopened directly to the Internet. Before reopening, check administrator login, existing device authentication, tunnel connections and audits. Backups contain credentials and must not be committed or attached to issues.

New installations set `LimitNOFILE=4096`, `TasksMax=256`, `MemoryHigh=768M` and `MemoryMax=1G`. Upgrades preserve explicit settings for these limits and add defaults only when a setting is absent from the existing unit/drop-ins. Choose connection quotas and systemd memory/CPU limits according to machine capacity; limits that are too low interrupt all sessions. The scripts do not modify firewall rules.
