#!/usr/bin/env bash
# Called by install.sh for existing services; never rebuild argv from defaults.
set -euo pipefail
umask 077
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
[[ $EUID == 0 && $# == 1 ]] || die 'Usage: sudo upgrade.sh NEW_BINARY'
SERVICE=tunnel-server
BINARY=/usr/local/bin/tunnel-server
SOURCE=$1
PID=$(systemctl show "$SERVICE" -p MainPID --value)
[[ $PID =~ ^[0-9]+$ ]] && ((PID > 0)) && [[ -r /proc/$PID/cmdline ]] || die 'Inactive service: cannot safely infer its arguments. Follow the offline upgrade instructions.'
mapfile -d '' -t ARGS < "/proc/$PID/cmdline"
[[ ${ARGS[0]:-} == "$BINARY" ]] || die 'Custom executable/wrapper: follow the offline upgrade instructions.'
# Go flag parsing stops at a positional argument or --. Refuse these before
# executing any preflight, which must never start or migrate a live service.
for ((i=1; i<${#ARGS[@]}; i++)); do
  arg=${ARGS[i]}
  [[ $arg == -* && $arg != - && $arg != -- ]] || die 'Positional arguments/-- require an offline upgrade.'
  name=${arg%%=*}; name=${name#-}; name=${name#-}
  case "$name" in
    version|check-auth) [[ $arg == *=false ]] || die 'Unexpected control flag in service arguments' ;;
    *) if [[ $arg != *=* ]]; then ((i+1<${#ARGS[@]})) || die 'Missing flag value'; i=$((i+1)); fi ;;
  esac
done
STAGED=$(mktemp "${BINARY}.new.XXXXXX")
trap 'rm -f -- "$STAGED"' EXIT
install -m 0755 -- "$SOURCE" "$STAGED"
VERSION=$("$STAGED" -version "${ARGS[@]:1}" -version) || die 'New executable/flags invalid; old service is untouched.'
[[ $VERSION == tunnel-server\ * ]] || die 'Not a tunnel-server executable'
WORKDIR=$(readlink -f "/proc/$PID/cwd")
AUTH="$WORKDIR/authorized_keys"; HOSTKEY="$WORKDIR/host_key"; TOKEN="$WORKDIR/admin.token"; DATA="$WORKDIR/data"
for ((i=1; i<${#ARGS[@]}; i++)); do
  arg=${ARGS[i]}
  [[ $arg != --* ]] || arg=${arg#-}
  case "$arg" in
    -auth|-hostkey|-admin-token-file|-data-dir) ((i+1<${#ARGS[@]})) || die 'Missing flag value'; value=${ARGS[i+1]}; i=$((i+1)) ;;
    -auth=*|-hostkey=*|-admin-token-file=*|-data-dir=*) value=${arg#*=}; arg=${arg%%=*} ;;
    *) continue ;;
  esac
  [[ $value == /* ]] || value="$WORKDIR/$value"
  value=$(realpath -m -- "$value")
  case "$arg" in -auth) AUTH=$value ;; -hostkey) HOSTKEY=$value ;; -admin-token-file) TOKEN=$value ;; -data-dir) DATA=$value ;; esac
done
AUTH=$(realpath -e -- "$AUTH") || die 'Authorization path is unavailable'
HOSTKEY=$(realpath -e -- "$HOSTKEY") || die 'Host key path is unavailable'
TOKEN=$(realpath -m -- "$TOKEN")
DATA=$(realpath -e -- "$DATA") || die 'Data directory is unavailable'
[[ -f $AUTH && -f $HOSTKEY && -d $DATA ]] || die 'Existing host key, authorization or state missing; refusing replacement credentials.'
# Old token arguments are preserved for service compatibility, but tokens are
# not an authentication mechanism and a missing token must not block upgrades.
[[ ! -e $TOKEN || -f $TOKEN ]] || die 'Legacy token path is not a regular file'
for file in "$DATA"/tunnel-server.db "$DATA"/tunnel-server.db-wal "$DATA"/tunnel-server.db-shm; do
  [[ ! -L $file ]] || die 'Symlinked database/WAL/SHM requires an offline consistent backup.'
done
"$STAGED" -check-auth -auth "$AUTH" || die 'Authorization preflight failed; old service is untouched.'
NEW_AUTH="$DATA/authorized_keys"
[[ ! -L $NEW_AUTH ]] || die 'Authorization state must not be a symlink'
if [[ $AUTH != "$NEW_AUTH" && -e $NEW_AUTH ]] && ! cmp -s -- "$AUTH" "$NEW_AUTH"; then die 'Old/new authorization files differ; refusing overwrite or merge.'; fi
FOUND_AUTH=0
for ((i=1; i<${#ARGS[@]}; i++)); do
  case "${ARGS[i]}" in -auth|--auth) ARGS[i+1]=$NEW_AUTH; FOUND_AUTH=1; i=$((i+1)) ;; -auth=*|--auth=*) ARGS[i]="-auth=$NEW_AUTH"; FOUND_AUTH=1 ;; esac
done
((FOUND_AUTH)) || ARGS+=(-auth "$NEW_AUTH")
unit_quote() {
  local v=$1
  [[ $v != *$'\n'* && $v != *$'\r'* ]] || die 'Newline in service arguments'
  v=${v//\\/\\\\}; v=${v//\"/\\\"}; v=${v//%/%%}
  if [[ ${2:-0} == 1 ]]; then v=${v//\$/\$\$}; fi
  printf '"%s"' "$v"
}
for arg in "${ARGS[@]}" "$DATA" "$HOSTKEY" "$TOKEN" "$WORKDIR"; do unit_quote "$arg" >/dev/null; done
RUN_USER=$(systemctl show "$SERVICE" -p User --value); RUN_USER=${RUN_USER:-root}
RUN_GROUP=$(systemctl show "$SERVICE" -p Group --value); RUN_GROUP=${RUN_GROUP:-$(id -gn "$RUN_USER")}
FRAGMENT=$(systemctl show "$SERVICE" -p FragmentPath --value)
DROPINS=/etc/systemd/system/tunnel-server.service.d
BACKUP="/var/backups/tunnel-server/$(date -u +%Y%m%dT%H%M%SZ)-$$"
[[ $BACKUP != "$DATA" && $BACKUP != "${DATA%/}/"* ]] || die 'Backup destination is inside the data directory; use an offline backup elsewhere.'
mkdir -p -- "$BACKUP"; chmod 700 "$BACKUP"
cp -a -- "$BINARY" "$BACKUP/tunnel-server"
cp -a -- "$FRAGMENT" "$BACKUP/original.service"
systemctl cat "$SERVICE" > "$BACKUP/effective-unit.txt"
[[ ! -d $DROPINS ]] || cp -a -- "$DROPINS" "$BACKUP/drop-ins"
LIMITS=()
for limit in LimitNOFILE=4096 TasksMax=256 MemoryHigh=768M MemoryMax=1G; do
  property=${limit%%=*}; value=${limit#*=}
  if grep -Eq "^[[:space:]]*${property}[[:space:]]*=" "$BACKUP/effective-unit.txt"; then
    value=$(systemctl show "$SERVICE" -p "$property" --value)
    [[ $value =~ ^([0-9]+|infinity)$ ]] || die 'Cannot preserve an existing resource limit'
    if [[ $property == LimitNOFILE ]]; then
      soft=$(systemctl show "$SERVICE" -p LimitNOFILESoft --value)
      [[ $soft =~ ^([0-9]+|infinity)$ ]] || die 'Cannot preserve the open-file soft limit'
      value="$soft:$value"
    fi
  fi
  LIMITS+=("$property=$value")
done
RESTART_OLD=0
restore_on_error() {
  local status=$?
  trap - ERR
  if ((RESTART_OLD)); then
    systemctl start "$SERVICE" || printf 'Could not restart the original service; inspect systemctl status.\n' >&2
  fi
  printf 'Upgrade failed; backup location: %s\n' "$BACKUP" >&2
  exit "$status"
}
trap restore_on_error ERR
systemctl stop "$SERVICE"
RESTART_OLD=1
printf 'Backup: %s\n' "$BACKUP"
cp -a -- "$DATA" "$BACKUP/data"
cp -a -- "$HOSTKEY" "$BACKUP/host_key"
[[ ! -f $TOKEN ]] || cp -a -- "$TOKEN" "$BACKUP/admin.token"
cp -a -- "$AUTH" "$BACKUP/authorized_keys"
printf 'binary=%q\ndata=%q\nauth=%q\nhostkey=%q\ntoken=%q\nfragment=%q\n' "$BINARY" "$DATA" "$AUTH" "$HOSTKEY" "$TOKEN" "$FRAGMENT" > "$BACKUP/paths.txt"
[[ -e $NEW_AUTH ]] || install -m 0600 -- "$AUTH" "$NEW_AUTH"
chown "$RUN_USER:$RUN_GROUP" "$NEW_AUTH" "$DATA"
chmod 600 "$NEW_AUTH"; chmod 700 "$DATA"
mkdir -p -- "$DROPINS"
RESTART_OLD=0
{
  printf '[Service]\nExecStart=\nExecStart='
  for arg in "${ARGS[@]}"; do unit_quote "$arg" 1; printf ' '; done
  printf '\nWorkingDirectory='; unit_quote "$WORKDIR"
  printf '\nReadWritePaths='; unit_quote "$DATA"
  printf '\nReadOnlyPaths='; unit_quote "$HOSTKEY"
  if [[ -f $TOKEN ]]; then printf ' '; unit_quote "$TOKEN"; fi
  printf '\n'
  printf '%s\n' "${LIMITS[@]}"
} > "$DROPINS/90-tunnelx-upgrade.conf"
chmod 644 "$DROPINS/90-tunnelx-upgrade.conf"
mv -f -- "$STAGED" "$BINARY"
systemctl daemon-reload
systemctl start "$SERVICE" || die "Startup failed. Backup: $BACKUP. Restore matching program AND data per deploy/README.md."
sleep 2
systemctl is-active --quiet "$SERVICE" || die "Service failed after startup. Backup: $BACKUP."
NEW_PID=$(systemctl show "$SERVICE" -p MainPID --value)
[[ $(readlink -f "/proc/$NEW_PID/cwd") == "$WORKDIR" ]] || { systemctl stop "$SERVICE"; die "Effective working directory changed. Backup: $BACKUP."; }
mapfile -d '' -t ACTUAL < "/proc/$NEW_PID/cmdline"
if ((${#ACTUAL[@]} != ${#ARGS[@]})); then systemctl stop "$SERVICE"; die "A later unit override changed startup arguments. Backup: $BACKUP."; fi
for ((i=0; i<${#ARGS[@]}; i++)); do
  if [[ ${ACTUAL[i]} != "${ARGS[i]}" ]]; then systemctl stop "$SERVICE"; die "Effective arguments differ from preserved arguments. Backup: $BACKUP."; fi
done
printf 'Started %s. Original keys, ports and other arguments preserved. Authorization: %s. Backup: %s\n' "$VERSION" "$NEW_AUTH" "$BACKUP"
printf '%s\n' 'Upgrade clients too. Existing business connections are interrupted by restart and then reconnect.'
printf '%s\n' 'The console now requires an administrator account; legacy tokens no longer grant access.'
printf 'To create/recover an administrator, stop %s, run as %s: %q -data-dir %q -admin-account admin, then start the service. The password is prompted securely.\n' "$SERVICE" "$RUN_USER" "$BINARY" "$DATA"
