# TunnelX Desktop

[English](README.en.md)

基于 Electron、Vue 3 与 TypeScript 的 TunnelX 桌面界面。Electron 主进程只通过本机鉴权 API 与 `tunnelx-cli` 核心通信；渲染进程不包含 Node.js 能力，也无法读取控制令牌。

## 架构

- `src/main`：窗口、托盘、单实例、核心进程发现/启动、本地 API 与 NDJSON 事件流。
- `src/preload`：启用 context isolation 与 sandbox 后暴露的最小类型化 IPC 接口。
- `src/shared`：核心 DTO 与 IPC 契约。
- `src/renderer`：Vue 3 Composition API 界面。

关闭窗口只会隐藏到托盘，核心继续运行；从托盘选择“退出 TunnelX”会先停止核心并等待进程退出。界面重新启动时会通过 `.tunnelx-control.json` 附加仍在运行的核心。

## 开发

安装一次依赖后直接启动开发环境：

```powershell
Set-Location desktop
npm install
npm run dev
```

开发脚本会自动编译仓库根目录的 Go 核心和 Electron 主进程，再启动固定在 `127.0.0.1:5173` 的 Vite 服务与 Electron，无需制作安装包。Vue 页面支持热更新；Go 核心日志也会同步输出到当前 PowerShell 窗口。修改 Go 核心、Electron 主进程或 preload 后只需从托盘退出，再重新运行 `npm run dev`。

运行日志页提供“复制诊断信息”，内容包括原始服务端注册表、UI 过滤原因、连接状态、隧道状态和最近 100 条日志，不包含本地控制令牌。反馈问题时直接粘贴该 JSON 即可。

> 不要单独运行 `npm run dev:renderer` 后在普通浏览器打开页面。浏览器中没有
> Electron preload 注入的 `window.tunnelx` 通信桥，因此无法读取或保存核心配置。

## 验证与构建

```powershell
npm run typecheck
npm run build
npm run package
```

`npm run package` 使用 electron-builder，并将仓库根目录的 `tunnelx-cli.exe` 放进安装包的 `resources/core/`。

## 路径覆盖

默认配置写入 Electron 的 `userData/config.json`，运行状态写入 `userData/state/`。以下环境变量可覆盖默认值：

- `TUNNELX_CORE_PATH`：`tunnelx-cli.exe` 的绝对或相对路径。
- `TUNNELX_CONFIG_PATH`：核心配置文件路径。
- `TUNNELX_STATE_DIR`：known_hosts、日志与控制文件目录。
- `TUNNELX_ENDPOINT_PATH`：控制端点文件路径。

开发模式默认从仓库根目录读取 `tunnelx-cli.exe`；打包模式默认读取 `resources/core/tunnelx-cli.exe`。
