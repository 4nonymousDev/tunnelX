# TunnelX Desktop

The TunnelX desktop interface is built with Electron, Vue 3, and TypeScript.
The Electron main process communicates with the `tunnelx-cli` core only through
an authenticated local API. The renderer has no Node.js capabilities and
cannot read the control token.

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
npm run package
```

`npm run package` uses electron-builder to create both an NSIS installer and a
portable ZIP, placing `tunnelx-cli.exe` from the repository root in each
package's `resources/core/` directory. Extract the portable ZIP before running
`TunnelX.exe`; do not launch it from inside the archive.

## Auto updates and versions

The installed app checks GitHub Releases 15 seconds after startup and every four hours thereafter. A new release only adds a notice to the header; it is never downloaded without confirmation. Users can click that notice or check manually under Settings → Application version. After confirmation, the dialog shows download progress, safely stops the core, and hands control to the NSIS installer to replace files and restart. Development builds never contact the update service.

The GUI and CLI have independent [SemVer](https://semver.org/) versions:

- The GUI version is `desktop/package.json`; a release tag must be `v<GUI version>`.
- The CLI version is the root `CLI_VERSION`, injected into `tunnelx-cli.exe` by the release build.
- GUI-only changes bump only the GUI version. CLI changes bump the CLI version and at least the GUI patch version because the desktop installer carries the CLI update.

`.github/workflows/release-desktop.yml` tests, builds, and publishes the unsigned NSIS installer and portable ZIP on a clean Windows runner. Update the versions before pushing a tag:

```powershell
npm --prefix desktop version 0.1.1 --no-git-tag-version
git add desktop/package.json desktop/package-lock.json
git commit -m "release: GUI 0.1.1"
git tag v0.1.1
git push origin HEAD --tags
```

The workflow uses GitHub's built-in `GITHUB_TOKEN`; no signing secret is currently required. Confirm that Settings → Actions → General → Workflow permissions permits writing Releases. Unsigned installers can trigger SmartScreen or Unknown Publisher warnings.

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
