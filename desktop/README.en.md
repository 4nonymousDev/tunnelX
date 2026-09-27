# TunnelX Desktop

The TunnelX desktop interface is built with Electron, Vue 3, and TypeScript.
The Electron main process communicates with the `tunnelx-cli` core only through
an authenticated local API. The renderer has no Node.js capabilities and
cannot read the control token.

## Public-key access

Clients authenticate with device public keys. Administrators register the key and device ID together under **Device management → Register device**. The console retains administrator password login. Existing keys, IDs and tunnels are preserved; valid account-enrolled devices migrate to independent key authorization, while restricted devices remain restricted. See the [client access guide](../CLIENT_ACCESS_GUIDE.md).

Set the server address in Connection settings, retain the existing private key or generate one for a new device, then send the public key and device ID to the administrator. Verify the server fingerprint on first connection. The local interface lock remains independent; client configuration stays at schema 2.

## Architecture

- `src/main`: window, tray, single-instance handling, core-process discovery
  and startup, local API, and the NDJSON event stream.
- `src/preload`: the minimal typed IPC interface exposed with context isolation
  and sandboxing enabled.
- `src/shared`: core DTOs and IPC contracts.
- `src/renderer`: the Vue 3 Composition API interface.

Closing the window only hides it to the tray, and the core keeps running.
Choosing “Quit TunnelX” from the tray stops the core first and waits for the
process to exit. When the UI starts again, it attaches to a still-running core
through `.tunnelx-control.json`.

## Development

Install dependencies once, then start the development environment directly:

```powershell
Set-Location desktop
npm install
npm run dev
```

The development script automatically builds the Go core in the repository root
and the Electron main process, then launches Vite on the fixed
`127.0.0.1:5173` address and starts Electron. No installer package is needed.
Vue pages support hot reload, and Go core logs are mirrored to the current
PowerShell window. After changing the Go core, Electron main process, or
preload, quit from the tray and run `npm run dev` again.

The log page provides “Copy diagnostics,” containing registry data, UI filtering
reasons, connection state, tunnel states, and the latest 100 log entries. It
does not include the local control token or directly copy the local client ID,
name, server address, or key path. Logs can still contain runtime addresses, so
review the report before posting it to a public issue.

> Do not run only `npm run dev:renderer` and open the page in a regular browser.
> A browser does not have the `window.tunnelx` bridge injected by Electron's
> preload, so it cannot read or save the core configuration.

## Verification and build

```powershell
npm run typecheck
npm run build
npm run test:security
npm run package
```

`npm run package` uses electron-builder to create both an NSIS installer and a
portable ZIP, placing `tunnelx-cli.exe` from the repository root in each
package's `resources/core/` directory. The NSIS setup wizard lets users choose
the installation directory. Extract the portable ZIP before running
`TunnelX.exe`; do not launch it from inside the archive.

## Auto updates and versions

The installed app checks GitHub Releases 15 seconds after startup and every four hours thereafter. A new release only adds a notice to the header; it is never downloaded without confirmation. Users can click that notice or check manually under Settings → Application version. After confirmation, the dialog shows download progress, safely stops the core, and hands control to the NSIS installer to replace files and restart. Development builds never contact the update service.

The GUI and CLI have independent [SemVer](https://semver.org/) versions:

- The GUI version is `desktop/package.json`; a release tag must be `v<GUI version>`.
- The CLI version is the root `CLI_VERSION`, injected into `tunnelx-cli.exe` by the release build.
- GUI-only changes bump only the GUI version. CLI changes bump the CLI version and at least the GUI patch version because the desktop installer carries the CLI update.

`.github/workflows/release-desktop.yml` tests, builds, and publishes a signed NSIS installer and portable ZIP on a clean Windows runner. Update the versions before pushing a tag:

```powershell
npm --prefix desktop version 0.1.1 --no-git-tag-version
git add desktop/package.json desktop/package-lock.json
git commit -m "release: GUI 0.1.1"
git tag v0.1.1
git push origin HEAD --tags
```

Before publishing, configure the `CSC_LINK` certificate and `CSC_KEY_PASSWORD` repository secrets, plus the `TUNNELX_PUBLISHER_NAME` repository variable matching the certificate Subject exactly. The workflow runs `npm run package:signed` and refuses publishing without these inputs. `GITHUB_TOKEN` is used to write Releases. Without a certificate, `npm run package -- --publish never` can create a local unsigned test package; automatic updates are disabled in those builds. Replace the program manually while preserving userData. Existing unsigned releases should also migrate to the first verified signed build through manual installation.

Signed builds pin the publisher in their update configuration. The main process registers an electron-updater verifier requiring Valid Windows Authenticode status and an exact match of the complete certificate Subject. Unavailable PowerShell, parsing failures and invalid signatures reject the update. No certificate is included in this repository. Configuration validation does not constitute real signature validation: the first signed release still needs an actual certificate and verification of its output.

The interface lock gates protected IPC in the main process and accepts calls only from the main frame of the main window. A locked interface cannot reset its password to bypass unlocking; corrupt lock files fail closed. This is interface access control within an OS account, not OS account isolation. Key generation allows empty username/email and never automatically collects the hostname. Any explicitly entered identity metadata is plaintext in the public-key comment.

The installed edition supports in-app updates. To remain installation-free,
portable ZIP users should download each new ZIP manually, exit the old version,
and extract it into a new empty directory. Confirming an in-app update from the
ZIP edition launches the NSIS installer and converts it to the installed
edition. Both editions keep configuration, SSH keys, known_hosts, and logs in
Electron's `userData` directory rather than the program directory, so replacing
program files does not remove them and they are never included in release
artifacts. Changing `appId`, `productName`, or the default `userData` path
requires an explicit data migration.

## Path overrides

By default, configuration is written to Electron's `userData/config.json`, and
runtime state is written to `userData/state/`. These environment variables can
override the defaults:

- `TUNNELX_CORE_PATH`: absolute or relative path to `tunnelx-cli.exe`.
- `TUNNELX_CONFIG_PATH`: path to the core configuration file.
- `TUNNELX_STATE_DIR`: directory for known_hosts, logs, and control files.
- `TUNNELX_ENDPOINT_PATH`: path to the control endpoint file.

Development mode reads `tunnelx-cli.exe` from the repository root by default;
packaged mode reads `resources/core/tunnelx-cli.exe` by default.
