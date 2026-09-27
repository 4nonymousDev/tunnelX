# 服务端一键部署

[English](README.en.md)

## 下载 Linux 发布包

发布工作流在推送新的 `v*` 标签后，会向同一个 GitHub Release 上传 Windows 安装包及两种 Linux 包：

- `TunnelX-<发布版本>-linux-amd64.tar.gz`：x86_64 服务器。
- `TunnelX-<发布版本>-linux-arm64.tar.gz`：aarch64 / ARM64 服务器。
- `SHA256SUMS-linux.txt`：两个压缩包的 SHA-256 校验值。

每个 Linux 包包含 `tunnel-server`（内嵌新构建的管理页面）、`tunnelx-cli`、`install.sh`、`upgrade.sh` 及文档。选择与 `uname -m` 匹配的包，解压至空目录后运行 `sudo bash install.sh --binary tunnel-server`；已有服务会走保留原参数和数据的升级流程。CLI 可直接用 `./tunnelx-cli --help` 查看命令。CLI 的配置和状态目录应放在持久目录中，升级时继续指定原 `--config` / `--state-dir`，保留原密钥。

下载两个包和校验文件后，可运行 `sha256sum -c SHA256SUMS-linux.txt`；仅下载一个架构时使用 `sha256sum --ignore-missing -c SHA256SUMS-linux.txt`。压缩包名使用 Release 版本，内部 CLI/server 的核心版本来自 `CLI_VERSION`，可查看 `VERSION.txt`、`./tunnelx-cli version` 或 `./tunnel-server -version`。

Linux 发布在 Windows 发布成功后执行；上传前会用打包后的 amd64 二进制运行真实连接测试，arm64 进行交叉编译。此配置对后续新标签生效，已有 Release 不会自动补包。

## 安装服务

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

客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。原密钥、ID 和隧道配置保留；账号登记的有效设备自动迁移为独立授权，受限设备不会自动恢复。详见[客户端接入指南](../CLIENT_ACCESS_GUIDE.md)。

客户端不再创建账号或提交密码。新设备使用专用密钥，在设置中复制设备 ID，连同公钥交给管理员。有效的旧 v2 客户端可直接连接新 server；更新客户端可移除旧登录界面。后台密码修改与设备授权互相独立。

连接、访问和管理审计都写入 SQLite，不再创建 JSONL 或 logrotate 配置。
升级时若存在 `/var/log/tunnel-server/audit.jsonl`，脚本会保留它供人工归档。
服务器数据库版本为 schema 7；客户端配置仍为 schema 2。

卸载：

```bash
sudo ./install.sh --uninstall          # 保留配置与数据
sudo ./install.sh --uninstall --purge  # 删除配置与数据
```

完整说明见 [DEPLOY.md](../DEPLOY.md)。

## 可选更新与强制更新

GitHub Actions 会在临时 Linux runner 中启动实际 server 和两个 CLI 进程，检查管理员登录与公钥登记、端到端 HTTP 转发、宽限期、到期断开、撤销后恢复及原密钥/配置保留。所有监听都在 runner 的 loopback 上，测试不连接公网生产实例；Windows 发布以这项检查通过为前提。

强制更新默认关闭。普通功能更新只需发布新版客户端，由用户选择安装。

必要更新的操作顺序：

1. 先提供可用的新客户端安装包，再登录后台打开「客户端更新」。
2. 启用强制更新，填写最低客户端版本、截止时间、更新说明和操作原因。默认宽限期为从现在起 **7 天**，可以自行调整。最低版本对应 CLI 核心版本，设置前确认桌面安装包内的 CLI 已达到要求。
3. 宽限期内旧版继续使用；支持此功能的客户端显示最低版本及截止时间，可以主动检查更新。到期后服务端停止低版本接入，并关闭已有连接与隧道；达标版本不受影响。到期时间以服务端时钟为准，在线会话通常在 1 秒内处理，网络写入异常受写超时约束。
4. 需要延期时修改截止时间；需要取消时关闭策略并保存。已被断开的客户端可点击“连接”重试，CLI 可重启连接进程。操作会记入管理审计。

截止时间保存在数据库中，重启服务或重连不会重新获得 7 天。更新只替换程序，管理员账号、设备密钥、配置及隧道数据继续使用。数据库迁移不会自动启用强制更新。

本功能从 GUI/CLI 0.2.1 开始提供完整提醒。原 v2 客户端可在宽限期内连接新 server，但没有新增的提醒界面；到期同样受最低版本限制。v1 不兼容当前 server。未来如果要切换不兼容的协议，需要在仍支持旧协议的服务端上提前通知并升级客户端，然后再切换协议；宽限期不能消除协议不兼容。

安装来源仍是客户端配置的发布源，不接受 server 下发的下载地址或脚本。桌面 0.2.4 起支持未签名发行版的应用内更新并校验下载完整性，签名发行版额外校验发布者；0.2.2 / 0.2.3 须手工覆盖升级一次。版本号是客户端自报信息，用于管理正常客户端的兼容性，不是对二进制完整性或漏洞已修复的证明。

## 已有服务升级与恢复

保持旧服务运行，先把新二进制和两个脚本上传到单独目录，再执行 `sudo bash install.sh`；升级时不要传 `--port`。脚本先检查新二进制能否接受当前参数，并用 `-check-auth` 验证授权文件，再读取正在运行的服务参数，保留监听地址、端口、主机密钥、数据目录及其他参数。旧 `-admin-token-file` 参数保持兼容，但不再认证，Token 文件缺失也不影响升级。只有 `-auth` 改指向数据目录的 `authorized_keys`，以允许管理后台在同目录写临时文件并原子替换；主机密钥以及仍存在的旧 Token 为只读文件。

脚本停止服务后备份数据库及原始凭据，并连同旧二进制和 systemd 单元保存到 `/var/backups/tunnel-server/<时间>-<PID>/`（仅 root 可访问）。随后原子替换二进制、写入独立的 `90-tunnelx-upgrade.conf` 并检查实际启动参数。该过程会中断已有业务连接；仍获授权的 v2 客户端按其重试策略重新建立连接。v1 客户端必须升级；已获授权的 v2 客户端可继续使用原密钥连接。原有客户端配置、专用私钥和 known_hosts 应保留。

服务未运行、使用自定义启动包装器、两份授权文件冲突或缺失主机密钥/数据时，脚本会拒绝推测。此时先保留维护窗口，在原 systemd 单元中核对 `ExecStart` 及 drop-in；复制完整数据目录、主机密钥、仍存在的旧 Token、授权文件、原二进制和所有单元配置，再只替换二进制，并将授权文件复制到可写数据目录、更新 `-auth` 与目录权限。保留原启动参数；不要重新生成主机或设备密钥。新数据目录需要运行账户所有权、`0700` 和 systemd 的可写许可，授权文件需要 `0600`。然后按上文初始化管理员账号。

若新版本启动失败，不要把旧程序直接接到已迁移的数据库。先停止服务、阻断公网入口，保留失败现场，再按备份 `paths.txt` 将原二进制、完整数据库目录和单元/drop-in 配套恢复，或部署包含相同安全修复的纠正版。对旧漏洞版本的恢复只可用于隔离排障，不能直接重新开放公网；重新开放前应通过管理员登录、存量设备鉴权、隧道连接和审计检查。备份含秘密，不要上传到仓库或 Issue。

新安装设置 `LimitNOFILE=4096`、`TasksMax=256`、`MemoryHigh=768M`、`MemoryMax=1G`；升级保留单元中显式设置的同名限制，仅为尚未配置的项目补默认值。根据机器容量调整连接配额与 systemd 内存/CPU 限额，过低的限额会中断所有会话。脚本不更改防火墙规则。
