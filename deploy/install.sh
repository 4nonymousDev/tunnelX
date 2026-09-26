#!/usr/bin/env bash
#
# TunnelX 服务端一键安装脚本（Linux）
# One-command TunnelX server installer (Linux).
#
# 用法：
# Usage:
#   sudo ./install.sh                          # 安装并启动
#   sudo ./install.sh                          # Install and start
#   sudo ./install.sh --port 3333              # 指定监听端口
#   sudo ./install.sh --port 3333              # Select the listen port
#   sudo ./install.sh --uninstall              # 卸载（保留配置与数据）
#   sudo ./install.sh --uninstall              # Uninstall (keep configuration and data)
#   sudo ./install.sh --uninstall --purge      # 卸载并删除全部数据
#   sudo ./install.sh --uninstall --purge      # Uninstall and delete all data
#
# 脚本可重复执行：已存在的主机密钥、authorized_keys、管理 Token 与数据不会被覆盖。
# 升级只需把新的二进制放在脚本旁边再跑一次。
# The script is idempotent: it never overwrites an existing host key,
# authorized_keys file, management token, or data. To upgrade, place the new
# binary beside this script and run it again.

set -euo pipefail
umask 077

# ---------- 默认值 ----------
# ---------- Defaults ----------
BINARY_NAME="tunnel-server-linux-amd64"
INSTALL_PATH="/usr/local/bin/tunnel-server"
CONFIG_DIR="/etc/tunnel-server"
DATA_DIR="/var/lib/tunnel-server"
SERVICE_NAME="tunnel-server"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
RUN_USER="tunnel"
PORT="2222"
PORT_SET=0
DO_UNINSTALL=0
DO_PURGE=0

# ---------- 输出 ----------
# ---------- Output helpers ----------
c_red=$'\033[31m'; c_green=$'\033[32m'; c_yellow=$'\033[33m'
c_blue=$'\033[36m'; c_bold=$'\033[1m'; c_off=$'\033[0m'

info()  { printf '%s==>%s %s\n' "$c_blue"  "$c_off" "$*"; }
ok()    { printf '%s  ✓%s %s\n' "$c_green" "$c_off" "$*"; }
warn()  { printf '%s  !%s %s\n' "$c_yellow" "$c_off" "$*"; }
die()   { printf '%s  ✗%s %s\n' "$c_red"   "$c_off" "$*" >&2; exit 1; }

# count_keys 统计已登记的公钥数。
# count_keys returns the number of registered public keys.
#
# 不用 `grep -c ... || echo 0`：grep 无匹配时退出码为 1（在 pipefail 下整条
# 管道也返回 1），`|| echo 0` 便会再追加一行，结果是两行 "0"，后续的算术
# 比较随即报 "syntax error in expression"。改用 grep -o 逐行输出再计数，
# wc -l 始终成功。
# Avoid `grep -c ... || echo 0`: with pipefail, no matches make the pipeline
# return 1, so the fallback appends another zero and breaks later arithmetic.
# Emitting matching lines with grep -o and counting them makes wc -l succeed.
count_keys() {
    local f="$DATA_DIR/authorized_keys"
    [[ -f "$f" ]] || { echo 0; return; }
    awk '/^ssh-/ { n++ } END { print n+0 }' "$f"
}

usage() {
    # 从文件头的注释块提取用法说明，避免两处维护同一段文字。
    # Extract usage from the header so the same text is not maintained twice.
    sed -n '3,/^$/p' "$0" | sed 's/^#\{1,\} \{0,1\}//'
    exit 0
}

# ---------- 参数 ----------
# ---------- Arguments ----------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --port)      PORT="${2:?--port 需要一个端口号}"; PORT_SET=1; shift 2 ;;
        --binary)    BINARY_NAME="${2:?--binary 需要一个文件名}"; shift 2 ;;
        --uninstall) DO_UNINSTALL=1; shift ;;
        --purge)     DO_PURGE=1; shift ;;
        -h|--help)   usage ;;
        *)           die "未知参数: $1（用 --help 查看用法）" ;;
    esac
done

[[ $EUID -eq 0 ]] || die "请用 root 运行：sudo $0 $*"
[[ $PORT =~ ^[0-9]{1,5}$ ]] && ((10#$PORT > 0 && 10#$PORT <= 65535)) || die "端口无效"
PORT=$((10#$PORT))

command -v systemctl >/dev/null 2>&1 || die "未找到 systemctl，本脚本仅支持 systemd 系统"

# ---------- 卸载 ----------
# ---------- Uninstall ----------
if [[ $DO_UNINSTALL -eq 1 ]]; then
    info "卸载 tunnel-server"

    if systemctl list-unit-files | grep -q "^${SERVICE_NAME}.service"; then
        systemctl disable --now "$SERVICE_NAME" 2>/dev/null || true
        ok "服务已停止并取消开机自启"
    fi

    rm -f "$SERVICE_FILE" "/etc/systemd/system/${SERVICE_NAME}.service.d/90-tunnelx-upgrade.conf"
    systemctl daemon-reload
    rm -f "$INSTALL_PATH"
    ok "已移除服务与二进制"

    if [[ $DO_PURGE -eq 1 ]]; then
        # 密钥一旦删除，所有客户端都需重新配置 known_hosts，故须显式确认。
        # Deleting the key forces every client to reconfigure known_hosts, so
        # this requires explicit confirmation.
        rm -rf "$CONFIG_DIR" "$DATA_DIR"
        userdel "$RUN_USER" 2>/dev/null || true
        ok "已删除配置目录、数据与服务账户"
    else
        warn "保留了 $CONFIG_DIR 与 $DATA_DIR（如需一并删除，加 --purge）"
    fi

    exit 0
fi

# ---------- 检查二进制 ----------
# ---------- Binary checks ----------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_BINARY="$SCRIPT_DIR/$BINARY_NAME"

if [[ ! -f "$SRC_BINARY" ]]; then
    # 便于直接把脚本和二进制丢在同一目录：也接受不带平台后缀的名字。
    # Also accept a name without a platform suffix for convenient colocated use.
    if [[ -f "$SCRIPT_DIR/tunnel-server" ]]; then
        SRC_BINARY="$SCRIPT_DIR/tunnel-server"
    else
        die "未找到二进制 $SRC_BINARY
    请把 $BINARY_NAME 与本脚本放在同一目录，或用 --binary 指定文件名。
    交叉编译命令见 DEPLOY.md §1："
    fi
fi

# 提前校验架构，避免装完才发现跑不起来。
# Validate the architecture before installation to fail early.
if command -v file >/dev/null 2>&1; then
    arch_info="$(file -b "$SRC_BINARY")"
    case "$(uname -m)" in
        x86_64)  echo "$arch_info" | grep -q "x86-64"  || warn "二进制架构可能不匹配（本机 x86_64）：$arch_info" ;;
        aarch64) echo "$arch_info" | grep -q "aarch64" || warn "二进制架构可能不匹配（本机 aarch64）：$arch_info" ;;
    esac
fi

if [[ -n $(systemctl show "$SERVICE_NAME" -p FragmentPath --value 2>/dev/null || true) ]]; then
    ((PORT_SET == 0)) || die "升级保留原端口，请不要传 --port"
    [[ -f "$SCRIPT_DIR/upgrade.sh" ]] || die "升级需要同目录的 deploy/upgrade.sh"
    exec bash "$SCRIPT_DIR/upgrade.sh" "$SRC_BINARY"
fi
chmod +x -- "$SRC_BINARY"
"$SRC_BINARY" -version >/dev/null || die "新二进制无法运行"
info "安装 tunnel-server（端口 $PORT）"

# ---------- 服务账户 ----------
# ---------- Service account ----------
# 以非 root 运行：转发端口由系统在 >1024 自动分配（DESIGN.md §4.3），
# 无需绑定特权端口。即便程序有漏洞，攻击者拿到的也只是 nologin 账户。
# Run without root: forwarded ports are allocated above 1024 (DESIGN.md §4.3),
# so privileged binds are unnecessary. A compromise is confined to a nologin
# account.
if id "$RUN_USER" >/dev/null 2>&1; then
    ok "服务账户 $RUN_USER 已存在"
else
    useradd -r -s /usr/sbin/nologin -M "$RUN_USER"
    ok "已创建服务账户 $RUN_USER"
fi

# ---------- 安装二进制 ----------
# ---------- Install binary ----------
# 先停服务再覆盖：正在运行的可执行文件无法直接写入（Text file busy）。
# Stop the service before replacement because a running executable may be busy.
if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
    systemctl stop "$SERVICE_NAME"
    warn "已停止运行中的服务以便升级（隧道会短暂中断）"
    WAS_RUNNING=1
else
    WAS_RUNNING=0
fi

install -m 0755 "$SRC_BINARY" "$INSTALL_PATH"
ok "二进制已安装到 $INSTALL_PATH"

VERSION="$("$INSTALL_PATH" -version 2>/dev/null || echo '未知版本')"
ok "版本：$VERSION"

# ---------- 配置目录 ----------
# ---------- Configuration directory ----------
mkdir -p "$CONFIG_DIR" "$DATA_DIR"

# 主机密钥：绝不覆盖已有的。
# Never overwrite an existing host key.
#
# 它是服务器的身份证明，客户端首次连接时确认过指纹并写入了 known_hosts。
# 换掉会让所有客户端报"主机密钥与记录不符"——那是中间人攻击的告警信号，
# 客户端会拒绝连接（DESIGN.md §7 问题 8）。
# The host key identifies the server. Clients pin its fingerprint in
# known_hosts; replacing it triggers a man-in-the-middle warning and causes
# clients to reject the connection (DESIGN.md §7, question 8).
if [[ -f "$CONFIG_DIR/host_key" ]]; then
    ok "主机密钥已存在，保留不动"
else
    ssh-keygen -t ed25519 -f "$CONFIG_DIR/host_key" -N "" -q
    ok "已生成主机密钥"
fi

# 授权公钥：同样不覆盖。
# Never overwrite authorized public keys either.
if [[ -f "$CONFIG_DIR/authorized_keys" ]]; then
    if [[ -e "$DATA_DIR/authorized_keys" ]]; then
        cmp -s "$CONFIG_DIR/authorized_keys" "$DATA_DIR/authorized_keys" || die "两处 authorized_keys 不一致，请先合并确认后重新安装"
    else
        install -m 0600 "$CONFIG_DIR/authorized_keys" "$DATA_DIR/authorized_keys"
    fi
fi
if [[ -f "$DATA_DIR/authorized_keys" ]]; then
    key_count="$(count_keys)"
    ok "authorized_keys 已存在（$key_count 个公钥），保留不动"
else
    touch "$DATA_DIR/authorized_keys"
    warn "authorized_keys 为空；安装完成后创建管理员，再通过账号管理为客户端创建账号"
fi

chown -R "$RUN_USER:$RUN_USER" "$CONFIG_DIR"
chown "$RUN_USER:$RUN_USER" "$DATA_DIR" "$DATA_DIR/authorized_keys"
chmod 700 "$CONFIG_DIR"
chmod 600 "$CONFIG_DIR/host_key" "$DATA_DIR/authorized_keys"

# ---------- 管理账号与数据目录 ----------
# ---------- Management accounts and data directory ----------
# Administrators are explicitly provisioned in SQLite using -admin-account.
# Legacy token files remain untouched but no longer authenticate requests.
mkdir -p "$DATA_DIR"
chown "$RUN_USER:$RUN_USER" "$DATA_DIR"
chmod 700 "$DATA_DIR"

# 新版本只写 SQLite。升级时不删除旧 JSONL，留给管理员自行归档。
# The new version writes only SQLite. Preserve legacy JSONL during upgrades so
# an administrator can archive it manually.
if [[ -f /var/log/tunnel-server/audit.jsonl ]]; then
    warn "检测到旧审计文件 /var/log/tunnel-server/audit.jsonl，已保留供人工归档"
fi
if [[ -f /etc/logrotate.d/tunnel-server ]]; then
    rm -f /etc/logrotate.d/tunnel-server
    ok "已移除旧 JSONL 审计的 logrotate 配置（审计文件本身保留）"
fi

# ---------- systemd 单元 ----------
# ---------- systemd unit ----------
cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=TunnelX Tunnel Server
Documentation=https://github.com/your-org/tunnelx
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$RUN_USER
Group=$RUN_USER
WorkingDirectory=$CONFIG_DIR
StateDirectory=tunnel-server
StateDirectoryMode=0700
UMask=0077
ExecStart=$INSTALL_PATH \\
  -addr :$PORT \\
  -hostkey $CONFIG_DIR/host_key \\
  -auth $DATA_DIR/authorized_keys \\
  -admin-addr 127.0.0.1:2223 \\
  -data-dir $DATA_DIR

# 常驻：异常退出后自动拉起
# Keep the service running by restarting it after an unexpected exit.
Restart=always
RestartSec=5

# 加固：服务端只需读配置、写 StateDirectory、监听端口
# Hardening: the server only needs to read configuration, write its
# StateDirectory, and listen on network ports.
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
ReadOnlyPaths=$CONFIG_DIR

# 仅需 IPv4/IPv6 socket
# Only IPv4/IPv6 sockets are required.
RestrictAddressFamilies=AF_INET AF_INET6
LimitNOFILE=4096
TasksMax=256
MemoryHigh=768M
MemoryMax=1G

[Install]
WantedBy=multi-user.target
EOF

ok "已写入 $SERVICE_FILE"

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
systemctl start "$SERVICE_NAME"

# ---------- 验证 ----------
# ---------- Verification ----------
sleep 1
if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    printf '\n%s服务启动失败%s，最近日志：\n\n' "$c_red$c_bold" "$c_off"
    journalctl -u "$SERVICE_NAME" -n 20 --no-pager
    exit 1
fi

ok "服务已启动并设为开机自启"

# ---------- 防火墙提示 ----------
# ---------- Firewall guidance ----------
FW_HINT=""
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "^Status: active"; then
    if ! ufw status | grep -q "$PORT"; then
        FW_HINT="sudo ufw allow $PORT/tcp"
    fi
elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    if ! firewall-cmd --list-ports 2>/dev/null | grep -q "$PORT/tcp"; then
        FW_HINT="sudo firewall-cmd --permanent --add-port=$PORT/tcp && sudo firewall-cmd --reload"
    fi
fi

# ---------- 完成 ----------
# ---------- Completion ----------
FINGERPRINT="$(ssh-keygen -lf "$CONFIG_DIR/host_key.pub" 2>/dev/null | awk '{print $2}')"
KEY_COUNT="$(count_keys)"

cat <<EOF

${c_bold}安装完成${c_off}

  服务      $SERVICE_NAME（已常驻，开机自启）
  监听      0.0.0.0:$PORT
  版本      $VERSION
  主机指纹  $FINGERPRINT
  授权公钥  $KEY_COUNT 个
  管理后台  仅服务器本机 127.0.0.1:2223

${c_bold}常用命令${c_off}

  systemctl status $SERVICE_NAME     查看状态
  journalctl -u $SERVICE_NAME -f     实时日志
  systemctl restart $SERVICE_NAME    重启

${c_bold}访问管理后台${c_off}

  首次创建管理员（密码交互输入，不写入命令行）：
  sudo systemctl stop $SERVICE_NAME
  sudo -u $RUN_USER $INSTALL_PATH -data-dir $DATA_DIR -admin-account admin
  sudo systemctl start $SERVICE_NAME

  ssh -L 2223:127.0.0.1:2223 <user>@<server>
  然后浏览 http://127.0.0.1:2223，用管理员账号密码登录；刷新保留登录状态。

EOF

if [[ -n "$FW_HINT" ]]; then
    printf '%s需要放行端口%s\n\n  %s\n\n' "$c_yellow$c_bold" "$c_off" "$FW_HINT"
fi

printf '%s云服务器还需在控制台的安全组中放行 %s/tcp%s\n\n' "$c_yellow" "$PORT" "$c_off"

if [[ "$KEY_COUNT" -eq 0 ]]; then
    cat <<EOF
${c_bold}下一步：创建客户端账号${c_off}

在管理后台「账号管理」为用户创建普通账号。用户在客户端登录后自动登记设备。
普通账号没有管理权限，原公钥和设备 ID 无需更换。

EOF
fi
