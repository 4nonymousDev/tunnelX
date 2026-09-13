# TunnelX — 构建与开发

[English](BUILD.en.md)

项目介绍见 [README.md](README.md)。

## 构建前置

| 依赖 | 用途 | 验证过的版本 |
|---|---|---|
| Go 1.22+ | 编译 | go1.26.5 |
| Node.js + npm | 构建 Electron/Vue 桌面端和内嵌管理后台 | Node.js 22+ |

全项目无需 CGO，**`CGO_ENABLED=0` 即可编译**，可从任意平台交叉编译。

验证工具链：

```bash
go version     # go1.22 或更高
```

## 构建

```bash
go mod download
go build ./...      # 编译检查
go test ./...       # 运行测试
go vet ./...        # 静态检查
```

### 新客户端（Windows Electron GUI）

先在仓库根目录构建独立 Go 核心，再构建桌面壳：

```powershell
go build -ldflags "-s -w -X main.Version=0.1.0" -o tunnelx-cli.exe ./cmd/tunnelx-cli
Set-Location desktop
npm install
npm run build
npm run package
```

`npm run package` 会把 `tunnelx-cli.exe` 作为独立核心放入安装包。Electron 主进程
通过带随机令牌的本地 API 与核心通信，Vue 渲染进程不接触令牌。
更多开发选项见 [`desktop/README.md`](desktop/README.md)。

### 客户端（通用 Linux CLI）

CLI 无需 CGO，可交叉编译为静态 ELF：

```bash
# x86_64：Debian / Ubuntu / RHEL / Fedora / Arch / Alpine 等
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnelx-cli-linux-amd64 ./cmd/tunnelx-cli

# ARM64：ARM 服务器、树莓派等
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnelx-cli-linux-arm64 ./cmd/tunnelx-cli
```

程序本身不依赖 systemd；可直接前台运行，也可由 systemd、OpenRC、runit、s6、
容器编排器或其他进程管理器托管。只读配置与可写状态可分别用 `--config` 和
`--state-dir` 指定。

### 服务端（Linux）

先构建管理后台；产物复制/输出到 Go 的嵌入资源目录后，再编译服务端。发布和 CI
不得跳过这个顺序：

```bash
cd admin-web
npm ci
npm run typecheck
npm run build
cd ..
```

```bash
# x86_64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnel-server-linux-amd64 ./cmd/tunnel-server

# ARM64（树莓派、部分云主机）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w -X main.Version=0.1.0" \
  -o tunnel-server-linux-arm64 ./cmd/tunnel-server
```

最终产物是包含管理 REST API 与 SPA 的单一静态 ELF。运行机器不需要 Node、npm、
外部静态文件或系统 SQLite；纯 Go SQLite 驱动保持 `CGO_ENABLED=0` 的 amd64/arm64
交叉构建能力。

## 目录结构

```
cmd/
  tunnelx-cli/       无 UI 核心宿主 + 本地控制命令
  tunnel-server/     服务端入口
internal/
  proto/             控制协议消息定义与 JSON Lines 编解码
  config/            config.json 读写、本机标识
  logbuf/            环形日志缓冲 + 文件落盘与轮转
  keyperm/           私钥 ACL 检查与 icacls 一键修复
  sshconn/           唯一 SSH 连接、心跳、主机密钥校验
  tunnel/            单条隧道 goroutine、失败分类、退避
  control/           控制通道客户端
  manager/           全局连接循环，驱动上述各层
  core/              UI 无关的应用服务、快照、事件与确认流
  localapi/          带鉴权的版本化本地 JSON/NDJSON API
  registry/          服务端在线注册表
  session/           服务端在线 SSH 会话与转发端口归属
  policy/            指纹黑名单快照与认证 admission gate
  store/             SQLite 客户端资料、黑名单与审计
  adminapi/          管理 REST API、SSE 与内嵌 SPA
  server/            服务端：SSH 接入、转发与生命周期编排
admin-web/            Vue 3 + TypeScript 管理后台源码
desktop/
  src/main/          Electron 主进程、核心托管、本地 API
  src/preload/       隔离的白名单 IPC 桥
  src/renderer/      Vue 3 界面
deploy/
  install.sh         服务端一键安装脚本
```

两层重连的职责划分体现在包边界上：`manager` 负责 SSH 连接本身的断线
重连，`tunnel` 只负责单条转发的成败——隧道不各自重连，那没有意义。

## 测试

```bash
go test ./...                          # 全部
go test ./internal/control/ -v         # 端到端数据通路
go test ./internal/server/ -v          # 服务端与安全边界
```

集成测试用的是**真实服务端而非 mock**——要验证的正是两端协议实现是否一致。

关键用例：

| 用例 | 验证内容 |
|---|---|
| `TestEndToEndTunnel` | 完整数据通路：回声服务 ← Exporter ← 服务端 ← Importer ← 客户端 |
| `TestControlChannelDoesNotBlockForwarding` | 同一连接上控制通道不阻塞转发通道（见下） |
| `TestNoShellAccess` | 隧道密钥无法取得 shell |
| `TestNoArbitraryForwardTarget` | 服务端不能被用作任意目标的跳板 |
| `TestResolveRemotePort` | 按「对端身份 + 源端口」解析 |
| `TestAuthKeysHotReload` | `authorized_keys` 变更即时生效 |

### 几个测试是护栏，不是形式

`sameconn_test.go` 守护的是一个真实发生过的严重缺陷：服务端曾在 channel 循环里
**同步**处理控制通道，而该函数会阻塞至连接关闭，导致同一连接上后续的转发请求
永远排队。症状是"连接建立后挂起、两侧日志均无异常"，极难定位。

此前的测试没能发现，因为它们把控制通道与转发通道放在了**不同的 SSH 连接**上。
真实的 Importer 在同一条连接上同时使用两者——这正是 SSH 多路复用的设计。

`security_test.go` 同理：若将来有人为调试给服务端加上 session 支持，
`TestNoShellAccess` 会立即失败并提示该改动破坏了权限隔离。

## 排查问题

客户端会在 exe 同目录写运行日志：

| 文件 | 内容 |
|---|---|
| `tunnelx.log` | 完整运行日志，含启动时的环境信息（版本、路径、本机标识、隧道配置） |

服务端：

```bash
sudo journalctl -u tunnel-server -f
```
