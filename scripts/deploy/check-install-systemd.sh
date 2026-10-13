#!/usr/bin/env bash
set -euo pipefail

# 仅供 GitHub 托管的临时 CI 设备执行，会实际安装软件包和系统服务。
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

run_installer() {
  # 使用当前 CI 构建的发布包，系统识别、依赖安装与服务管理均使用真实实现。
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
for service in emby-auto-api emby-auto-worker postgresql@17-emby-auto nginx; do
  systemctl is-active --quiet "$service"
done
curl --fail --silent --noproxy '*' http://127.0.0.1:18080/ >/dev/null
curl --fail --silent --noproxy '*' http://127.0.0.1:18080/api/v1/setup/status | python3 -c '
import json, sys
status = json.load(sys.stdin)
assert status["state"] == "required", status
assert status["databaseConfigured"] is True, status
assert status["databaseManagedExternally"] is True, status
'
[[ "$(systemctl show -p User --value emby-auto-api)" == emby-auto ]]
[[ "$(systemctl show -p User --value emby-auto-worker)" == emby-auto ]]
[[ "$(stat -c %a /etc/emby-auto/installer.env)" == 600 ]]
[[ "$(stat -c %a /etc/emby-auto/runtime.env)" == 640 ]]
[[ "$(runuser -u postgres -- psql -p 18082 -d emby_auto -Atc 'SELECT 1')" == 1 ]]
[[ "$(runuser -u postgres -- psql -p 18082 -d emby_auto -Atc 'SHOW server_encoding')" == UTF8 ]]
before="$(sha256sum /etc/emby-auto/installer.env /etc/emby-auto/runtime.env)"
run_installer
[[ "$before" == "$(sha256sum /etc/emby-auto/installer.env /etc/emby-auto/runtime.env)" ]]
printf '%s\n' '真实 systemd 安装、数据库连接、网页向导与重复安装检查通过。'
