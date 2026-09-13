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
│  TunnelX  ═══╪═══ SSH ═══▶│  :41273 ◄───┼═══ SSH ═══╪══ TunnelX    │
│   [导出]     │            │ （仅环回）   │            │   [导入]      │
└──────────────┘            └─────────────┘            └──────────────┘
```

## 特点

- 客户端核心不依赖 UI，可由桌面界面、CLI 或后续 WebUI 使用。
- Windows 桌面端采用 Electron + Vue，通过本地 API 连接独立 Go 核心。
- 客户端、CLI 和服务端均可构建为单文件，不依赖系统 OpenSSH。
- 服务端单二进制内嵌管理 REST API 和 Vue 管理后台，历史与审计持久化到 SQLite。
- 导入侧按机器标识和源端口解析隧道，不依赖服务端动态分配的端口。
- 服务端不开放 shell，转发目标限制为环回地址。

## 快速开始

### 1. 部署服务端

需要一台有公网 IP 的 Linux 服务器（需 systemd）。

```bash
# 先构建内嵌管理后台
cd admin-web && npm ci && npm run typecheck && npm run build && cd ..

# 在开发机上交叉编译（无需 CGO）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-amd64 ./cmd/tunnel-server

# 上传并一键安装
scp deploy/install.sh tunnel-server-linux-amd64 user@your-server:~/
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

### 2. 生成并登记密钥

**生成**：双击运行 `tunnelx.exe`，点「设置」，在「私钥」一行点「生成…」。
密钥会生成在程序所在目录（默认名 `tunnel_key`），弹窗里点「复制公钥」即可。
不需要装 OpenSSH，也不用敲命令。

**登记**：把复制到的那行公钥追加到服务端的 authorized_keys：

```bash
ssh user@your-server \
  'sudo tee -a /etc/tunnel-server/authorized_keys' # 粘贴公钥后回车，Ctrl-D 结束
```

约 2 秒生效，无需重启服务。

<details>
<summary>也可以用 ssh-keygen 生成（批量部署、服务端操作时更顺手）</summary>

```bash
# 在你的机器上生成隧道专用密钥
ssh-keygen -t ed25519 -f tunnel_key -N ""

# 把公钥登记到服务端（约 2 秒生效，无需重启服务）
cat tunnel_key.pub | ssh user@your-server \
  'sudo tee -a /etc/tunnel-server/authorized_keys > /dev/null'
```

生成的密钥与界面生成的完全等价——同为无密码短语的 ed25519。

</details>

> **不要复用服务器的登录密钥。** 那把密钥能开 root shell，分发到各台客户端
> 等于把服务器权限一并交出去，且某台泄露时无法单独撤销。

### 3. 配置客户端

回到「设置」窗口填写其余字段（上一步界面生成的密钥已自动填好私钥栏）：

| 字段 | 示例 |
|---|---|
| 服务器 | `your-server.com:2222` |
| 用户名 | 任意（服务端只认公钥，不校验用户名） |
| 私钥 | `tunnel_key` |

用 `ssh-keygen` 生成的密钥，把 `tunnel_key` 与 `tunnelx.exe` 放在同一目录，
私钥栏填文件名即可；也可以点「浏览…」选择。

### 4. 建立隧道

**导出端**（有服务要分享的机器）：在「导出」页签点「添加」，
填本地地址和端口，如 `127.0.0.1:80`。

**导入端**（要访问服务的机器）：在「导入」页签点「从在线列表添加」，
从列表里点选对端即可。

之后浏览器访问 `http://localhost:<你设的本地端口>` 就能到达对端的服务。

## 配置文件

界面上的所有配置都存在 `tunnelx.exe` 同目录的 `config.json` 里。
通常不需要手工编辑，但批量部署时直接分发配置文件更方便。

完整示例见 [config.example.json](config.example.json)，含每个字段的说明。

### 连接配置

| 字段 | 说明 |
|---|---|
| `id` | 本机标识。**留空则首次启动自动生成**，此后永不改变。其他机器接入本机导出的端口时，`peer_id` 填的就是它。**切勿在多台机器间复制同一个 id**——服务端会拒绝后连的那台 |
| `name` | 显示名，留空则取计算机名。仅用于显示，可随时改 |
| `server_addr` | `主机:端口`。端口是 tunnel-server 的（默认 2222），不是系统 sshd 的 22 |
| `server_user` | 任意值。服务端只校验公钥，但日志会记录它 |
| `key_path` | 私钥路径。相对路径以配置文件所在目录为基准 |

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

服务端端口由服务器自动分配，**无需也无法指定**。

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

同一 Exporter 导出多个相同端口时，`tunnel-server`、导出客户端和导入客户端都需
升级到支持 `tunnel_id` 的版本。旧导入配置没有 `peer_tunnel_id` 时仍按
`peer_id + peer_src_port` 匹配，保持兼容，但无法区分同一对端的两个相同端口。

## Linux CLI 客户端

`tunnelx-cli` 是无 CGO 的通用 Linux 客户端，不依赖 Debian 或 systemd。
同一架构的静态 ELF 可用于 Debian、Ubuntu、RHEL、Fedora、Arch、Alpine 等主流发行版。

### 便携运行

把 `tunnelx-cli`、`config.json` 和 `tunnel_key` 放在同一目录：

```bash
chmod +x tunnelx-cli
./tunnelx-cli run
```

`run` 在前台运行核心，`Ctrl-C` 停止。首次连接会显示服务器主机指纹，核对后输入确认。

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

## 与同类工具的区别

| | TunnelX | frp / ngrok | Dev Tunnels |
|---|---|---|---|
| 传输 | SSH | 自定义协议 / HTTP | HTTPS |
| 认证 | SSH 公钥 | Token | 微软账号 |
| 服务端 | 自建 | 自建 / 官方 | 官方托管 |
| 客户端 | 无 UI 核心 + 桌面/CLI 前端 | 命令行 + 配置文件 | VSCode 插件 / CLI |
| 依赖 | 无 | 无 | VSCode / .NET |

选择 SSH 而非自定义协议的原因：认证与加密由 `golang.org/x/crypto/ssh` 提供，
**不自写一行认证代码**——那是最容易出漏洞的地方。详见下方「安全说明」。

## 系统要求

| | 客户端 | 服务端 |
|---|---|---|
| 系统 | Windows 10/11 桌面；主流 Linux amd64/arm64 CLI | 任意 Linux（systemd） |
| 依赖 | 无 | 无 |
| 体积 | 桌面端约 26MB；CLI 约 7-8MB | 约 4.7MB |

> **已知问题**：Windows Server 2019 上托盘图标正常但主窗口不显示，原因待查。
> Windows 10/11 正常。

## 安全说明

- 服务端转发端口**硬编码绑定 `127.0.0.1`**，内网服务不会直接暴露在公网
- Importer 的转发目标同样限制为环回地址，服务端不能被用作跳板
- 服务端只接受公钥认证，不开放密码认证
- 管理 API 只允许监听明确的 loopback 地址，并要求 Bearer Token
- 客户端在线状态只在内存；资料、黑名单和新审计写入纯 Go SQLite
- 客户端会检查私钥权限：Windows 使用 ACL，Unix 使用文件权限位
- 首次连接服务器时展示主机指纹供核对，之后严格校验（防中间人）

这些边界由 `internal/server/security_test.go` 实测守护——若将来有人为调试
放宽限制，测试会立即失败。

## 状态

已在 Windows 11 与 Linux 服务端之间实机跑通完整链路。核心功能可用，
细节仍在打磨。欢迎提 issue。
