# 服务端一键部署

[English](README.en.md)

将 `install.sh` 与对应架构的 `tunnel-server` 二进制放在同一目录后执行：

```bash
chmod +x install.sh
sudo ./install.sh
```

默认隧道端口是 `2222`；可用 `--port 3333` 修改。ARM64 构建可通过
`--binary tunnel-server-linux-arm64` 指定。脚本可重复执行，升级不会覆盖主机密钥、
`authorized_keys`、管理 Token 或 SQLite 数据。

| 路径 | 用途 | 权限 |
|---|---|---|
| `/usr/local/bin/tunnel-server` | 单一服务端二进制（内嵌管理页面） | `0755` |
| `/etc/tunnel-server/host_key` | SSH 主机私钥 | `0600` |
| `/etc/tunnel-server/authorized_keys` | TunnelX 客户端公钥 | `0600` |
| `/etc/tunnel-server/admin.token` | 32 字节随机管理 Token（64 位 hex） | `0600` |
| `/var/lib/tunnel-server` | SQLite 数据库、WAL 与 SHM | `0700` |

管理服务只监听 `127.0.0.1:2223`，不要为 2223 配置防火墙或云安全组规则。访问时通过
系统 SSH 转发：

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

随后打开 `http://127.0.0.1:2223`，输入 `/etc/tunnel-server/admin.token` 的内容。
Token 只保存在当前页面内存中。

新版本的连接、访问和管理审计都写入 SQLite，不再创建 JSONL 或 logrotate 配置。
升级时若存在 `/var/log/tunnel-server/audit.jsonl`，脚本会保留它供人工归档。

卸载：

```bash
sudo ./install.sh --uninstall          # 保留配置与数据
sudo ./install.sh --uninstall --purge  # 删除配置与数据
```

完整说明见 [DEPLOY.md](../DEPLOY.md)。
