# tunnel-server 部署（Linux）

[English](DEPLOY.en.md)

服务端是一个无 CGO 的单二进制，包含 SSH 隧道服务、管理 REST API 和管理 SPA。
隧道端口默认 `:2222`；管理端强制只监听 `127.0.0.1:2223`。

已有公网实例按 [安全升级操作说明](UPGRADE_GUIDE.md) 操作。账号登录需要更新 server 与客户端，保留原密钥、地址和配置；尚未纳入账号的已授权 v2 客户端仍可使用原手工授权规则。

## 构建

管理页面必须先构建，再编译 Go 嵌入资源：

```bash
cd admin-web
npm ci
npm run typecheck
npm run build
cd ..

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-amd64 ./cmd/tunnel-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w" -o tunnel-server-linux-arm64 ./cmd/tunnel-server
```

运行机器不需要 Node、npm、外部网页文件、CGO 或系统 SQLite。

## 一键安装

```bash
scp deploy/install.sh deploy/upgrade.sh tunnel-server-linux-amd64 <user>@<server>:~/
ssh <user>@<server>
chmod +x install.sh
sudo ./install.sh
```

安装脚本创建 `tunnel` nologin 账户，并准备：

- `/etc/tunnel-server/host_key`：只读主机私钥，目录 `0700`、文件 `0600`；
- `/var/lib/tunnel-server/authorized_keys`：可原子更新的授权公钥，文件 `0600`；
- `/var/lib/tunnel-server`，目录 `0700`，用于数据库、WAL 和 SHM；
- 带 `StateDirectory=tunnel-server`、`StateDirectoryMode=0700`、`UMask=0077` 的 systemd 服务。

管理账号保存在数据库中，首次安装需按下文创建。旧 `admin.token` 保留但不再参与认证；新安装不生成 Token。脚本不再
创建 JSONL/logrotate 配置；升级也不会删除旧 `/var/log/tunnel-server/audit.jsonl`。

## systemd 关键配置

```ini
[Service]
User=tunnel
Group=tunnel
StateDirectory=tunnel-server
StateDirectoryMode=0700
UMask=0077
ExecStart=/usr/local/bin/tunnel-server \
  -addr :2222 \
  -hostkey /etc/tunnel-server/host_key \
  -auth /var/lib/tunnel-server/authorized_keys \
  -admin-addr 127.0.0.1:2223 \
  -data-dir /var/lib/tunnel-server
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
RestrictNamespaces=true
ReadOnlyPaths=/etc/tunnel-server
ReadWritePaths=/var/lib/tunnel-server
MemoryHigh=768M
MemoryMax=1G
TasksMax=256
LimitNOFILE=4096
```

旧 `-audit` 参数已退出主流程。在线状态只在内存，客户端资料、黑名单、连接审计、
访问审计和管理操作持久化在 `/var/lib/tunnel-server/tunnel-server.db`。

## 网络与管理访问

只需为 TunnelX SSH 端口（默认 2222）配置必要的防火墙/安全组规则。绝不能开放
2223；管理端也会拒绝非 loopback 监听地址。

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

首次安装或从 Token 升级后，在服务器终端创建管理员（自定义部署替换实际服务用户和数据目录）：

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

密码交互输入，不写在命令行。然后保持上述 SSH 转发，浏览 `http://127.0.0.1:2223`，使用管理员账号密码登录。刷新页面保留登录状态；退出、服务端重启、会话过期或密码重置后需重新登录。普通客户端账号不会自动获得管理权限。找回密码及会话规则见[管理后台登录](ADMIN_LOGIN_GUIDE.md)。

## 账号与客户端接入

在管理后台「账号管理 → 创建账号」设置用户名、密码、设备上限和审计原因。
用户在客户端点击「账号登录」，填写服务器地址、账号和密码，并核对首次服务器指纹；
设备使用原私钥自动登记，缺少私钥时自动生成，无需上传 `.pub` 或填写设备 ID。
默认设备上限为 10，可选 1–50。完整步骤见 [账号密码接入](ACCOUNT_LOGIN_GUIDE.md)。

禁用账号或重置密码会断开该账号的全部会话，并使原设备凭据失效。
重新启用后，设备仍须使用当前密码登录；这不同于普通断线后的自动重连。
客户端不保存密码，既有配置、ID、隧道和服务器信任记录保留。

### 高级：存量手工授权

未纳入账号的已授权 v2 设备可继续使用原公钥和可信身份绑定。
「设备与高级管理 → 高级：存量公钥与身份管理」保留原 `.pub` 与原 ID 的手工登记、换绑和撤销入口。
如需手工管理授权文件，可把隧道专用公钥追加到：

```bash
cat tunnel_key.pub | ssh <user>@<server> \
  'sudo tee -a /var/lib/tunnel-server/authorized_keys > /dev/null'
```

`authorized_keys` 约每 2 秒热更新。无效内容或不支持的 `from=`、`command=` 等选项使新准入失败，修复文件后恢复。手工删除公钥禁止后续认证；立即断开现有连接应使用管理端封禁/断开功能。编辑文件时避免与管理端导入并发。

手工追加公钥后仍需在高级入口登记对应原 ID，历史自报 ID 不会自动成为可信绑定。
设备纳入账号后，旧 `authorized_keys` 条目不能绕过账号禁用、密码重置、黑名单或设备撤销。

## 运维

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

备份前建议短暂停止服务，复制整个 `/var/lib/tunnel-server`。数据库时间统一使用 UTC
Unix 微秒整数；“今日拒绝”边界按服务器操作系统时区计算。

服务端数据库当前为 schema 5，保存账号、管理员权限、设备归属、身份和审计；客户端配置仍为 schema 2。
升级和恢复须配套保留程序与完整数据库备份，按 [安全升级操作说明](UPGRADE_GUIDE.md) 操作。
