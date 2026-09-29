# One-command server deployment

## Linux release downloads

Pushing a new `v*` tag publishes Windows assets and two Linux archives in the same GitHub Release:

- `TunnelX-<release-version>-linux-amd64.tar.gz` for x86_64.
- `TunnelX-<release-version>-linux-arm64.tar.gz` for aarch64 / ARM64.
- `SHA256SUMS-linux.txt` with checksums for both archives.

Each Linux archive contains `tunnel-server` with a freshly built admin UI, `tunnelx-cli`, `install.sh` and documentation. Choose the architecture matching `uname -m`, extract into an empty directory, then run `sudo bash install.sh --binary tunnel-server`. First and repeated installations use the same flow and retain existing data. Run `./tunnelx-cli --help` for CLI commands. Keep CLI configuration and state in persistent directories and reuse the original `--config` / `--state-dir` and keys when upgrading.

Download both archives and the checksum file, then run `sha256sum -c SHA256SUMS-linux.txt`. When downloading only one architecture, use `sha256sum --ignore-missing -c SHA256SUMS-linux.txt`. Archive names use the Release version; the bundled CLI/server use `CLI_VERSION`. See `VERSION.txt`, `./tunnelx-cli version` or `./tunnel-server -version`.

Linux publication runs after Windows publication succeeds. Packaged amd64 binaries pass a real connection test before upload; arm64 is cross-compiled. This applies to future tags and does not automatically add assets to existing releases.

## Install the service

Place `install.sh` and the `tunnel-server` binary for the target architecture
in the same directory, then run:

```bash
chmod +x install.sh
sudo ./install.sh
```

The default tunnel port is `2222`; use `--port 3333` to change it. For an ARM64
build, select the file with `--binary tunnel-server-linux-arm64`. The installer
retains the host key, `authorized_keys` and SQLite data. Every installation writes the current service configuration; pass a custom `--port` on every run.

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

Only when the first installation has no administrator, initialize an administrator in the server terminal:

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

The password is entered privately at the terminal. Custom deployments must use the actual service user and data directory. An existing username has its password reset and gains administrator access; existing administrators do not need reinitialization. Keep the SSH forward open, visit `http://127.0.0.1:2223`, and log in with the administrator account. Refreshing retains the cookie session. Logout, server restart or expiry requires login again. See the [admin login guide](../ADMIN_LOGIN_GUIDE.md).

Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Device registrations in the current database layout are retained, along with existing keys, IDs and tunnels. See the [client access guide](../CLIENT_ACCESS_GUIDE.md).

New devices submit their public key and device ID for administrator approval. Registered devices retain their keys and device IDs. Administrator password changes do not revoke device keys.

Connection, access and management audits are stored in SQLite. Only current data structures are supported; no historical rule migration runs.

Uninstall with:

```bash
sudo ./install.sh --uninstall          # Keep configuration and data
sudo ./install.sh --uninstall --purge  # Delete configuration and data
```

See [DEPLOY.en.md](../DEPLOY.en.md) for the complete guide.


## Reinstallation and data retention

Place the new binary beside `install.sh` and use the same installation command, including the desired port on every run:

```bash
sudo bash install.sh --binary tunnel-server --port 3333
```

The installer validates the executable and authorization file, stops the service, backs up the complete data/config directories, program and service configuration under `/var/backups/tunnel-server/<timestamp>-<PID>/`, then atomically replaces the program and writes the current service definition. Running and stopped services follow the same flow. No old arguments or historical rules are converted. Custom deployments should replace the binary directly while keeping their actual data and key paths.

Retain `authorized_keys`, the complete `/var/lib/tunnel-server` database directory, and `/etc/tunnel-server/host_key`. These preserve public keys, verified device ID bindings, authorization state, administrators, audits and server identity. Clients retain their configuration, private keys and `known_hosts`. No device re-enrollment or administrator initialization is needed when reinstalling.

An existing database without its host key or authorization file stops installation. `--uninstall` retains data; adding `--purge` deletes the default config and data directories. After an installation failure, stop the service and recover using the backup's `data`, `config`, `tunnel-server` and service configuration.

Installation interrupts business connections; running clients reconnect according to their retry policy. The service uses current defaults: `LimitNOFILE=4096`, `TasksMax=256`, `MemoryHigh=768M`, `MemoryMax=1G`. Administrators maintain custom systemd drop-ins. The installer does not change firewall rules.

## Desktop updates

The desktop client retains update checks, downloads and user-confirmed installation. Updates reuse the configuration, keys and device ID in `userData`. The server does not provide minimum-client-version policies or historical rule upgrades.
