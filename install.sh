#!/usr/bin/env bash
set -euo pipefail

# 此入口面向公开用户，所有安装文件均从公开 Release 获取。
repository="onprs/Emby-Auto"
install_dir="/opt/emby-auto"
config_dir="/etc/emby-auto"
state_dir="/var/lib/emby-auto"
storage_dir="/srv/emby-auto"
unit_dir="/etc/systemd/system"
nginx_dir="/etc/nginx"
postgres_dir="/etc/postgresql/17/emby_auto"
lock_dir="/run/emby-auto-install"
version="latest"
service_user=""
web_port=""
api_port=""
database_port=""
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
    '  --port <端口>             网页端口，默认 18080' \
    '  --api-port <端口>         API 回环端口，默认 18081' \
    '  --database-port <端口>    独立 PostgreSQL 回环端口，默认 18082' \
    '  --help                    显示帮助' \
    '' \
    '安装后通过本机浏览器或 SSH 隧道访问网页，填写外部服务信息。' \
    '重复执行保留已安装版本、数据库及配置；升级请参阅部署文档。'
}

parse_arguments() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h) usage; exit 0 ;;
      --version|--user|--port|--api-port|--database-port)
        [[ $# -ge 2 && -n "$2" ]] || fail "$1 缺少参数。"
        case "$1" in
          --version) version="$2" ;;
          --user) service_user="$2" ;;
          --port) web_port="$2" ;;
          --api-port) api_port="$2" ;;
          --database-port) database_port="$2" ;;
        esac
        shift 2
        ;;
      *) fail "未知参数：$1；使用 --help 查看用法。" ;;
    esac
  done
  [[ "$version" == latest || "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || fail 'Release 版本格式无效。'
  [[ -z "$service_user" || "$service_user" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '服务账户名称无效。'
}

validate_ports() {
  local port
  for port in "$web_port" "$api_port" "$database_port"; do
    if [[ ! "$port" =~ ^[1-9][0-9]{3,4}$ ]] || (( port < 1024 || port > 65535 )); then
      fail '端口必须是 1024 到 65535 之间的整数。'
    fi
  done
  [[ "$web_port" != "$api_port" && "$web_port" != "$database_port" && "$api_port" != "$database_port" ]] || fail '网页、API 和数据库端口必须互不相同。'
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
  distribution_codename="${VERSION_CODENAME:-}"
  [[ "$distribution_codename" =~ ^[a-z]+$ ]] || fail '无法识别发行版代号。'
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
    local key requested saved
    for key in service_user web_port api_port database_port; do
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
    database_password="$(read_setting database_password)"
    [[ "$database_password" =~ ^[a-f0-9]{64}$ ]] || fail '安装记录中的数据库凭据无效。'
    [[ "$service_user" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '安装记录中的账户名称无效。'
  else
    local existing
    for existing in "$config_dir/runtime.env" "$install_dir/current" "$state_dir/bootstrap.json" \
      "$unit_dir/emby-auto-api.service" "$unit_dir/emby-auto-worker.service" \
      "$unit_dir/emby-auto-api.service.d" "$unit_dir/emby-auto-worker.service.d" \
      "$nginx_dir/conf.d/emby-auto.conf" "$nginx_dir/emby-auto-locations.conf" "$postgres_dir"; do
      [[ ! -e "$existing" && ! -L "$existing" ]] || fail '检测到已有手动部署，安装器不会接管；请按部署文档升级。'
    done
    service_user="${service_user:-emby-auto}"
    web_port="${web_port:-18080}"
    api_port="${api_port:-18081}"
    database_port="${database_port:-18082}"
    database_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  fi
  validate_ports
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
    config/emby-auto-api.service config/emby-auto-worker.service config/nginx.conf config/emby-auto-locations.conf; do
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

check_ports() {
  # 重试失败的安装时，允许安装器自身的实例继续占用原端口。
  [[ ! -f "$config_dir/installer.env" ]] || return 0
  local port
  for port in "$web_port" "$api_port" "$database_port"; do
    [[ -z "$(ss -H -ltn "sport = :$port")" ]] || fail "端口 $port 已被占用，请使用端口选项重新安装。"
  done
}

install_dependencies() {
  log '安装 PostgreSQL 17、Nginx 和 FFmpeg……'
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates
  if ! apt-cache show postgresql-17 >/dev/null 2>&1; then
    install -d -m 0755 /usr/share/keyrings
    fetch 'https://www.postgresql.org/media/keys/ACCC4CF8.asc' "$temporary/postgresql.asc"
    install -m 0644 "$temporary/postgresql.asc" /usr/share/keyrings/emby-auto-postgresql.asc
    printf 'Types: deb\nURIs: https://apt.postgresql.org/pub/repos/apt\nSuites: %s-pgdg\nComponents: main\nSigned-By: /usr/share/keyrings/emby-auto-postgresql.asc\n' \
      "$distribution_codename" >/etc/apt/sources.list.d/emby-auto-postgresql.sources
    apt-get update
  fi
  apt-get install -y --no-install-recommends postgresql-17 nginx ffmpeg iproute2
  check_ports
  nginx -t || fail '现有 Nginx 配置无效，请修复后重试。'
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
    (umask 077; printf 'service_user=%s\nweb_port=%s\napi_port=%s\ndatabase_port=%s\ndatabase_password=%s\nversion=%s\n' \
      "$service_user" "$web_port" "$api_port" "$database_port" "$database_password" "$version" >"$config_dir/installer.env.next")
    mv -f "$config_dir/installer.env.next" "$config_dir/installer.env"
  fi
}

configure_database() {
  log '配置独立 PostgreSQL 实例……'
  if [[ ! -d "$postgres_dir" ]]; then
    # PostgreSQL 服务模板将实例名中的连字符解码为路径分隔符，集群名使用下划线。
    pg_createcluster --port "$database_port" --start-conf=auto 17 emby_auto -- \
      --encoding=UTF8 --locale=C.UTF-8 --auth-local=peer --auth-host=scram-sha-256
  fi
  # 辅助工具会把 IPv4 地址误判为数值，移除旧值后显式写入带引号的字符串。
  pg_conftool 17 emby_auto remove listen_addresses
  printf "\nlisten_addresses = '127.0.0.1'\n" >>"$postgres_dir/postgresql.conf"
  if ! systemctl enable --now postgresql@17-emby_auto.service || \
    ! systemctl is-active --quiet postgresql@17-emby_auto.service; then
    fail 'PostgreSQL 实例启动失败，请运行 systemctl status postgresql@17-emby_auto.service 查看状态。'
  fi
  # 密码只通过标准输入传递，不出现在命令参数或安装日志中。
  if ! {
    printf "SELECT 'CREATE ROLE emby_auto LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'emby_auto')\\gexec\n"
    printf "ALTER ROLE emby_auto PASSWORD '%s';\n" "$database_password"
    printf "SELECT 'CREATE DATABASE emby_auto OWNER emby_auto' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'emby_auto')\\gexec\n"
  } | runuser -u postgres -- psql --no-psqlrc --quiet --set=ON_ERROR_STOP=1 \
    --host=/var/run/postgresql --port="$database_port" --dbname=postgres >"$temporary/database.log" 2>&1; then
    fail '创建应用数据库失败，请检查 PostgreSQL 服务。数据库凭据保留在受限配置文件中。'
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
    (umask 077; printf 'APP_ENV=production\nAPI_ADDRESS=127.0.0.1:%s\nBOOTSTRAP_CONFIG_PATH=%s/bootstrap.json\nDATABASE_URL=postgres://emby_auto:%s@127.0.0.1:%s/emby_auto?sslmode=disable\nDATABASE_MANAGED_EXTERNALLY=true\nLOG_LEVEL=info\nSHUTDOWN_TIMEOUT=30s\nSESSION_COOKIE_SECURE=false\nWORKER_CONCURRENCY=4\nWORKER_TRANSCODE_MAX_CONCURRENCY=1\nEMBY_MEDIA_OWNER_UID=%s\n' \
      "$api_port" "$state_dir" "$database_password" "$database_port" "$service_uid" >"$config_dir/runtime.env.next")
    chown "root:$service_gid" "$config_dir/runtime.env.next"
    chmod 0640 "$config_dir/runtime.env.next"
    mv -f "$config_dir/runtime.env.next" "$config_dir/runtime.env"
  fi
  for service in api worker; do
    install -m 0644 "$release_source/config/emby-auto-$service.service" "$unit_dir/emby-auto-$service.service"
    install -d -m 0755 "$unit_dir/emby-auto-$service.service.d"
    printf '[Service]\nUser=%s\nGroup=%s\n' "$service_user" "$service_gid" >"$unit_dir/emby-auto-$service.service.d/installer.conf"
  done
  install -m 0644 "$release_source/config/emby-auto-locations.conf" "$nginx_dir/emby-auto-locations.conf"
  awk -v port="$web_port" -v api="$api_port" '
    $1 == "listen" { sub(/127\.0\.0\.1:18080/, "127.0.0.1:" port) }
    $1 == "server" { sub(/127\.0\.0\.1:18081/, "127.0.0.1:" api) }
    { print }
  ' "$release_source/config/nginx.conf" >"$nginx_dir/conf.d/emby-auto.conf"
  nginx -t || fail 'Nginx 配置检查失败，应用尚未启动。'
  systemctl daemon-reload
  services_started=1
  systemctl enable --now emby-auto-api.service emby-auto-worker.service nginx.service
  systemctl reload nginx.service
}

check_health() {
  local attempt
  for ((attempt = 0; attempt < 30; attempt++)); do
    if curl --fail --silent --noproxy '*' --max-time 2 "http://127.0.0.1:$web_port/api/v1/setup/status" -o "$temporary/setup-status.json" && \
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
  log '随后在自己的电脑打开同一网页地址，设置管理员并填写 qBittorrent、Emby 和 TMDb 信息。'
  log '数据库已自动配置。qBittorrent 的下载路径需要与安装向导中的下载目录一致。'
  log "部署、HTTPS、备份与升级：https://github.com/$repository/blob/main/docs/direct-deployment.md"
}

cleanup() {
  local status=$?
  if (( status != 0 )); then
    if (( services_started == 1 && installation_completed == 0 )); then
      systemctl stop emby-auto-worker.service emby-auto-api.service || true
    fi
    printf '%s\n' '安装未完成。已生成的数据库、凭据和媒体目录会保留；修复原因后可使用相同命令重试。' >&2
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
  for command in curl tar sha256sum apt-get apt-cache awk flock runuser; do
    command -v "$command" >/dev/null || fail "缺少 $command，请先安装该系统工具。"
  done
  install -d -m 0755 "$lock_dir"
  exec 9>"$lock_dir/lock"
  flock -n 9 || fail '另一项安装正在执行。'
  load_settings
  check_account
  if command -v ss >/dev/null; then
    check_ports
  fi
  temporary="$(mktemp -d)"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  if [[ -f "$config_dir/installed" ]]; then
    [[ "$version" == latest || "$version" == "$(read_setting version)" ]] || fail '已有其它版本，请按部署文档升级。'
    check_health
    installation_completed=1
    show_access
    return
  fi
  download_release
  install_dependencies
  prepare_account_and_storage
  configure_database
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
