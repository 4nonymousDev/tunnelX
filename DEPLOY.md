# tunnel-server 部署（Linux）

[English](DEPLOY.en.md)

服务端是一个无 CGO 的单二进制，包含 SSH 隧道服务、管理 REST API 和管理 SPA。
隧道端口默认 `:2222`；管理端强制只监听 `127.0.0.1:2223`。

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
scp deploy/install.sh tunnel-server-linux-amd64 <user>@<server>:~/
ssh <user>@<server>
chmod +x install.sh
sudo ./install.sh
```

安装脚本创建 `tunnel` nologin 账户，并准备：

- `/etc/tunnel-server/host_key`、`authorized_keys`、`admin.token`，目录 `0700`、文件 `0600`；
- `/var/lib/tunnel-server`，目录 `0700`，用于数据库、WAL 和 SHM；
- 带 `StateDirectory=tunnel-server`、`StateDirectoryMode=0700`、`UMask=0077` 的 systemd 服务。

`admin.token` 是 32 字节随机值的 64 位十六进制表示，已存在时绝不覆盖。脚本不再
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
  -auth /etc/tunnel-server/authorized_keys \
  -admin-addr 127.0.0.1:2223 \
  -admin-token-file /etc/tunnel-server/admin.token \
  -data-dir /var/lib/tunnel-server
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
RestrictNamespaces=true
```

旧 `-audit` 参数已退出主流程。在线状态只在内存，客户端资料、黑名单、连接审计、
访问审计和管理操作持久化在 `/var/lib/tunnel-server/tunnel-server.db`。

## 网络与管理访问

只需为 TunnelX SSH 端口（默认 2222）配置必要的防火墙/安全组规则。绝不能开放
2223；管理端也会拒绝非 loopback 监听地址。

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

保持该系统 SSH 会话，然后浏览 `http://127.0.0.1:2223`。管理 Token 可在服务器上
读取：

```bash
sudo cat /etc/tunnel-server/admin.token
```

## 客户端授权

每台客户端使用独立 SSH 密钥，把公钥一行追加到：

```bash
cat tunnel_key.pub | ssh <user>@<server> \
  'sudo tee -a /etc/tunnel-server/authorized_keys > /dev/null'
```

`authorized_keys` 会热更新。空文件可撤销最后一把密钥；无效行会使本次重载失败并
保留上一份有效集合。管理端拉黑按服务端计算的 SSH SHA256 指纹生效，不改动此文件。

## 运维

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

备份前建议短暂停止服务，复制整个 `/var/lib/tunnel-server`。数据库时间统一使用 UTC
Unix 微秒整数；“今日拒绝”边界按服务器操作系统时区计算。
