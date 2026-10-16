#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mode="${1:-static}"
target_arch="${TARGET_GOARCH:-amd64}"
release_dir="${DIRECT_RELEASE_DIR:-${root_dir}/runtime/release/emby-auto-direct}"

case "${mode}" in
  static|release) ;;
  *)
    echo "usage: $0 {static|release}" >&2
    exit 2
    ;;
esac
case "${target_arch}" in
  amd64|arm64) ;;
  *)
    echo "TARGET_GOARCH must be amd64 or arm64" >&2
    exit 2
    ;;
esac

required_files=(
  LICENSE
  README.md
  README.en.md
  docs/direct-deployment.md
  docs/direct-deployment.en.md
  deploy/direct/runtime.env.example
  deploy/direct/emby-auto-api.service
  deploy/direct/emby-auto-worker.service
  deploy/direct/emby-auto-systemd-control.service
  deploy/direct/worker-runtime.sh
  deploy/direct/host-control.env.example
  deploy/direct/nginx.conf
  scripts/deploy/build-direct-release.sh
  install.sh
)
for file in "${required_files[@]}"; do
  test -s "${root_dir}/${file}"
done
bash -n "${root_dir}/scripts/deploy/build-direct-release.sh"
bash -n "${root_dir}/install.sh"
bash -n "${root_dir}/deploy/direct/worker-runtime.sh"
grep -Fq 'KillMode=control-group' "${root_dir}/deploy/direct/emby-auto-worker.service"
grep -Fq 'User=root' "${root_dir}/deploy/direct/emby-auto-systemd-control.service"
grep -Fq -- '--allowed-uid ${EMBY_AUTO_SERVICE_UID}' "${root_dir}/deploy/direct/emby-auto-systemd-control.service"
grep -Fq 'EMBY_AUTO_HOST_CONTROL_SOCKET=/run/emby-auto-systemd/control.sock' "${root_dir}/deploy/direct/runtime.env.example"

grep -Fq 'license": "MIT"' "${root_dir}/package.json"
grep -Fq 'MIT License' "${root_dir}/LICENSE"
grep -Fq '[English](README.en.md)' "${root_dir}/README.md"
grep -Fq '[简体中文](README.md)' "${root_dir}/README.en.md"
grep -Fq 'EMBY_MEDIA_OWNER_UID=' "${root_dir}/deploy/direct/runtime.env.example"
grep -Fq 'API_ADDRESS=127.0.0.1:18080' "${root_dir}/deploy/direct/runtime.env.example"
grep -Fq 'WEB_ROOT=/opt/emby-auto/current/web' "${root_dir}/deploy/direct/runtime.env.example"

for unit in emby-auto-api emby-auto-worker; do
  unit_file="${root_dir}/deploy/direct/${unit}.service"
  grep -Fq 'User=emby' "${unit_file}"
  grep -Fq 'Group=emby' "${unit_file}"
  grep -Fq 'NoNewPrivileges=true' "${unit_file}"
  grep -Fq 'CapabilityBoundingSet=' "${unit_file}"
  if grep -Eq 'User=root|docker\.sock|/run/systemd|/var/run/docker' "${unit_file}"; then
    echo "direct service exposes a privileged runtime surface: ${unit_file}" >&2
    exit 1
  fi
done
grep -Fq 'ExecStart=/opt/emby-auto/current/bin/emby-auto-api' "${root_dir}/deploy/direct/emby-auto-api.service"
grep -Fq 'ExecStart=/opt/emby-auto/current/bin/emby-auto-worker' "${root_dir}/deploy/direct/emby-auto-worker.service"

grep -Fq 'proxy_pass http://127.0.0.1:18080;' "${root_dir}/deploy/direct/nginx.conf"
grep -Fq 'listen 127.0.0.1:18079;' "${root_dir}/deploy/direct/nginx.conf"
grep -Fq 'proxy_buffering off;' "${root_dir}/deploy/direct/nginx.conf"
grep -Fq 'proxy_set_header Range $http_range;' "${root_dir}/deploy/direct/nginx.conf"

if [[ "${mode}" == "static" ]]; then
  echo "direct Linux deployment static checks passed"
  exit 0
fi

release_files=(
  bin/emby-auto-api
  bin/emby-auto-worker
  bin/emby-auto-migration-check
  bin/emby-auto-host-control
  bin/emby-auto-background-runtime
  bin/emby-auto-release-preflight
  bin/emby-auto-recover-stale-operations
  bin/emby-auto-migrate-runtime-addresses
  bin/emby-auto-migrate-staging-ownership
  web/index.html
  config/runtime.env.example
  config/emby-auto-api.service
  config/emby-auto-worker.service
  config/emby-auto-systemd-control.service
  config/worker-runtime.sh
  config/host-control.env.example
  config/nginx.conf
  DEPLOYMENT.md
  DEPLOYMENT.en.md
  install.sh
  LICENSE
  SHA256SUMS
)
for file in "${release_files[@]}"; do
  test -s "${release_dir}/${file}"
done
cmp -s "${root_dir}/deploy/direct/runtime.env.example" "${release_dir}/config/runtime.env.example"
cmp -s "${root_dir}/deploy/direct/emby-auto-api.service" "${release_dir}/config/emby-auto-api.service"
cmp -s "${root_dir}/deploy/direct/emby-auto-worker.service" "${release_dir}/config/emby-auto-worker.service"
for file in emby-auto-systemd-control.service worker-runtime.sh host-control.env.example; do
  cmp -s "${root_dir}/deploy/direct/${file}" "${release_dir}/config/${file}"
done
cmp -s "${root_dir}/deploy/direct/nginx.conf" "${release_dir}/config/nginx.conf"
cmp -s "${root_dir}/docs/direct-deployment.md" "${release_dir}/DEPLOYMENT.md"
cmp -s "${root_dir}/docs/direct-deployment.en.md" "${release_dir}/DEPLOYMENT.en.md"
cmp -s "${root_dir}/LICENSE" "${release_dir}/LICENSE"
cmp -s "${root_dir}/install.sh" "${release_dir}/install.sh"
diff -qr "${root_dir}/apps/web/dist" "${release_dir}/web" >/dev/null
for binary in emby-auto-api emby-auto-worker emby-auto-migration-check emby-auto-host-control emby-auto-background-runtime emby-auto-release-preflight emby-auto-recover-stale-operations emby-auto-migrate-runtime-addresses emby-auto-migrate-staging-ownership; do
  test -s "${release_dir}/bin/${binary}"
  go version -m "${release_dir}/bin/${binary}" >/dev/null
  case "${target_arch}" in
    amd64) file "${release_dir}/bin/${binary}" | grep -Eq 'ELF 64-bit.*x86-64' ;;
    arm64) file "${release_dir}/bin/${binary}" | grep -Eq 'ELF 64-bit.*(ARM aarch64|aarch64)' ;;
  esac
done
(
  cd "${release_dir}"
  sha256sum --quiet -c SHA256SUMS
)
test -s "${release_dir}-linux-${target_arch}.tar.gz"
release_name="$(basename "${release_dir}")"
release_parent="$(dirname "${release_dir}")"
for binary in emby-auto-api emby-auto-worker emby-auto-migration-check emby-auto-host-control emby-auto-background-runtime emby-auto-release-preflight emby-auto-recover-stale-operations emby-auto-migrate-runtime-addresses emby-auto-migrate-staging-ownership; do
  (
    cd "${release_parent}"
    tar -tzvf "${release_name}-linux-${target_arch}.tar.gz" "${release_name}/bin/${binary}"
  ) | tail -n 1 | grep -Eq '^-rwxr-xr-x[[:space:]]'
done
echo "direct Linux deployment release checks passed"
