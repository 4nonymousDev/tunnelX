# TunnelX Desktop

[English](README.en.md)

基于 Electron、Vue 3 与 TypeScript 的 TunnelX 桌面界面。Electron 主进程只通过本机鉴权 API 与 `tunnelx-cli` 核心通信；渲染进程不包含 Node.js 能力，也无法读取控制令牌。

## 公钥接入

客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。原密钥、ID 和隧道配置保留；账号登记的有效设备自动迁移为独立授权，受限设备不会自动恢复。详见[客户端接入指南](../CLIENT_ACCESS_GUIDE.md)。

在连接设置中填写服务器地址，保留原私钥；新设备点击“生成”，复制公钥与设备 ID 交给管理员登记。首次连接核对服务器指纹。本机界面锁保持独立，客户端配置仍为 schema 2。

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
npm run test:security
npm run package
```

`npm run package` 使用 electron-builder，同时生成 NSIS 安装版和 ZIP 便携版，并将仓库根目录的 `tunnelx-cli.exe` 放进两种产物的 `resources/core/`。NSIS 安装向导允许用户选择安装目录；便携版解压后可直接运行 `TunnelX.exe`，不要在压缩包内直接启动。

## 自动更新与版本

安装版启动 15 秒后检查 GitHub Releases，此后每 4 小时检查一次。发现新版时只在顶栏显示提示，不会自动下载；用户可以点击提示，或在“设置 → 应用版本”中手动检查。确认更新后界面显示下载进度，下载完成会先安全停止核心，再交给 NSIS 安装程序替换文件并重启。开发模式不会访问更新服务。

从 0.2.1 起，客户端也会显示 server 管理员设置的必要更新及截止时间：默认有 7 天宽限期，期限内可以关闭提醒继续使用；到期由 server 停止低版本接入并断开隧道，设置和原数据仍可查看。安装新版后沿用原密钥、配置和数据重新连接。后台撤销或延期后，可点击“连接”重试。普通可选更新的行为保持不变。详情见[更新策略](../deploy/README.md#可选更新与强制更新)。

必要更新不会强制下载或静默安装。无法自动更新时，向管理员获取可信的新安装包并保留原 userData。最低版本按 CLI 核心判断，不能仅更新 GUI 而保留不达标的外部 `TUNNELX_CORE_PATH` 核心。

GUI 与 CLI 使用独立的 [SemVer](https://semver.org/lang/zh-CN/) 版本号：

- GUI 版本来自 `desktop/package.json`，GitHub Release 标签必须是 `v<GUI版本>`。
- CLI 版本来自仓库根目录 `CLI_VERSION`，发布构建会把它注入 `tunnelx-cli.exe`。
- 只修改 GUI 时只提升 GUI 版本；修改 CLI 时提升 CLI 版本，并至少提升 GUI 的补丁版本，因为桌面安装包是 CLI 更新的载体。

仓库中的 `.github/workflows/release-desktop.yml` 先执行 Linux 实际连接测试，再在干净的 Windows runner 中测试、构建并发布 NSIS 安装版和 ZIP 便携版。默认发布未签名包；从 GUI 0.2.4 起支持应用内检查、下载和安装更新。创建标签前先同步版本、编写 `.github/release-notes/v<版本>.md`，然后推送标签：

```powershell
# 示例：GUI 0.1.1，CLI 仍为 0.1.0
npm --prefix desktop version 0.1.1 --no-git-tag-version
git add desktop/package.json desktop/package-lock.json
git commit -m "release: GUI 0.1.1"
git tag v0.1.1
git push origin HEAD --tags
```

当前无证书时，工作流使用 `npm run package -- --publish always` 发布未签名包，不要求 Authenticode 签名。更新从本仓库 GitHub Releases 经 HTTPS 获取，electron-updater 校验发布元数据中的 SHA-512；哈希校验用于检查下载完整性，不等同于发布者签名。用户确认后下载并启动安装程序。**0.2.2 / 0.2.3 的客户端内置了更新禁用逻辑，必须手工覆盖安装 0.2.4 一次**，保留原 userData，此后可直接在应用内更新。

以后启用签名时，在仓库 Variables 中设置 `TUNNELX_SIGN_WINDOWS=true` 和与证书 Subject 完全一致的 `TUNNELX_PUBLISHER_NAME`，并在 Secrets 中设置 `CSC_LINK`、`CSC_KEY_PASSWORD`。启用签名后，工作流通过 `npm run package:signed` 强制签名并开启发布者校验；缺少配置会停止发布。`GITHUB_TOKEN` 用于写入 Releases。

签名版将发布者固定在更新配置中，主进程给 electron-updater 注册的验证器要求 Windows Authenticode 状态为 Valid 且证书完整 Subject 完全匹配；PowerShell 不可用、解析失败或签名无效都会拒绝更新。此仓库不包含证书；配置检查不等于已完成真实签名验证，首次正式发布仍需使用实际证书构建并验证签名。

界面锁在主进程拒绝受保护的 IPC，并且只有主窗口的主 frame 可调用接口；锁定后不能通过重新设密码绕过解锁。锁文件损坏会拒绝启动解锁流程。它仍是同一系统账户内的界面访问控制，不能替代操作系统账户隔离。生成公钥时用户名、邮箱均可留空，默认不采集计算机名；填写的描述信息会以明文写入公钥注释。

安装版可使用应用内自动更新。ZIP 便携版若要继续保持免安装方式，请手动下载新版 ZIP，退出旧版本后解压到新的空目录；应用内确认更新会启动 NSIS 安装程序并转为安装版。两种版本的用户配置、SSH 密钥、known_hosts 和日志都保存在 Electron `userData` 目录中，不在程序目录内，不随发布包上传，也不会因为替换程序文件而删除。不要修改 `appId`、`productName` 或默认 `userData` 路径；这些变更必须配套数据迁移。

## 路径覆盖

默认配置写入 Electron 的 `userData/config.json`，运行状态写入 `userData/state/`。以下环境变量可覆盖默认值：

- `TUNNELX_CORE_PATH`：`tunnelx-cli.exe` 的绝对或相对路径。
- `TUNNELX_CONFIG_PATH`：核心配置文件路径。
- `TUNNELX_STATE_DIR`：known_hosts、日志与控制文件目录。
- `TUNNELX_ENDPOINT_PATH`：控制端点文件路径。

开发模式默认从仓库根目录读取 `tunnelx-cli.exe`；打包模式默认读取 `resources/core/tunnelx-cli.exe`。
