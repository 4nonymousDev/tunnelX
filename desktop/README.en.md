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

The log page provides “Copy diagnostics,” containing the raw server registry,
UI filtering reasons, connection state, tunnel states, and the latest 100 log
entries. It does not include the local control token. Paste this JSON directly
when reporting a problem.

> Do not run only `npm run dev:renderer` and open the page in a regular browser.
> A browser does not have the `window.tunnelx` bridge injected by Electron's
> preload, so it cannot read or save the core configuration.

## Verification and build

```powershell
npm run typecheck
npm run build
npm run package
```

`npm run package` uses electron-builder and places `tunnelx-cli.exe` from the
repository root in the package's `resources/core/` directory.

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
