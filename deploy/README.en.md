# One-command server deployment

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

In the management page, open **Account management → Create account
(账号管理 → 创建账号)** and enter a username, password, device limit and audit
reason. The limit defaults to 10 and accepts 1–50; there is no self-registration.
The client enters the server address and the administrator-provided credentials,
verifies the server fingerprint on first connection, and automatically enrolls
the device. Existing keys, device IDs and tunnels are preserved; a missing key
is generated automatically. No `.pub` upload or manual device ID is required.
See the [account login guide](../ACCOUNT_LOGIN_GUIDE.md).

Ordinary restarts can reconnect with the enrolled device key. Disabling an
account or resetting its password disconnects all of its sessions and requires
password login next time. Re-enabling it does not automatically restore those
sessions. Passwords are not stored in client configuration or logs.

**Devices and advanced management → Advanced: legacy keys and identities
(设备与高级管理 → 高级：存量公钥与身份管理)** retains manual key/ID registration
and identity changes. Existing authorized v2 devices may continue under their
manual authorization rules. Conflicts, identity changes, reactivation and
revocation remain explicit actions. Once enrolled into an account, a device
cannot fall back to legacy file authorization to bypass account status, even
if its key remains in `authorized_keys`.

Connection, access, and management audits are now stored in SQLite; the new
version does not create JSONL or logrotate configuration. If a legacy
`/var/log/tunnel-server/audit.jsonl` exists during an upgrade, the installer
keeps it for manual archival.
The server database is schema 5; client configuration remains schema 2.

Uninstall with:

```bash
sudo ./install.sh --uninstall          # Keep configuration and data
sudo ./install.sh --uninstall --purge  # Delete configuration and data
```

See [DEPLOY.en.md](../DEPLOY.en.md) for the complete guide.

## Existing installations and recovery

Keep the old service running while uploading the new binary and both scripts into a separate directory. Run `sudo bash install.sh`, without `--port`. Before stopping the service, the script verifies the new executable accepts the existing flags and validates the authorization file with `-check-auth`. It reads the running process arguments and preserves addresses, ports, the host key, data directory and other arguments. The legacy `-admin-token-file` option remains accepted but no longer authenticates; a missing token does not block upgrading. Only `-auth` moves to `authorized_keys` inside the writable data directory so the management API can replace it atomically. The host key and any remaining old token stay read-only.

After stopping the service, the script copies the entire database directory and original credentials into `/var/backups/tunnel-server/<timestamp>-<PID>/`, together with the old binary and systemd configuration. This directory is root-only. It then atomically replaces the binary, writes a separate `90-tunnelx-upgrade.conf` and verifies the actual running arguments. A backup failure attempts to restart the original service. A restart interrupts existing business connections; still-authorized v2 clients reconnect according to their retry policy. v1 clients must upgrade, and account login also requires an updated client. Retain existing configuration, dedicated private keys and known_hosts.

Inactive services, custom launch wrappers, conflicting authorization files or missing host keys/data cause an explicit refusal. For an offline upgrade, inspect the original `ExecStart` and every drop-in during a maintenance window. Back up the full data directory, host key, any existing legacy token, authorization file, binary and all unit configuration before replacement. Validate the authorization file using the new binary's `-check-auth -auth <existing-file>`. Copy authorization to the writable data directory and update only its path and write permissions; preserve all other arguments and never generate replacement device or host keys. The service account must own the data directory (`0700`) and authorization file (`0600`); systemd must allow writing to that directory while keeping the host key and any existing token read-only. Then initialize an administrator as described above.

If startup fails, do not point the old binary at a database already migrated by the new version. Stop the service, block public ingress, retain the failed state, and restore the matching binary, complete database directory and unit/drop-ins using the backup's `paths.txt`, or deploy a corrected build carrying the same security fixes. An old vulnerable version may only be restored for isolated recovery, never reopened directly to the Internet. Before reopening, check account login, existing device authentication, tunnel connections and audits. Backups contain credentials and must not be committed or attached to issues.

New installations set `LimitNOFILE=4096`, `TasksMax=256`, `MemoryHigh=768M` and `MemoryMax=1G`. Upgrades preserve explicit settings for these limits and add defaults only when a setting is absent from the existing unit/drop-ins. Choose connection quotas and systemd memory/CPU limits according to machine capacity; limits that are too low interrupt all sessions. The scripts do not modify firewall rules.
