# 服务端一键部署

[English](README.en.md)

## 下载 Linux 发布包

发布工作流在推送新的 `v*` 标签后，会向同一个 GitHub Release 上传 Windows 安装包及两种 Linux 包：

- `TunnelX-<发布版本>-linux-amd64.tar.gz`：x86_64 服务器。
- `TunnelX-<发布版本>-linux-arm64.tar.gz`：aarch64 / ARM64 服务器。
- `SHA256SUMS-linux.txt`：两个压缩包的 SHA-256 校验值。

每个 Linux 包包含 `tunnel-server`（内嵌新构建的管理页面）、`tunnelx-cli`、`install.sh` 及文档。选择与 `uname -m` 匹配的包，解压至空目录后运行 `sudo bash install.sh --binary tunnel-server`；首次和重复安装使用相同流程，沿用已有数据。CLI 可直接用 `./tunnelx-cli --help` 查看命令。CLI 的配置和状态目录应放在持久目录中，升级时继续指定原 `--config` / `--state-dir`，保留原密钥。

下载两个包和校验文件后，可运行 `sha256sum -c SHA256SUMS-linux.txt`；仅下载一个架构时使用 `sha256sum --ignore-missing -c SHA256SUMS-linux.txt`。压缩包名使用 Release 版本，内部 CLI/server 的核心版本来自 `CLI_VERSION`，可查看 `VERSION.txt`、`./tunnelx-cli version` 或 `./tunnel-server -version`。

Linux 发布在 Windows 发布成功后执行；上传前会用打包后的 amd64 二进制运行真实连接测试，arm64 进行交叉编译。此配置对后续新标签生效，已有 Release 不会自动补包。

## 安装服务

将 `install.sh` 与对应架构的 `tunnel-server` 二进制放在同一目录后执行：

```bash
chmod +x install.sh
sudo ./install.sh
```

默认隧道端口是 `2222`；可用 `--port 3333` 修改。ARM64 构建可通过
`--binary tunnel-server-linux-arm64` 指定。脚本可重复执行，重复安装不会覆盖主机密钥、
`authorized_keys` 或 SQLite 数据。每次安装均按本次参数写入服务配置，自定义端口需每次传入 `--port`。

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

仅首次安装尚无管理员时，在服务器终端初始化管理员：

```bash
sudo systemctl stop tunnel-server
sudo -u tunnel /usr/local/bin/tunnel-server -data-dir /var/lib/tunnel-server -admin-account admin
sudo systemctl start tunnel-server
```

密码交互输入；自定义部署使用实际服务用户与原数据目录。同名账号会重设密码并启用管理权限，已有管理员无需重复执行。保持上述 SSH 转发，打开 `http://127.0.0.1:2223` 并用管理员账号密码登录；刷新页面保留会话。服务器重启、退出或过期后重新登录。详见[管理后台登录](../ADMIN_LOGIN_GUIDE.md)。

客户端使用设备公钥连接，管理员在「设备管理 → 登记设备」中一次提交公钥与设备 ID。管理后台仍使用管理员账号登录。当前结构数据库中的设备登记保持不变，原密钥、ID 和隧道配置继续使用。详见[客户端接入指南](../CLIENT_ACCESS_GUIDE.md)。

新设备提交专用公钥与设备 ID，管理员确认一次后即可接入。后台密码修改与设备授权互相独立。

连接、访问和管理审计均写入 SQLite。服务端与客户端只使用当前数据结构，不执行历史规则迁移。

卸载：

```bash
sudo ./install.sh --uninstall          # 保留配置与数据
sudo ./install.sh --uninstall --purge  # 删除配置与数据
```

完整说明见 [DEPLOY.md](../DEPLOY.md)。

## 重复安装与数据保留

将新二进制和 `install.sh` 放在同一目录，按首次安装相同命令执行。例如使用自定义端口时，每次均运行：

```bash
sudo bash install.sh --binary tunnel-server --port 3333
```

脚本检查程序及现有授权文件，停服后将数据目录、配置目录、现有程序和 systemd 配置备份至 `/var/backups/tunnel-server/<时间>-<PID>/`，再原子替换程序并写入当前服务配置。运行中的服务和已停止的服务使用相同安装流程，不读取旧程序参数、不转换旧规则。自定义路径部署应直接替换二进制，并继续使用实际数据目录和密钥路径。

以下数据继续保留，无需重复导入：

- `/var/lib/tunnel-server/authorized_keys`：已登记公钥。
- `/var/lib/tunnel-server/tunnel-server.db` 及完整数据目录：设备 ID 绑定、授权状态、管理员账号和审计。
- `/etc/tunnel-server/host_key`：客户端已信任的服务器身份。
- 客户端原配置、设备私钥和 `known_hosts`：继续放在原持久目录。

已有数据库但主机密钥或授权文件缺失时，安装会停止，避免意外生成新身份。仅首次安装需要创建管理员；重复安装不要执行密码初始化命令。`--uninstall` 保留数据，只有明确追加 `--purge` 才删除默认配置与数据目录。

安装中断现有业务连接；服务恢复后，运行中的客户端按重试策略重连。若安装失败，备份目录中的 `data`、`config`、`tunnel-server` 和服务配置可用于停服恢复。

服务配置使用当前默认资源限制：`LimitNOFILE=4096`、`TasksMax=256`、`MemoryHigh=768M`、`MemoryMax=1G`。自定义 systemd drop-in 由管理员维护。脚本不修改防火墙规则。

## 桌面客户端更新

桌面客户端保留检查、下载和确认安装更新的功能。安装包替换程序，沿用原 `userData` 中的配置、密钥和设备 ID。服务端不提供客户端最低版本策略或旧规则升级流程。
