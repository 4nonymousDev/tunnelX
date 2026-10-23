# tunnel-server 部署（Linux）

[English](DEPLOY.en.md)

服务端是一个无 CGO 的单二进制，包含 SSH 隧道服务、管理 REST API 和管理 SPA。
隧道端口默认 `:2222`；管理端强制只监听 `127.0.0.1:2223`。

已有公网实例按 [安装与数据保留说明](deploy/README.md#重复安装与数据保留) 操作。客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。当前结构数据库中的设备登记保持不变，原密钥、ID 和隧道配置继续使用。详见[客户端接入指南](CLIENT_ACCESS_GUIDE.md)。

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
sudo ./install.sh tunnel-server-linux-amd64
```

安装脚本创建 `tunnel` nologin 账户，并准备：

- `/etc/tunnel-server/host_key`：只读主机私钥，目录 `0700`、文件 `0600`；
- `/var/lib/tunnel-server/authorized_keys`：可原子更新的授权公钥，文件 `0600`；
- `/var/lib/tunnel-server`，目录 `0700`，用于数据库、WAL 和 SHM；
- 带 `StateDirectory=tunnel-server`、`StateDirectoryMode=0700`、`UMask=0077` 的 systemd 服务。

管理账号保存在数据库中，首次安装需按下文创建。审计写入 SQLite。

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

在线状态只在内存，客户端资料、黑名单、连接审计、
访问审计和管理操作持久化在 `/var/lib/tunnel-server/tunnel-server.db`。

## 网络与管理访问

只需为 TunnelX SSH 端口（默认 2222）配置必要的防火墙/安全组规则。绝不能开放
2223；管理端也会拒绝非 loopback 监听地址。

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

首次安装尚无管理员时，在服务器终端创建管理员（自定义部署替换实际服务用户和数据目录）：

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

密码交互输入，不写在命令行。然后保持上述 SSH 转发，浏览 `http://127.0.0.1:2223`，使用管理员账号密码登录。刷新页面保留登录状态；退出、服务端重启、会话过期或密码重置后需重新登录。客户端不使用后台账号。找回密码及会话规则见[管理后台登录](ADMIN_LOGIN_GUIDE.md)。

## 客户端公钥接入

客户端在连接设置中保留或生成专用密钥，将公钥与设备 ID 交给管理员。管理员在「设备管理 → 登记设备」一次完成授权及身份绑定。首次连接核对服务器指纹。

客户端继续使用原密钥、ID 和配置。当前结构中的设备授权继续使用，已禁用或撤销的设备保持受限。后台密码修改不影响设备；封禁、撤销和恢复在设备管理中操作。详见 [客户端接入指南](CLIENT_ACCESS_GUIDE.md)。

手工编辑 authorized_keys 只改变文件授权，不能替代设备 ID 登记，也不改变数据库中的设备授权。立即停止设备访问应使用后台封禁或撤销身份。

## 运维

```bash
sudo systemctl status tunnel-server
sudo journalctl -u tunnel-server -f
sudo systemctl restart tunnel-server
```

### 握手日志与可疑连接

每次 TCP 连接在 SSH 握手结束或准入拒绝时记录结果。日志包含 `remote`（来源 IP 和端口）、
`duration_ms`、`result`、`reason` 和具体 `error`；进入认证阶段后还可记录 `client_version`、
`user`、`method`、`fingerprint`、`key_status` 和 `auth_error`。未取得的字段为空。
客户端版本、用户名及未认证的公钥指纹均是对方提供的信息，不代表已验证身份。
不记录密码、签名或原始报文；外部文本转义并截断到 512 字节，避免日志注入和过长记录。

`reason` 区分 `timeout`、`peer_closed`、`network_error`、`protocol_error`、
`authentication_failed`、`unauthorized_key`、`blocked_key`，以及服务器容量和来源速率限制。
握手成功为 `result=success reason=authenticated`，仅表示 SSH 认证成功，设备登记等后续准入仍可能失败。
每个失败或被拒绝的连接只计一次拒绝次数；正常的 SSH `none` 认证方法探测不单独计数。

同一 IP（跨来源端口合并）在一个统计窗口内累计失败达到阈值时，输出
`event=alert suspicious=true rule=frequent_failures`。默认窗口为 1 分钟，阈值为 10 次；
窗口从该 IP 的首条记录开始，结束后在下一条记录到达时重新计数，`window_start` 标明窗口起点。
握手失败、单来源并发超限和单来源速率超限计入判断；服务器全局容量不足、关闭和本地初始化失败不计入。
这是可疑频率提示，网络故障或共享出口也可能触发；不会自动封禁 IP。
可在 systemd 的 `ExecStart` 中追加 `-handshake-failure-window 1m -handshake-failure-threshold 10` 调整。

低频连接记录详情；同一 IP 的常规详情最多每 5 秒一条，阈值告警可提前输出。
全局每 5 秒最多输出 20 条详情及来源汇总。被限流的记录仍参与计数，
`suppressed` 表示当前记录合并了多少条未单独输出的记录；`event=summary` 保存最后一条样本及计数，
定时刷新，即使对方停止连接也会补写。来源跟踪默认最多 4096 个，满额时额外输出
`event=overflow untracked_attempts=...`；这些溢出连接只保留总数，不作逐 IP 判断。
正常关闭时补写仍在等待的来源汇总，不受上述输出速率限制。进程重启会清空内存中的可疑频率统计。
详情保存在标准输出（systemd 部署时由 journal 收集），数据库继续保存拒绝总数，不新增匿名逐条审计记录。

```bash
# 导出最近一小时的握手诊断日志
sudo journalctl -u tunnel-server --since "1 hour ago" --no-pager -o short-iso > tunnel-server-log.txt
# 查看可疑来源告警和被跟踪来源的后续可疑记录
sudo journalctl -u tunnel-server --since "1 hour ago" --no-pager | grep 'suspicious=true'
```

备份前建议短暂停止服务，复制整个 `/var/lib/tunnel-server`。数据库时间统一使用 UTC
Unix 微秒整数；“今日拒绝”边界按服务器操作系统时区计算。

服务端只维护当前数据库结构，保存管理员账号、设备授权、身份和审计，不提供历史版本迁移；客户端配置只使用当前结构。
升级和恢复须配套保留程序与完整数据库备份，按 [安装与数据保留说明](deploy/README.md#重复安装与数据保留) 操作。
