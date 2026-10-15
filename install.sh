#!/usr/bin/env bash
set -euo pipefail

# 此入口面向公开用户，所有应用文件均从公开 Release 获取。
repository="onprs/Emby-Auto"
install_dir="/opt/emby-auto"
config_dir="/etc/emby-auto"
state_dir="/var/lib/emby-auto"
storage_dir="/srv/emby-auto"
unit_dir="/etc/systemd/system"
lock_dir="/run/emby-auto-install"
version="latest"
service_user=""
web_port=""
temporary=""
services_started=0
installation_completed=0

log() { printf '%s\n' "$*"; }
fail() { printf '安装失败：%s\n' "$*" >&2; exit 1; }

usage() {
  printf '%s\n' \
    'Emby Auto 一键安装（无需 Docker、Git 或编译工具）' \
    '支持 Debian 12/13、Ubuntu 22.04/24.04/26.04，amd64/arm64，systemd。' \
    '' \
    '用法：sudo bash install.sh [选项]' \
    '  --version <v版本号>       安装指定 Release，默认最新正式版本' \
    '  --user <现有账户>         使用媒体文件所有者；默认创建 emby-auto' \
    '  --port <端口>             网页与 API 回环端口，默认 18080' \
    '  --help                    显示帮助' \
    '' \
    '安装应用和 FFmpeg；请自行准备 PostgreSQL、qBittorrent 与 Emby。' \
    '安装后通过本机浏览器或 SSH 隧道打开初始化向导，填写数据库和外部服务信息。' \
    '网络访问可自行选用反向代理并配置 HTTPS。' \
    '重复执行保留已安装版本、数据及配置；升级请参阅部署文档。'
}

parse_arguments() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h) usage; exit 0 ;;
      --version|--user|--port)
        [[ $# -ge 2 && -n "$2" ]] || fail "$1 缺少参数。"
        case "$1" in
          --version) version="$2" ;;
          --user) service_user="$2" ;;
          --port) web_port="$2" ;;
        esac
        shift 2
        ;;
      *) fail "未知参数：$1；使用 --help 查看用法。" ;;
    esac
  done
  [[ "$version" == latest || "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || fail 'Release 版本格式无效。'
  [[ -z "$service_user" || "$service_user" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '服务账户名称无效。'
}

validate_port() {
  if [[ ! "$web_port" =~ ^[1-9][0-9]{3,4}$ ]] || (( web_port < 1024 || web_port > 65535 )); then
    fail '端口必须是 1024 到 65535 之间的整数。'
  fi
}

check_platform() {
  [[ "$(uname -s)" == Linux ]] || fail '此安装器支持 Linux；请在支持的 Linux 设备上执行。'
  [[ -d /run/systemd/system ]] || fail '需要正在运行的 systemd；WSL 请先启用 systemd。'
  [[ -r /etc/os-release ]] || fail '无法识别 Linux 发行版。'
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-}:${VERSION_ID:-}" in
    debian:12|debian:13|ubuntu:22.04|ubuntu:24.04|ubuntu:26.04) ;;
    *) fail '自动安装支持 Debian 12/13、Ubuntu 22.04/24.04/26.04。其它 Linux 请使用手动部署。' ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) target_arch=amd64 ;;
    aarch64|arm64) target_arch=arm64 ;;
    *) fail '需要 64 位 amd64 或 arm64 设备。' ;;
  esac
  [[ "$(dpkg --print-architecture)" == "$target_arch" ]] || fail '系统用户空间与 CPU 架构不匹配。'
}

require_root() {
  (( EUID == 0 )) || fail '请使用 sudo 或 root 运行安装命令。'
}

read_setting() {
  awk -v key="$1" 'index($0, key "=") == 1 {print substr($0, length(key) + 2)}' "$config_dir/installer.env"
}

load_settings() {
  if [[ -f "$config_dir/installer.env" ]]; then
    [[ "$(read_setting installer_format)" == 2 ]] || fail '检测到旧版安装记录，请按部署文档迁移；已有服务和数据保持原样。'
    local key requested saved
    for key in service_user web_port; do
      requested="${!key}"
      saved="$(read_setting "$key")"
      [[ -n "$saved" ]] || fail '安装记录不完整，请检查 /etc/emby-auto/installer.env。'
      [[ -z "$requested" || "$requested" == "$saved" ]] || fail '已有安装使用不同的账户或端口，请保留原参数。'
      printf -v "$key" '%s' "$saved"
    done
    local saved_version
    saved_version="$(read_setting version)"
    [[ "$saved_version" == latest || "$saved_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || fail '安装记录中的版本无效。'
    [[ "$version" == latest || "$version" == "$saved_version" ]] || fail '已有其它版本，请按部署文档升级。'
    version="$saved_version"
    [[ "$service_user" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '安装记录中的账户名称无效。'
  else
    local existing
    for existing in "$config_dir/runtime.env" "$install_dir/current" "$state_dir/bootstrap.json" "$state_dir/bootstrap.json.complete" \
      "$unit_dir/emby-auto-api.service" "$unit_dir/emby-auto-worker.service" \
      "$unit_dir/emby-auto-api.service.d" "$unit_dir/emby-auto-worker.service.d"; do
      [[ ! -e "$existing" && ! -L "$existing" ]] || fail '检测到已有手动部署，安装器不会接管；请按部署文档迁移或升级。'
    done
    service_user="${service_user:-emby-auto}"
    web_port="${web_port:-18080}"
  fi
  validate_port
}

check_account() {
  if id "$service_user" >/dev/null 2>&1; then
    service_uid="$(id -u "$service_user")"
    service_gid="$(id -g "$service_user")"
    (( service_uid > 0 && service_gid > 0 )) || fail '服务账户的 UID 和 GID 必须大于 0。'
  else
    [[ "$service_user" == emby-auto ]] || fail '请先创建指定的媒体账户，再执行安装。'
  fi
}

fetch() {
  curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --connect-timeout 15 --max-time 900 --retry 3 --retry-delay 2 "$1" -o "$2"
}

verify_archive() {
  local archive="$1" checksum entry type
  checksum="$(awk -v name="$archive" '$2 == name {print $1}' "$temporary/SHA256SUMS")"
  [[ "$checksum" =~ ^[a-fA-F0-9]{64}$ ]] || fail 'Release 校验文件中缺少唯一的发布包校验和。'
  (cd "$temporary" && printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status) || fail '发布包 SHA-256 校验失败，请重新下载。'
  archive_root="${archive%-linux-*}"
  tar -tzf "$temporary/$archive" >"$temporary/archive.list"
  while IFS= read -r entry; do
    [[ "$entry" == "$archive_root/"* && "/$entry/" != *'/../'* && "/$entry/" != *'/./'* ]] || fail '发布包包含无效路径。'
  done <"$temporary/archive.list"
  tar -tvzf "$temporary/$archive" >"$temporary/archive.types"
  while IFS= read -r type; do
    [[ "$type" == -* || "$type" == d* ]] || fail '发布包包含不支持的链接或特殊文件。'
  done <"$temporary/archive.types"
  tar -xzf "$temporary/$archive" --no-same-owner --no-same-permissions -C "$temporary"
  release_source="$temporary/$archive_root"
  (cd "$release_source" && sha256sum --check --status SHA256SUMS) || fail '发布包内容校验失败。'
  local required
  for required in bin/emby-auto-api bin/emby-auto-worker web/index.html \
    config/emby-auto-api.service config/emby-auto-worker.service; do
    [[ -s "$release_source/$required" ]] || fail "发布包缺少 $required。"
  done
  release_digest="$checksum"
}

download_release() {
  local base_url archive
  if [[ "$version" == latest ]]; then
    base_url="https://github.com/$repository/releases/latest/download"
  else
    base_url="https://github.com/$repository/releases/download/$version"
  fi
  archive="emby-auto-direct-$target_arch-linux-$target_arch.tar.gz"
  log "下载 Linux $target_arch 发布包（$version）……"
  fetch "$base_url/$archive" "$temporary/$archive" || fail '无法下载发布包，请检查网络和 Release 版本。'
  fetch "$base_url/SHA256SUMS" "$temporary/SHA256SUMS" || fail '无法下载 Release 校验文件。'
  verify_archive "$archive"
}

check_port() {
  [[ ! -f "$config_dir/installer.env" ]] || return 0
  if command -v ss >/dev/null; then
    [[ -z "$(ss -H -ltn "sport = :$web_port")" ]] || fail "端口 $web_port 已被占用，请使用 --port 重新安装。"
  elif (exec 3<>"/dev/tcp/127.0.0.1/$web_port") 2>/dev/null; then
    fail "端口 $web_port 已被占用，请使用 --port 重新安装。"
  fi
}

install_dependencies() {
  log '安装 FFmpeg 与 ffprobe……'
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates ffmpeg
  check_port
}

prepare_account_and_storage() {
  if ! id "$service_user" >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "$state_dir" --no-create-home --shell /usr/sbin/nologin "$service_user"
  fi
  check_account
  local directory
  local directories=("$state_dir" "$storage_dir/downloads" "$storage_dir/work" "$storage_dir/staging"
    "$storage_dir/media/anime" "$storage_dir/media/movies")
  # 先检查已有目录，避免权限失败后留下属于错误账户的新目录。
  for directory in "${directories[@]}"; do
    if [[ ! -e "$directory" && ! -L "$directory" ]]; then
      continue
    fi
    if [[ ! -d "$directory" ]] || ! runuser -u "$service_user" -- test -r "$directory" || \
      ! runuser -u "$service_user" -- test -w "$directory" || ! runuser -u "$service_user" -- test -x "$directory"; then
      fail "账户 $service_user 无法读写 $directory，请调整目录权限或使用 --user 指定媒体所有者。"
    fi
  done
  for directory in "${directories[@]}"; do
    if [[ ! -e "$directory" && ! -L "$directory" ]]; then
      install -d -o "$service_uid" -g "$service_gid" -m 0770 "$directory"
    fi
  done
  install -d -m 0755 "$config_dir"
  if [[ ! -f "$config_dir/installer.env" ]]; then
    (umask 077; printf 'installer_format=2\nservice_user=%s\nweb_port=%s\nversion=%s\n' \
      "$service_user" "$web_port" "$version" >"$config_dir/installer.env.next")
    mv -f "$config_dir/installer.env.next" "$config_dir/installer.env"
  fi
}

install_application() {
  log '安装应用与服务……'
  local release_path="$install_dir/releases/${release_digest:0:16}" service
  install -d -m 0755 "$install_dir/releases"
  if [[ ! -d "$release_path" ]]; then
    rm -rf "$release_path.next"
    install -d -m 0755 "$release_path.next"
    cp -R "$release_source/." "$release_path.next/"
    find "$release_path.next" -type d -exec chmod 0755 {} +
    find "$release_path.next" -type f -exec chmod 0644 {} +
    chmod 0755 "$release_path.next"/bin/*
    mv "$release_path.next" "$release_path"
  fi
  ln -sfn "releases/${release_digest:0:16}" "$install_dir/current.next"
  mv -Tf "$install_dir/current.next" "$install_dir/current"
  if [[ ! -f "$config_dir/runtime.env" ]]; then
    (umask 077; printf 'APP_ENV=production\nAPI_ADDRESS=127.0.0.1:%s\nWEB_ROOT=%s/current/web\nBOOTSTRAP_CONFIG_PATH=%s/bootstrap.json\nLOG_LEVEL=info\nSHUTDOWN_TIMEOUT=30s\nSESSION_COOKIE_SECURE=false\nRIVER_GENERAL_WORKERS=10\nRIVER_TRANSCODE_WORKERS=1\nEMBY_MEDIA_OWNER_UID=%s\n' \
      "$web_port" "$install_dir" "$state_dir" "$service_uid" >"$config_dir/runtime.env.next")
    chown "root:$service_gid" "$config_dir/runtime.env.next"
    chmod 0640 "$config_dir/runtime.env.next"
    mv -f "$config_dir/runtime.env.next" "$config_dir/runtime.env"
  fi
  for service in api worker; do
    install -m 0644 "$release_source/config/emby-auto-$service.service" "$unit_dir/emby-auto-$service.service"
    install -d -m 0755 "$unit_dir/emby-auto-$service.service.d"
    printf '[Service]\nUser=%s\nGroup=%s\n' "$service_user" "$service_gid" >"$unit_dir/emby-auto-$service.service.d/installer.conf"
  done
  systemctl daemon-reload
  services_started=1
  systemctl enable --now emby-auto-api.service emby-auto-worker.service
}

check_health() {
  local attempt
  for ((attempt = 0; attempt < 30; attempt++)); do
    if curl --fail --silent --noproxy '*' --max-time 2 "http://127.0.0.1:$web_port/api/v1/setup/status" -o "$temporary/setup-status.json" && \
      curl --fail --silent --noproxy '*' --max-time 2 "http://127.0.0.1:$web_port/" -o "$temporary/index.html" && \
      systemctl is-active --quiet emby-auto-api.service && systemctl is-active --quiet emby-auto-worker.service; then
      return 0
    fi
    sleep 2
  done
  fail '服务未通过启动检查。请运行 journalctl -u emby-auto-api -u emby-auto-worker 查看日志，再重试安装。'
}

show_access() {
  log ''
  log "Emby Auto 已安装。浏览器打开：http://127.0.0.1:$web_port"
  log '远程设备可先在自己的电脑建立 SSH 隧道：'
  log "  ssh -L $web_port:127.0.0.1:$web_port 用户@设备地址"
  log '随后在自己的电脑打开同一网页地址，填写已准备好的 PostgreSQL 连接、管理员及外部服务信息。'
  log 'qBittorrent 的下载路径需要与初始化向导中的下载目录一致。'
  log "部署、HTTPS、备份与升级：https://github.com/$repository/blob/main/docs/direct-deployment.md"
}

cleanup() {
  local status=$?
  if (( status != 0 )); then
    if (( services_started == 1 && installation_completed == 0 )); then
      systemctl stop emby-auto-worker.service emby-auto-api.service || true
    fi
    printf '%s\n' '安装未完成。已生成的应用配置和媒体目录会保留；修复原因后可使用相同命令重试。' >&2
  fi
  if [[ -n "$temporary" ]]; then
    rm -rf "$temporary"
  fi
}

main() {
  parse_arguments "$@"
  check_platform
  require_root
  export PATH="/usr/sbin:/usr/bin:/sbin:/bin:$PATH"
  export LC_ALL=C
  local command
  for command in curl tar sha256sum apt-get awk flock runuser; do
    command -v "$command" >/dev/null || fail "缺少 $command，请先安装该系统工具。"
  done
  install -d -m 0755 "$lock_dir"
  exec 9>"$lock_dir/lock"
  flock -n 9 || fail '另一项安装正在执行。'
  load_settings
  check_account
  check_port
  temporary="$(mktemp -d)"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  if [[ -f "$config_dir/installed" ]]; then
    check_health
    installation_completed=1
    show_access
    return
  fi
  download_release
  install_dependencies
  prepare_account_and_storage
  install_application
  check_health
  touch "$config_dir/installed"
  installation_completed=1
  show_access
}

# 保持入口位于文件末尾，下载中断时不会执行未完整定义的安装流程。
if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
