#!/usr/bin/env bash
set -euo pipefail

# 仅供 GitHub 托管的临时 CI 设备执行，会实际安装软件包和应用服务。
[[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted && "$EUID" == 0 ]] || {
  printf '%s\n' '此检查只能在 GitHub 托管的临时 CI 设备中以 root 执行。' >&2
  exit 1
}
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
release_dir="$(realpath "${1:?请提供发布包目录}")"
assets="$(mktemp -d)"
trap 'rm -rf "$assets"' EXIT
archive=emby-auto-direct-amd64-linux-amd64.tar.gz
cp "$release_dir/$archive" "$assets/$archive"
(cd "$assets" && sha256sum "$archive" > SHA256SUMS)
export EMBY_AUTO_INSTALL_TEST_SOURCE="$root_dir/install.sh"
export EMBY_AUTO_INSTALL_TEST_ASSETS="$assets"

external_services_snapshot() {
  dpkg-query -W -f='${Package}=${Version}\n' 'postgresql*' 'nginx*' 2>/dev/null || true
  local directory
  for directory in /etc/postgresql /etc/nginx; do
    if [[ -d "$directory" ]]; then
      find "$directory" -type f -exec sha256sum {} + | sort
    fi
  done
}
external_services_snapshot >"$assets/external.before"

run_installer() {
  # 使用当前 CI 构建的发布包，其它安装操作均使用真实实现。
  bash -c '
    source "$EMBY_AUTO_INSTALL_TEST_SOURCE"
    download_release() {
      local archive="emby-auto-direct-$target_arch-linux-$target_arch.tar.gz"
      cp "$EMBY_AUTO_INSTALL_TEST_ASSETS/$archive" "$temporary/$archive"
      cp "$EMBY_AUTO_INSTALL_TEST_ASSETS/SHA256SUMS" "$temporary/SHA256SUMS"
      verify_archive "$archive"
    }
    main --version v0.0.0
  '
}

run_installer
for service in emby-auto-api emby-auto-worker emby-auto-systemd-control; do
  systemctl is-active --quiet "$service"
done
curl --fail --silent --noproxy '*' http://127.0.0.1:18080/ >"$assets/index.html"
curl --fail --silent --noproxy '*' http://127.0.0.1:18080/tasks | cmp -s - "$assets/index.html"
curl --fail --silent --noproxy '*' http://127.0.0.1:18080/api/v1/setup/status | python3 -c '
import json, sys
status = json.load(sys.stdin)
assert status["state"] == "required", status
assert status["databaseConfigured"] is False, status
assert status["databaseManagedExternally"] is False, status
assert status["administratorConfigured"] is False, status
'
[[ "$(systemctl show -p User --value emby-auto-api)" == emby-auto ]]
[[ "$(systemctl show -p User --value emby-auto-worker)" == emby-auto ]]
[[ "$(stat -c %a /etc/emby-auto/installer.env)" == 600 ]]
[[ "$(stat -c %a /etc/emby-auto/runtime.env)" == 640 ]]
[[ ! -e /var/lib/emby-auto/bootstrap.json ]]
[[ ! -e /var/lib/emby-auto/bootstrap.json.complete ]]
[[ "$(stat -c %a /run/emby-auto-systemd/control.sock)" == 660 ]]
[[ "$(stat -c %u /run/emby-auto-systemd/control.sock)" == 0 ]]
[[ "$(stat -c %g /run/emby-auto-systemd/control.sock)" == "$(id -g emby-auto)" ]]
control_command() {
  runuser -u emby-auto -- env EMBY_AUTO_HOST_CONTROL_SOCKET=/run/emby-auto-systemd/control.sock \
    /opt/emby-auto/current/bin/emby-auto-host-control "$@"
}
[[ "$(control_command media-owner)" == "$(id -u emby-auto)" ]]
# 此时尚未填写数据库，Worker 只在等待初始化，可直接验证隔离 CI 设备的控制通道。
[[ "$(control_command worker-stop)" == stopped ]]
! systemctl is-active --quiet emby-auto-worker
[[ "$(control_command worker-start)" == running ]]
systemctl is-active --quiet emby-auto-worker
external_services_snapshot >"$assets/external.after"
cmp -s "$assets/external.before" "$assets/external.after"
before="$(sha256sum /etc/emby-auto/installer.env /etc/emby-auto/runtime.env)"
run_installer
[[ "$before" == "$(sha256sum /etc/emby-auto/installer.env /etc/emby-auto/runtime.env)" ]]
printf '%s\n' '真实 systemd 安装、应用身份与后台控制、数据库填写向导、网页路由、外部服务保留与重复安装检查通过。'
