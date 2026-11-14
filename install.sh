#!/usr/bin/env bash
set -euo pipefail

# 此入口面向公开用户，所有应用文件均从公开 Release 获取。
repository="onprs/Emby-Auto"
installer_format=3
install_dir="/opt/emby-auto"
config_dir="/etc/emby-auto"
state_dir="/var/lib/emby-auto"
storage_dir="/srv/emby-auto"
unit_dir="/etc/systemd/system"
lock_dir="/run/emby-auto-install"
version="latest"
service_user=""
share_group=""
web_port=""
release_archive=""
release_checksums=""
prepare_only=0
finalize_only=0
temporary=""
services_started=0
installation_completed=0
worker_paused=0

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
    '  --share-group <共享组>    与 qBittorrent 等账户共享下载目录的现有用户组' \
    '  --port <端口>             网页与 API 回环端口，默认 18080' \
    '  --archive <发布包>        使用已下载的对应架构发布包' \
    '  --checksums <校验文件>    本地发布包对应的 SHA256SUMS' \
    '  --no-start                只准备应用和服务，供迁移前使用' \
    '  --finalize                核验已准备并启动的服务，完成安装登记' \
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
      --no-start) prepare_only=1; shift ;;
      --finalize) finalize_only=1; shift ;;
      --version|--user|--share-group|--port|--archive|--checksums)
        [[ $# -ge 2 && -n "$2" ]] || fail "$1 缺少参数。"
        case "$1" in
          --version) version="$2" ;;
          --user) service_user="$2" ;;
          --share-group) share_group="$2" ;;
          --port) web_port="$2" ;;
          --archive) release_archive="$2" ;;
          --checksums) release_checksums="$2" ;;
        esac
        shift 2
        ;;
      *) fail "未知参数：$1；使用 --help 查看用法。" ;;
    esac
  done
  (( prepare_only == 0 || finalize_only == 0 )) || fail '--no-start 与 --finalize 不能同时使用。'
  (( finalize_only == 0 )) || [[ -z "$release_archive" && -z "$release_checksums" ]] || fail '--finalize 不接受发布包参数。'
  [[ "$version" == latest || "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || fail 'Release 版本格式无效。'
  [[ -z "$service_user" || "$service_user" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '服务账户名称无效。'
  [[ -z "$share_group" || "$share_group" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '共享组名称无效。'
  if [[ -n "$release_archive" || -n "$release_checksums" ]]; then
    [[ -f "$release_archive" && -f "$release_checksums" ]] || fail '--archive 与 --checksums 必须同时指定可读取的文件。'
  fi
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

validate_application_paths() {
  local directory mode
  for directory in "$install_dir" "$install_dir/releases" "$config_dir"; do
    [[ ! -L "$directory" ]] || fail '应用安装目录不能是符号链接。'
    [[ ! -e "$directory" || -d "$directory" ]] || fail '应用安装位置必须是目录。'
    if (( EUID == 0 )) && [[ -d "$directory" ]]; then
      [[ "$(stat -c %u "$directory")" == 0 ]] || fail '应用安装目录必须由 root 管理。'
      mode="$(stat -c %a "$directory")"
      (( (8#$mode & 0022) == 0 )) || fail '应用安装目录不能允许组或其他用户写入。'
    fi
  done
}

read_setting() {
  awk -v key="$1" 'index($0, key "=") == 1 {print substr($0, length(key) + 2)}' "$config_dir/installer.env"
}

load_settings() {
  if [[ -f "$config_dir/installer.env" ]]; then
    [[ "$(read_setting installer_format)" == "$installer_format" ]] || fail '安装记录版本不兼容，请按部署文档迁移；已有服务和数据保持原样。'
    local key requested saved
    for key in service_user web_port; do
      requested="${!key}"
      saved="$(read_setting "$key")"
      [[ -n "$saved" ]] || fail '安装记录不完整，请检查 /etc/emby-auto/installer.env。'
      [[ -z "$requested" || "$requested" == "$saved" ]] || fail '已有安装使用不同的账户或端口，请保留原参数。'
      printf -v "$key" '%s' "$saved"
    done
    local saved_version saved_group
    saved_version="$(read_setting version)"
    [[ "$saved_version" == latest || "$saved_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || fail '安装记录中的版本无效。'
    [[ "$version" == latest || "$version" == "$saved_version" ]] || fail '已有其它版本，请按部署文档升级。'
    version="$saved_version"
    [[ "$service_user" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '安装记录中的账户名称无效。'
    saved_group="$(read_setting share_group)"
    [[ -z "$saved_group" || "$saved_group" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail '安装记录中的共享组名称无效。'
    [[ -z "$share_group" || -z "$saved_group" || "$share_group" == "$saved_group" ]] || fail '已有安装使用不同的共享组，请保留原参数或按部署文档调整目录权限。'
    share_group="${saved_group:-$share_group}"
  else
    local existing
    for existing in "$config_dir/runtime.env" "$install_dir/current" "$state_dir/bootstrap.json" "$state_dir/bootstrap.json.complete" \
      "$unit_dir/emby-auto-api.service" "$unit_dir/emby-auto-worker.service" "$unit_dir/emby-auto-systemd-control.service" \
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

resolve_share_group() {
  [[ -n "$share_group" ]] || return 0
  command -v getent >/dev/null 2>&1 || fail '缺少 getent，无法校验共享组。'
  getent group "$share_group" >/dev/null 2>&1 || fail "共享组 $share_group 不存在。"
  share_gid="$(getent group "$share_group" | cut -d: -f3)"
  [[ "$share_gid" =~ ^[0-9]+$ ]] && (( share_gid > 0 )) || fail "共享组 $share_group 的 GID 无效。"
}

write_share_group_setting() {
  [[ -n "$share_group" ]] || return 0
  local record="$config_dir/installer.env" next="$config_dir/installer.env.next" saved program
  [[ -f "$record" ]] || fail '安装记录缺失，无法写入共享组；请按部署文档恢复安装记录。'
  saved="$(read_setting share_group)"
  [[ "$saved" != "$share_group" ]] || return 0
  # 不同共享组已在 load_settings 拒绝；这里保留同一保护，写入前不修改成员与目录权限。
  [[ -z "$saved" ]] || fail '已有安装使用不同的共享组，请保留原参数或按部署文档调整目录权限。'
  # 幂等 upsert：已有行（含空值或重复行）统一替换为一条，缺失行插在 service_user 之后；
  # 其它字段原样保留，经同目录临时文件以 0600 原子替换后再回读校验。
  if grep -q '^share_group=' "$record"; then
    program='/^share_group=/ { if (!written) { printf "share_group=%s\n", group; written = 1 } next } { print }'
  else
    program='{ print } /^service_user=/ && !written { printf "share_group=%s\n", group; written = 1 } END { if (!written) printf "share_group=%s\n", group }'
  fi
  (umask 077; awk -v group="$share_group" "$program" "$record" >"$next") || {
    rm -f "$next"
    fail '无法写入安装记录中的共享组，共享组目录权限可能已应用；修复原因后用相同命令重试。'
  }
  chmod 0600 "$next" || {
    rm -f "$next"
    fail '无法设置安装记录的权限；共享组目录权限可能已应用，修复原因后重试。'
  }
  mv -f "$next" "$record" || {
    rm -f "$next"
    fail '无法更新安装记录中的共享组，共享组目录权限可能已应用；修复原因后用相同命令重试。'
  }
  [[ "$(read_setting share_group)" == "$share_group" ]] || fail '安装记录中的共享组未通过回读校验，请检查 /etc/emby-auto/installer.env。'
}

apply_share_group_request() {
  [[ -n "$share_group" ]] || return 0
  # 先确认能力，再修改成员与目录权限，避免缺少 setfacl 时留下不完整的成员变更。
  command -v setfacl >/dev/null 2>&1 || fail '缺少 setfacl，无法配置共享组目录权限；请先安装 acl 软件包。'
  ensure_share_group_membership
  apply_share_group_access
}

apply_share_group_access() {
  command -v setfacl >/dev/null 2>&1 || fail '缺少 setfacl，无法配置共享组目录权限；请先安装 acl 软件包。'
  local directory
  for directory in "$storage_dir/downloads" "$storage_dir/work" "$storage_dir/staging"; do
    apply_share_group_directory "$directory"
  done
  # 目录权限实际应用并校验后才写安装记录；写入失败以非零退出，不输出共享成功文案。
  write_share_group_setting
}

apply_share_group_directory() {
  local directory="${1:?用法: apply_share_group_directory <目录>}"
  [[ -d "$directory" ]] || fail "目录 $directory 不存在，无法应用共享组。"
  runuser -u "$service_user" -- test -w "$directory" || fail "账户 $service_user 无法写入 $directory。"
  chgrp "$share_group" "$directory" || fail "无法将 $directory 的属组改为共享组 $share_group。"
  chmod 2770 "$directory" || fail "无法设置 $directory 的 setgid 与组写权限。"
  setfacl -m "g:${share_group}:rwx" -m "m::rwx" "$directory" || fail "无法为 $directory 设置共享组 $share_group 的访问 ACL。"
  setfacl -d -m "g:${share_group}:rwx" -m "m::rwx" "$directory" || fail "无法为 $directory 设置共享组 $share_group 的默认 ACL。"
}

share_group_primary_group() {
  [[ "$(id -gn "$service_user" 2>/dev/null)" == "$share_group" ]]
}

share_group_member() {
  share_group_primary_group || id -nG "$service_user" 2>/dev/null | tr ' ' '\n' | grep -qxF "$share_group"
}

ensure_share_group_membership() {
  [[ -n "$share_group" ]] || return 0
  share_group_member && return 0
  command -v usermod >/dev/null 2>&1 || fail '缺少 usermod，无法将服务账户加入共享组。'
  usermod -aG "$share_group" "$service_user" || fail "无法将账户 $service_user 加入共享组 $share_group。"
  # 组成员关系对新进程生效后才能写入共享组目录；未生效说明需要重启相关服务。
  share_group_member || fail "账户 $service_user 加入共享组 $share_group 后未生效，请检查账户后重试。"
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
  for required in bin/emby-auto-api bin/emby-auto-worker bin/emby-auto-migration-check bin/emby-auto-host-control \
    bin/emby-auto-background-runtime bin/emby-auto-release-preflight bin/emby-auto-recover-stale-operations \
    bin/emby-auto-migrate-runtime-addresses bin/emby-auto-migrate-staging-ownership web/index.html \
    config/emby-auto-api.service config/emby-auto-worker.service config/emby-auto-systemd-control.service config/worker-runtime.sh; do
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
  if [[ -n "$release_archive" ]]; then
    cp -- "$release_archive" "$temporary/$archive"
    cp -- "$release_checksums" "$temporary/SHA256SUMS"
    verify_archive "$archive"
    return
  fi
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
  local need_media_tools=0 need_acl=0 packages=()
  command -v ffmpeg >/dev/null && command -v ffprobe >/dev/null || need_media_tools=1
  if [[ -n "$share_group" ]] && ! command -v setfacl >/dev/null 2>&1; then
    need_acl=1
  fi
  if (( need_media_tools == 0 && need_acl == 0 )); then
    log '复用已安装的 FFmpeg 与 ffprobe。'
    check_port
    return
  fi
  if (( need_media_tools == 1 )); then
    packages=(ca-certificates ffmpeg)
    log '安装 FFmpeg 与 ffprobe……'
  else
    log '安装目录 ACL 工具……'
  fi
  (( need_acl == 0 )) || packages+=(acl)
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends "${packages[@]}"
  check_port
}

prepare_account_and_storage() {
  if ! id "$service_user" >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "$state_dir" --no-create-home --shell /usr/sbin/nologin "$service_user"
  fi
  check_account
  ensure_share_group_membership
  local directory group_id
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
  group_id="${share_gid:-$service_gid}"
  for directory in "$storage_dir/downloads" "$storage_dir/work" "$storage_dir/staging"; do
    if [[ -e "$directory" || -L "$directory" ]]; then
      if [[ -n "$share_group" ]]; then
        # 已有目录补充共享组与 setgid，仅修改目录本身，不动递归内容；
        # 缺省 ACL 让 qBittorrent 的 umask 不影响新建子目录的组写权限。
        apply_share_group_directory "$directory"
      fi
      continue
    fi
    # setgid 让后续子目录继承共享组；默认 ACL 让 qBittorrent 的 umask 不影响组写权限。
    install -d -o "$service_uid" -g "$group_id" -m 2770 "$directory"
    if [[ -n "$share_group" ]]; then
      setfacl -m "g:${share_group}:rwx" -m "m::rwx" "$directory"
      setfacl -d -m "g:${share_group}:rwx" -m "m::rwx" "$directory"
    fi
  done
  for directory in "$storage_dir/media/anime" "$storage_dir/media/movies"; do
    if [[ -e "$directory" || -L "$directory" ]]; then
      continue
    fi
    # 媒体库根保留 other 的读与遍历权限，让独立运行的 Emby 账户能扫描。
    install -d -o "$service_uid" -g "$group_id" -m 2775 "$directory"
  done
  install -d -o "$service_uid" -g "$service_gid" -m 0770 "$state_dir"
  install -d -m 0755 "$config_dir"
  if [[ ! -f "$config_dir/installer.env" ]]; then
    (umask 077; printf 'installer_format=%s\nservice_user=%s\nshare_group=%s\nweb_port=%s\nversion=%s\n' \
      "$installer_format" "$service_user" "$share_group" "$web_port" "$version" >"$config_dir/installer.env.next")
    mv -f "$config_dir/installer.env.next" "$config_dir/installer.env"
  else
    write_share_group_setting
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
    chmod 0755 "$release_path.next"/bin/* "$release_path.next/config/worker-runtime.sh"
    mv "$release_path.next" "$release_path"
  fi
  ln -sfn "releases/${release_digest:0:16}" "$install_dir/current.next"
  mv -Tf "$install_dir/current.next" "$install_dir/current"
  if [[ ! -f "$config_dir/runtime.env" ]]; then
    (umask 077; printf 'APP_ENV=production\nAPI_ADDRESS=127.0.0.1:%s\nWEB_ROOT=%s/current/web\nBOOTSTRAP_CONFIG_PATH=%s/bootstrap.json\nLOG_LEVEL=info\nSHUTDOWN_TIMEOUT=30s\nSESSION_COOKIE_SECURE=false\nRIVER_GENERAL_WORKERS=10\nRIVER_TRANSCODE_WORKERS=1\nEMBY_MEDIA_OWNER_UID=%s\nEMBY_AUTO_HOST_CONTROL_EXECUTABLE=%s/current/bin/emby-auto-host-control\nEMBY_AUTO_HOST_CONTROL_SOCKET=/run/emby-auto-systemd/control.sock\n' \
      "$web_port" "$install_dir" "$state_dir" "$service_uid" "$install_dir" >"$config_dir/runtime.env.next")
    chown "root:$service_gid" "$config_dir/runtime.env.next"
    chmod 0640 "$config_dir/runtime.env.next"
    mv -f "$config_dir/runtime.env.next" "$config_dir/runtime.env"
  fi
  for service in api worker; do
    install -m 0644 "$release_source/config/emby-auto-$service.service" "$unit_dir/emby-auto-$service.service"
    install -d -m 0755 "$unit_dir/emby-auto-$service.service.d"
    printf '[Service]\nUser=%s\nGroup=%s\n' "$service_user" "$service_gid" >"$unit_dir/emby-auto-$service.service.d/installer.conf"
  done
  if [[ ! -f "$config_dir/host-control.env" ]]; then
    (umask 077; printf 'EMBY_AUTO_SERVICE_UID=%s\nEMBY_AUTO_SERVICE_GID=%s\n' "$service_uid" "$service_gid" >"$config_dir/host-control.env.next")
    mv -f "$config_dir/host-control.env.next" "$config_dir/host-control.env"
  fi
  install -m 0644 "$release_source/config/emby-auto-systemd-control.service" "$unit_dir/emby-auto-systemd-control.service"
  systemctl daemon-reload
}

verify_prepared_release() {
  [[ -f "$install_dir/current/SHA256SUMS" ]] || fail '已准备的应用缺少内容校验文件。'
  (cd "$install_dir/current" && sha256sum --check --status SHA256SUMS) || fail '已准备的应用内容已变化，请核对制品。'
  local binary
  for binary in "$install_dir/current"/bin/* "$install_dir/current/config/worker-runtime.sh"; do
    [[ -f "$binary" && -x "$binary" ]] || fail '已准备的应用缺少可执行文件。'
  done
}

start_services() {
  services_started=1
  systemctl enable --now emby-auto-systemd-control.service emby-auto-api.service emby-auto-worker.service
}

check_health() {
  local requirement="${1:-running}" attempt worker_state
  for ((attempt = 0; attempt < 30; attempt++)); do
    if curl --fail --silent --noproxy '*' --max-time 2 "http://127.0.0.1:$web_port/api/v1/setup/status" -o "$temporary/setup-status.json" && \
      curl --fail --silent --noproxy '*' --max-time 2 "http://127.0.0.1:$web_port/" -o "$temporary/index.html" && \
      systemctl is-active --quiet emby-auto-api.service && \
      systemctl is-active --quiet emby-auto-systemd-control.service; then
      worker_state="$(runuser -u "$service_user" -- env EMBY_AUTO_HOST_CONTROL_SOCKET=/run/emby-auto-systemd/control.sock "$install_dir/current/bin/emby-auto-host-control" worker-status 2>/dev/null || true)"
      case "$worker_state" in
        running)
          if systemctl is-active --quiet emby-auto-worker.service; then
            return 0
          fi
          ;;
        stopped)
          # Worker 正常停止后单元为 inactive；崩溃后为 failed。只有 inactive 才视为
          # 用户主动暂停：保留暂停意图，不自动启动 Worker，也不掩盖真实故障。
          if [[ "$requirement" == any ]] && ! systemctl is-active --quiet emby-auto-worker.service && \
            [[ "$(systemctl show emby-auto-worker.service --property=ActiveState --value)" == inactive ]]; then
            worker_paused=1
            return 0
          fi
          ;;
      esac
    fi
    sleep 2
  done
  fail '服务未通过启动检查。请运行 journalctl -u emby-auto-api -u emby-auto-worker 查看日志，再重试安装。'
}

show_access() {
  log ''
  log "Emby Auto 已安装。浏览器打开：http://127.0.0.1:$web_port"
  if (( worker_paused == 1 )); then
    log '后台当前处于暂停状态，现有安装保持原样；需要后台处理时在 Dashboard 恢复后台。'
  fi
  log '远程设备可先在自己的电脑建立 SSH 隧道：'
  log "  ssh -L $web_port:127.0.0.1:$web_port 用户@设备地址"
  log '首次使用时，在初始化向导中填写 PostgreSQL、管理员和外部服务信息。'
  log 'qBittorrent 的下载路径需要与初始化向导中的下载目录一致。'
  if [[ -n "$share_group" ]]; then
    log "下载目录使用共享组 $share_group；请将 qBittorrent 账户加入该组并重启其服务。"
  else
    log 'qBittorrent 使用独立账户时，请按部署文档配置共享组或目录 ACL，使其能写入下载目录。'
  fi
  log "部署、HTTPS、备份与升级：https://github.com/$repository/blob/main/docs/direct-deployment.md"
}

cleanup() {
  local status=$?
  if (( status != 0 )); then
    if (( services_started == 1 && installation_completed == 0 )); then
      systemctl stop emby-auto-worker.service emby-auto-api.service emby-auto-systemd-control.service || true
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
  validate_application_paths
  load_settings
  resolve_share_group
  check_account
  check_port
  temporary="$(mktemp -d)"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  if [[ -f "$config_dir/installed" ]]; then
    if (( prepare_only == 0 )); then
      # 已有安装请求共享组时实际应用成员与目录 ACL，并持久化到安装记录；
      # 失败即整体失败，不能只输出文案假装已生效。
      apply_share_group_request
      check_health any
      show_access
    else
      log '应用已经安装，现有服务状态保持原样。'
    fi
    installation_completed=1
    return
  fi
  if (( finalize_only == 1 )); then
    [[ -f "$config_dir/prepared" ]] || fail '尚未准备应用，无法完成安装登记。'
    verify_prepared_release
    if [[ -n "$share_group" ]]; then
      # --finalize 只补齐共享组成员、目录 ACL 与安装记录；不启动、停止或重启现有服务。
      apply_share_group_request
    fi
    check_health running
    touch "$config_dir/installed"
    installation_completed=1
    show_access
    return
  fi
  local prepared_now=0
  if [[ ! -f "$config_dir/prepared" ]]; then
    download_release
    install_dependencies
    prepare_account_and_storage
    install_application
    touch "$config_dir/prepared"
    prepared_now=1
  fi
  verify_prepared_release
  if (( prepared_now == 0 )) && [[ -n "$share_group" ]]; then
    # 首次安装因启动检查失败留下 prepared 后重跑：补齐依赖、成员、目录 ACL 与安装记录，
    # 再启动与核验；新安装已在 prepare 阶段应用，避免重复。
    command -v setfacl >/dev/null 2>&1 || install_dependencies
    apply_share_group_request
  fi
  if (( prepare_only == 1 )); then
    installation_completed=1
    log '应用与服务已准备。迁移旧配置后先启动控制服务和 API，再通过正式后台恢复命令启动 Worker。'
    return
  fi
  start_services
  check_health running
  touch "$config_dir/installed"
  installation_completed=1
  show_access
}

# 保持入口位于文件末尾，下载中断时不会执行未完整定义的安装流程。
if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
