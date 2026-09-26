# 服务端一键部署

[English](README.en.md)

将 `install.sh`、`upgrade.sh` 与对应架构的 `tunnel-server` 二进制放在同一目录后执行：

```bash
chmod +x install.sh
sudo ./install.sh
```

默认隧道端口是 `2222`；可用 `--port 3333` 修改。ARM64 构建可通过
`--binary tunnel-server-linux-arm64` 指定。脚本可重复执行，升级不会覆盖主机密钥、
`authorized_keys`、旧 Token 文件或 SQLite 数据。Token 已不再参与认证。

| 路径 | 用途 | 权限 |
|---|---|---|
| `/usr/local/bin/tunnel-server` | 单一服务端二进制（内嵌管理页面） | `0755` |
| `/etc/tunnel-server/host_key` | SSH 主机私钥 | `0600` |
| `/var/lib/tunnel-server/authorized_keys` | TunnelX 客户端公钥，可由后台原子更新 | `0600` |
| `/var/lib/tunnel-server` | SQLite 数据库、WAL 与 SHM | `0700` |

管理服务只监听 `127.0.0.1:2223`，不要为 2223 配置防火墙或云安全组规则。访问时通过
系统 SSH 转发：

```bash
ssh -L 2223:127.0.0.1:2223 <user>@<server>
```

首次安装或从 Token 升级后，在服务器终端初始化管理员：

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

密码交互输入；自定义部署使用实际服务用户与原数据目录。同名账号会重设密码并启用管理权限，普通账号不会自动提升权限。保持上述 SSH 转发，打开 `http://127.0.0.1:2223` 并用管理员账号密码登录；刷新页面保留会话。服务器重启、退出或过期后重新登录。详见[管理后台登录](../ADMIN_LOGIN_GUIDE.md)。

在管理页打开「账号管理 → 创建账号」，填写用户名、密码、设备上限和审计原因。设备上限默认 10，可设为 1–50；不提供自助注册。客户端填写服务器地址后，使用管理员提供的账号密码登录，并核对首次连接的服务器指纹，即可自动登记设备。已有私钥、设备 ID 和隧道配置会保留；没有私钥时自动生成，无需上传 `.pub` 或手工提交设备 ID。详见[账号登录指南](../ACCOUNT_LOGIN_GUIDE.md)。

普通重启可使用已登记的设备密钥重连。禁用账号或重置密码会断开该账号的全部会话，下次连接需要密码登录；重新启用账号不会自动恢复这些会话。密码不写入客户端配置或日志。

「设备与高级管理 → 高级：存量公钥与身份管理」保留原公钥与 ID 登记、身份查询和变更入口。已授权的 v2 设备可继续沿用原手工授权规则；身份冲突、换绑、恢复和撤销仍需显式操作。设备纳入账号后，即使公钥仍在 `authorized_keys` 中，也不能绕过账号状态回退到旧文件授权。

新版本的连接、访问和管理审计都写入 SQLite，不再创建 JSONL 或 logrotate 配置。
升级时若存在 `/var/log/tunnel-server/audit.jsonl`，脚本会保留它供人工归档。
服务器数据库版本为 schema 5；客户端配置仍为 schema 2。

卸载：

```bash
sudo ./install.sh --uninstall          # 保留配置与数据
sudo ./install.sh --uninstall --purge  # 删除配置与数据
```

完整说明见 [DEPLOY.md](../DEPLOY.md)。

## 已有服务升级与恢复

保持旧服务运行，先把新二进制和两个脚本上传到单独目录，再执行 `sudo bash install.sh`；升级时不要传 `--port`。脚本先检查新二进制能否接受当前参数，并用 `-check-auth` 验证授权文件，再读取正在运行的服务参数，保留监听地址、端口、主机密钥、数据目录及其他参数。旧 `-admin-token-file` 参数保持兼容，但不再认证，Token 文件缺失也不影响升级。只有 `-auth` 改指向数据目录的 `authorized_keys`，以允许管理后台在同目录写临时文件并原子替换；主机密钥以及仍存在的旧 Token 为只读文件。

脚本停止服务后备份数据库及原始凭据，并连同旧二进制和 systemd 单元保存到 `/var/backups/tunnel-server/<时间>-<PID>/`（仅 root 可访问）。随后原子替换二进制、写入独立的 `90-tunnelx-upgrade.conf` 并检查实际启动参数。该过程会中断已有业务连接；仍获授权的 v2 客户端按其重试策略重新建立连接。v1 客户端必须升级；使用账号登录也需要更新客户端。原有客户端配置、专用私钥和 known_hosts 应保留。

服务未运行、使用自定义启动包装器、两份授权文件冲突或缺失主机密钥/数据时，脚本会拒绝推测。此时先保留维护窗口，在原 systemd 单元中核对 `ExecStart` 及 drop-in；复制完整数据目录、主机密钥、仍存在的旧 Token、授权文件、原二进制和所有单元配置，再只替换二进制，并将授权文件复制到可写数据目录、更新 `-auth` 与目录权限。保留原启动参数；不要重新生成主机或设备密钥。新数据目录需要运行账户所有权、`0700` 和 systemd 的可写许可，授权文件需要 `0600`。然后按上文初始化管理员账号。

若新版本启动失败，不要把旧程序直接接到已迁移的数据库。先停止服务、阻断公网入口，保留失败现场，再按备份 `paths.txt` 将原二进制、完整数据库目录和单元/drop-in 配套恢复，或部署包含相同安全修复的纠正版。对旧漏洞版本的恢复只可用于隔离排障，不能直接重新开放公网；重新开放前应通过账号登录、存量设备鉴权、隧道连接和审计检查。备份含秘密，不要上传到仓库或 Issue。

新安装设置 `LimitNOFILE=4096`、`TasksMax=256`、`MemoryHigh=768M`、`MemoryMax=1G`；升级保留单元中显式设置的同名限制，仅为尚未配置的项目补默认值。根据机器容量调整连接配额与 systemd 内存/CPU 限额，过低的限额会中断所有会话。脚本不更改防火墙规则。
