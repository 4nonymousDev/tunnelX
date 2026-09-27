# TunnelX

[English](README.en.md)

通过一台公网服务器，把内网服务安全地暴露给授权的远程机器。

基于 SSH 的端口转发工具，用于远程开发调试——把办公室的服务映射出去，
在家里或出差时像访问本地服务一样访问它。

```
   办公室 PC                    公网服务器                     笔记本
┌──────────────┐            ┌─────────────┐            ┌──────────────┐
│ nginx :80    │            │             │            │ 浏览器访问    │
│      ▲       │            │             │            │ localhost:80 │
│      │       │            │             │            │      │       │
│  TunnelX  ═══╪═══ SSH ═══▶│ 身份校验转发 ┼═══ SSH ═══╪══ TunnelX    │
│   [导出]     │            │ 无中转端口   │            │   [导入]      │
└──────────────┘            └─────────────┘            └──────────────┘
```

## 特点

- 客户端核心不依赖 UI，可由桌面界面、CLI 或后续 WebUI 使用。
- Windows 桌面端采用 Electron + Vue，通过本地 API 连接独立 Go 核心。
- 客户端、CLI 和服务端均可构建为单文件，不依赖系统 OpenSSH。
- 服务端单二进制内嵌管理 REST API 和 Vue 管理后台，历史与审计持久化到 SQLite。
- 导入侧绑定已核验的设备、公钥与隧道 ID，每次连接核对当前会话和发布版本。
- 服务端不开放 shell，不接受任意服务器地址或端口转发。

已有实例请先阅读 [安全升级操作说明](deploy/README.md#已有服务升级与恢复)。客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。原密钥、ID 和隧道配置保留；账号登记的有效设备自动迁移为独立授权，受限设备不会自动恢复。详见[客户端接入指南](CLIENT_ACCESS_GUIDE.md)。

## 快速开始

### 1. 部署服务端

需要一台有公网 IP 的 Linux 服务器（需 systemd）。

GitHub 标签发布流程会生成 Linux amd64 / arm64 包，包含 CLI、server 和部署脚本。可直接下载对应架构的发布包，按 [Linux 发布包说明](deploy/README.md#下载-linux-发布包) 安装；以下是自行编译的方法。

```bash
# 先构建内嵌管理后台
cd admin-web && npm ci && npm run typecheck && npm run build && cd ..

# 在开发机上交叉编译（无需 CGO）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-amd64 ./cmd/tunnel-server

# 上传并一键安装
scp deploy/install.sh deploy/upgrade.sh tunnel-server-linux-amd64 user@your-server:~/
ssh user@your-server 'chmod +x install.sh && sudo ./install.sh'
```

安装脚本会创建服务账户、生成主机密钥、配置 systemd 常驻与开机自启。
详见 [deploy/README.md](deploy/README.md)。

别忘了放行端口（默认 2222），云服务器还需在控制台的安全组中放行。
管理端口 2223 不应加入防火墙或安全组；安装后通过系统 SSH 转发访问：

```bash
ssh -L 2223:127.0.0.1:2223 user@your-server
# 浏览 http://127.0.0.1:2223
```

### 2. 准备设备公钥

在桌面端「连接设置」填写服务器地址，保留原密钥；新设备点击「生成」。复制公钥与设置中的设备 ID 交给管理员。

### 3. 管理员登记设备

客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。原密钥、ID 和隧道配置保留；账号登记的有效设备自动迁移为独立授权，受限设备不会自动恢复。详见[客户端接入指南](CLIENT_ACCESS_GUIDE.md)。

登记后保存设置并连接，首次连接时核对服务器指纹。私钥只留在本机；后台密码不提供给客户端。

### 4. 建立隧道

**导出端**（有服务要分享的机器）：在「导出」页签点「添加」，
填本地地址和端口，如 `127.0.0.1:80`。

**导入端**（要访问服务的机器）：在「导入」页签点「从在线列表添加」，
从列表里点选对端即可。

之后浏览器访问 `http://localhost:<你设的本地端口>` 就能到达对端的服务。

## 配置文件

桌面端配置默认保存在 Electron `userData/config.json`，CLI 使用指定的配置文件。
通常不需要手工编辑，但批量部署时直接分发配置文件更方便。

客户端配置 schema 保持为 2；服务端数据库升级为 schema 7。迁移保留原身份、密钥和隧道，升级前先备份，见 [安全升级操作说明](deploy/README.md#已有服务升级与恢复)。

完整示例见 [config.example.json](config.example.json)，含每个字段的说明。

### 连接配置

| 字段 | 说明 |
|---|---|
| `id` | 本机标识。**留空则首次启动自动生成**，此后永不改变。其他机器接入本机导出的端口时，`peer_id` 填的就是它。**切勿在多台机器间复制同一个 id**——服务端会拒绝后连的那台 |
| `name` | 显示名，留空则取计算机名。仅用于显示，可随时改 |
| `server_addr` | `主机:端口`。端口是 tunnel-server 的（默认 2222），不是系统 sshd 的 22 |
| `key_path` | 私钥路径。相对路径以配置文件所在目录为基准 |

旧配置中的 `server_user` 会被忽略，连接始终使用 TunnelX 内部协议值。

### 导出隧道（把本机端口分享出去）

```json
{
  "kind": "export",
  "name": "nginx",
  "enabled": true,
  "local_host": "127.0.0.1",
  "local_port": 80
}
```

`local_host` **不限于本机**——填内网其他设备的地址，即可把 NAS、打印机、
路由器管理页一并导出。

每条导出隧道由自己的 `id` 唯一标识，因此同一客户端可以同时导出
`127.0.0.1:80`、`192.168.1.50:80` 等不同地址上的相同端口。`id` 留空时
客户端会自动生成；手工填写时必须保证每条隧道都不同。

服务端直接转发到核验后的出口 SSH 连接，不再分配中转端口。

### 导入隧道（接入他人分享的端口）

```json
{
  "kind": "import",
  "name": "接入办公室 nginx",
  "enabled": true,
  "peer_id": "对端的 id 值",
  "peer_tunnel_id": "对端导出隧道的 id 值",
  "peer_name": "办公室PC",
  "peer_src_port": 80,
  "listen_port": 8080
}
```

| 字段 | 说明 |
|---|---|
| `peer_id` | 对端 `config.json` 里的 `id`。填错与对端离线的症状相同，但日志会列出注册表中实际可用的值供对照 |
| `peer_tunnel_id` | 对端导出隧道的 `id`。与 `peer_id` 一起精确定位服务，因此可区分不同地址上的相同端口。建议通过「从在线列表添加」自动填写 |
| `peer_name` | 仅供显示，对端改名不影响接入——匹配走的是 `peer_id + peer_tunnel_id` |
| `peer_src_port` | 对端的 `local_port`，用于显示、默认本地端口，以及兼容没有 `peer_tunnel_id` 的旧配置；**不是服务端分配的端口** |
| `listen_port` | 本机监听端口。可与 `peer_src_port` 不同（本机该端口可能已被占用）。Windows 上绑定 1024 以下端口需管理员权限 |

> 手填 `peer_id` 容易出错，建议用界面的「从在线列表添加」自动填写。

`enabled` 为 `false` 表示暂时停用：配置保留，但不建立隧道。

旧导入配置没有 `peer_tunnel_id` 时，只在可信注册表中的 `peer_id + peer_src_port`
唯一匹配后补齐目标；存在多个匹配时拒绝猜测。v1 程序需要升级到 v2。

## Linux CLI 客户端

`tunnelx-cli` 是无 CGO 的通用 Linux 客户端，不依赖 Debian 或 systemd。
同一架构的静态 ELF 可用于 Debian、Ubuntu、RHEL、Fedora、Arch、Alpine 等主流发行版。

### 公钥接入

新设备生成专用密钥后，将 `.pub` 文件和 `config.json` 中的 `id` 交给管理员登记。已授权设备使用原配置直接运行核心；不需要账号密码。

### 生成设备密钥

CLI 可以直接生成隧道专用密钥，不依赖系统 `ssh-keygen`：

```bash
./tunnelx-cli keygen \
  --username alice \
  --email alice@example.com \
  --output tunnel_key
```

`--output` 默认为当前目录的 `tunnel_key`。命令不会覆盖已有的私钥或 `.pub`；
主动填写的用户名、邮箱会以明文 JSON 写入 `.pub` 注释（均可留空，不自动读取计算机名），之后可在
服务端管理页面直接导入。

### 便携运行

配置服务器地址，保留已有 `config.json` 与私钥；新设备先生成密钥并交给管理员登记：

```bash
chmod +x tunnelx-cli
./tunnelx-cli run
```

`run` 在前台运行核心，`Ctrl-C` 停止。首次连接会显示服务器主机指纹，核对后输入确认。

首次确认完成后，如果不创建 systemd 服务，也可以用 `nohup` 转入后台运行，
并在终端关闭后继续保持隧道：

```bash
nohup ./tunnelx-cli run > /dev/null 2>&1 < /dev/null &
echo $! > tunnelx-cli.pid
```

标准输出和错误输出被丢弃，但 TunnelX 仍会把内部日志写入配置目录的
`tunnelx.log`。可用 `tail -f tunnelx.log` 查看日志；停止进程时执行
`kill "$(cat tunnelx-cli.pid)"`。这种方式不会在系统重启后自动启动，也不会在
进程崩溃后自动重启，需要这些能力时应使用 systemd。

### 系统服务运行

配置和运行状态可以分开放置：

```bash
./tunnelx-cli run \
  --config /etc/tunnelx/config.json \
  --state-dir /var/lib/tunnelx
```

`key_path` 的相对路径以配置文件目录为基准。`known_hosts`、日志和本地控制凭据写入
`--state-dir`；未指定时写入配置文件目录。

无人值守运行不能交互确认指纹，需先通过独立渠道核对服务器指纹：

```bash
./tunnelx-cli run \
  --config /etc/tunnelx/config.json \
  --state-dir /var/lib/tunnelx \
  --accept-host-key 'SHA256:已核对的指纹'
```

在另一个终端中使用相同的 `--config` 和 `--state-dir` 控制该实例：

```bash
./tunnelx-cli status --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli tunnels --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli logs --follow --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli disconnect --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
./tunnelx-cli connect --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
```

`tunnels` 会显示每条导出隧道的 `id`；Linux 导入端手工配置时，可将它填入
`peer_tunnel_id`。桌面端使用「从在线列表添加」时会自动填写。

出现待确认操作时，可先从命令输出取得确认编号，再执行：

```bash
./tunnelx-cli confirm --id 1 --accept \
  --config /etc/tunnelx/config.json --state-dir /var/lib/tunnelx
```

## 架构

```text
Electron + Vue 桌面端 ─┐
                      ├─> 本地 API v1 ─> core.Service ─> manager ─> SSH ─> tunnel-server
tunnelx-cli 控制命令 ─┘          ▲
                                  │
                         tunnelx-cli run
```

- `core.Service` 管理配置、连接生命周期、隧道、日志、事件和确认请求。
- Electron 主进程负责启动或附加核心、托盘与单实例；Vue 渲染进程只能调用白名单 IPC。
- `tunnelx-cli run` 承载无 UI 核心，并在随机的 `127.0.0.1` 端口提供本地 API。
- CLI 控制命令和桌面端复用同一接口，界面退出不会中断核心。
- API 地址和随机令牌保存在状态目录的 `.tunnelx-control.json` 中；Unix 权限为 `0600`。
- Electron 开发、打包与路径覆盖说明见 [`desktop/README.md`](desktop/README.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [BUILD.md](BUILD.md) | 构建说明、目录结构、测试、排查问题 |
| [DEPLOY.md](DEPLOY.md) | 完整部署指南、密钥规划、防火墙、审计日志 |
| [deploy/README.md](deploy/README.md) | 一键安装脚本用法 |
| [CLIENT_ACCESS_GUIDE.md](CLIENT_ACCESS_GUIDE.md) | 公钥登记、设备迁移与原数据保留 |

## 与同类工具的区别

| | TunnelX | frp / ngrok | Dev Tunnels |
|---|---|---|---|
| 传输 | SSH | 自定义协议 / HTTP | HTTPS |
| 认证 | 设备公钥 + 设备 ID 绑定 | Token | 微软账号 |
| 服务端 | 自建 | 自建 / 官方 | 官方托管 |
| 客户端 | 无 UI 核心 + 桌面/CLI 前端 | 命令行 + 配置文件 | VSCode 插件 / CLI |
| 依赖 | 无 | 无 | VSCode / .NET |

SSH 传输加密和公钥验证由 `golang.org/x/crypto/ssh` 提供。客户端仅使用公钥认证，账号密码仅用于管理后台。

## 系统要求

| | 客户端 | 服务端 |
|---|---|---|
| 系统 | Windows 10/11 桌面；主流 Linux amd64/arm64 CLI | 任意 Linux（systemd） |
| 依赖 | 无 | 无 |
| 体积 | 桌面端约 26MB；CLI 约 7-8MB | 约 4.7MB |

> **已知问题**：Windows Server 2019 上托盘图标正常但主窗口不显示，原因待查。
> Windows 10/11 正常。

## 安全说明

- 服务端按设备、公钥、隧道 ID、会话和发布代际精确转发，不开中转端口或 shell
- 拒绝任意服务器地址及旧式 TCP 端口转发
- SSH 不接受密码认证；设备公钥必须获得管理员授权并绑定设备 ID，迁移设备保留原接入限制
- 管理 API 只允许监听明确的 loopback 地址，要求管理员账号登录；HttpOnly 会话 Cookie 保持登录，写操作校验来源和 CSRF，旧 Bearer Token 无效
- 客户端在线状态只在内存；资料、黑名单和新审计写入纯 Go SQLite
- 客户端会检查私钥权限：Windows 使用 ACL，Unix 使用文件权限位
- 首次连接服务器时展示主机指纹供核对，之后严格校验（防中间人）

这些边界由 `internal/server/security_test.go` 实测守护——若将来有人为调试
放宽限制，测试会立即失败。

## 状态

已在 Windows 11 与 Linux 服务端之间实机跑通完整链路。核心功能可用，
细节仍在打磨。欢迎提 issue。
