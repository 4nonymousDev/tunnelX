# tunnel-server 部署（Linux）

[English](DEPLOY.en.md)

服务端是一个无 CGO 的单二进制，包含 SSH 隧道服务、管理 REST API 和管理 SPA。
隧道端口默认 `:2222`；管理端强制只监听 `127.0.0.1:2223`。

已有公网实例按 [安全升级操作说明](deploy/README.md#已有服务升级与恢复) 操作。客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。原密钥、ID 和隧道配置保留；账号登记的有效设备自动迁移为独立授权，受限设备不会自动恢复。详见[客户端接入指南](CLIENT_ACCESS_GUIDE.md)。

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

密码交互输入，不写在命令行。然后保持上述 SSH 转发，浏览 `http://127.0.0.1:2223`，使用管理员账号密码登录。刷新页面保留登录状态；退出、服务端重启、会话过期或密码重置后需重新登录。客户端不使用后台账号。找回密码及会话规则见[管理后台登录](ADMIN_LOGIN_GUIDE.md)。

## 客户端公钥接入

客户端在连接设置中保留或生成专用密钥，将公钥与设备 ID 交给管理员。管理员在「设备管理 → 登记设备」一次完成授权及身份绑定。首次连接核对服务器指纹。

旧 v2 客户端继续使用原密钥、ID 和配置。账号登记的有效设备自动迁移为独立授权，已禁用、撤销或密码代次不匹配的设备保持受限。后台密码修改不影响设备；封禁、撤销和恢复在设备管理中操作。详见 [客户端接入指南](CLIENT_ACCESS_GUIDE.md)。

手工编辑 authorized_keys 只改变文件授权，不能替代设备 ID 登记，也不改变数据库中迁移设备的授权。立即停止设备访问应使用后台封禁或撤销身份。

## 运维

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

备份前建议短暂停止服务，复制整个 `/var/lib/tunnel-server`。数据库时间统一使用 UTC
Unix 微秒整数；“今日拒绝”边界按服务器操作系统时区计算。

服务端数据库当前为 schema 7，保存管理员账号、设备授权、身份和审计；客户端配置仍为 schema 2。
升级和恢复须配套保留程序与完整数据库备份，按 [安全升级操作说明](deploy/README.md#已有服务升级与恢复) 操作。
