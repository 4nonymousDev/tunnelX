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

运行日志页提供“复制诊断信息”，内容包括服务端注册表、UI 过滤原因、连接状态、隧道状态和最近 100 条日志，不包含本地控制令牌，也不会直接复制本机 ID、名称、服务器地址或私钥路径。日志内容仍可能包含运行时地址，发布到公开 Issue 前请先检查。

> 不要单独运行 `npm run dev:renderer` 后在普通浏览器打开页面。浏览器中没有
> Electron preload 注入的 `window.tunnelx` 通信桥，因此无法读取或保存核心配置。

## 验证与构建

```powershell
npm run typecheck
npm run build
npm run package
```

`npm run package` 使用 electron-builder，同时生成 NSIS 安装版和 ZIP 便携版，并将仓库根目录的 `tunnelx-cli.exe` 放进两种产物的 `resources/core/`。NSIS 安装向导允许用户选择安装目录；便携版解压后可直接运行 `TunnelX.exe`，不要在压缩包内直接启动。

## 自动更新与版本

安装版启动 15 秒后检查 GitHub Releases，此后每 4 小时检查一次。发现新版时只在顶栏显示提示，不会自动下载；用户可以点击提示，或在“设置 → 应用版本”中手动检查。确认更新后界面显示下载进度，下载完成会先安全停止核心，再交给 NSIS 安装程序替换文件并重启。开发模式不会访问更新服务。

GUI 与 CLI 使用独立的 [SemVer](https://semver.org/lang/zh-CN/) 版本号：

- GUI 版本来自 `desktop/package.json`，GitHub Release 标签必须是 `v<GUI版本>`。
- CLI 版本来自仓库根目录 `CLI_VERSION`，发布构建会把它注入 `tunnelx-cli.exe`。
- 只修改 GUI 时只提升 GUI 版本；修改 CLI 时提升 CLI 版本，并至少提升 GUI 的补丁版本，因为桌面安装包是 CLI 更新的载体。

仓库中的 `.github/workflows/release-desktop.yml` 负责在干净的 Windows runner 中测试、构建并发布未签名的 NSIS 安装版和 ZIP 便携版。创建标签前先同步版本，然后推送标签：

```powershell
# 示例：GUI 0.1.1，CLI 仍为 0.1.0
npm --prefix desktop version 0.1.1 --no-git-tag-version
git add desktop/package.json desktop/package-lock.json
git commit -m "release: GUI 0.1.1"
git tag v0.1.1
git push origin HEAD --tags
```

工作流只使用 GitHub 自动提供的 `GITHUB_TOKEN`，当前未配置 Windows 代码签名。首次使用时确认仓库 `Settings → Actions → General → Workflow permissions` 允许工作流写入 Releases。未签名安装程序可能触发 Windows SmartScreen 或“未知发布者”提示。

安装版可使用应用内自动更新。ZIP 便携版若要继续保持免安装方式，请手动下载新版 ZIP，退出旧版本后解压到新的空目录；应用内确认更新会启动 NSIS 安装程序并转为安装版。两种版本的用户配置、SSH 密钥、known_hosts 和日志都保存在 Electron `userData` 目录中，不在程序目录内，不随发布包上传，也不会因为替换程序文件而删除。不要修改 `appId`、`productName` 或默认 `userData` 路径；这些变更必须配套数据迁移。

## 路径覆盖

默认配置写入 Electron 的 `userData/config.json`，运行状态写入 `userData/state/`。以下环境变量可覆盖默认值：

- `TUNNELX_CORE_PATH`：`tunnelx-cli.exe` 的绝对或相对路径。
- `TUNNELX_CONFIG_PATH`：核心配置文件路径。
- `TUNNELX_STATE_DIR`：known_hosts、日志与控制文件目录。
- `TUNNELX_ENDPOINT_PATH`：控制端点文件路径。

开发模式默认从仓库根目录读取 `tunnelx-cli.exe`；打包模式默认读取 `resources/core/tunnelx-cli.exe`。
