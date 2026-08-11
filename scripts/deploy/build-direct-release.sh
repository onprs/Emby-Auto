#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_dir="${1:-${root_dir}/runtime/release/emby-auto-direct}"
target_arch="${TARGET_GOARCH:-amd64}"

case "${target_arch}" in
  amd64|arm64) ;;
  *)
    echo "TARGET_GOARCH must be amd64 or arm64" >&2
    exit 2
    ;;
esac

release_root="$(realpath -m "${root_dir}/runtime/release")"
output_dir="$(realpath -m "${output_dir}")"
case "${output_dir}" in
  "${release_root}/"*) ;;
  *)
    echo "output directory must be below ${release_root}: ${output_dir}" >&2
    exit 2
    ;;
esac

if [[ "${SKIP_NPM_CI:-0}" != "1" ]]; then
  (cd "${root_dir}" && npm ci)
fi
(cd "${root_dir}" && npm run build --workspace @emby-auto/web)

temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT
release="${temporary}/emby-auto-direct"
mkdir -p "${release}/bin" "${release}/web" "${release}/config"

binary_names=(
  emby-auto-api
  emby-auto-worker
  emby-auto-migration-check
)
binary_packages=(
  ./cmd/api
  ./cmd/worker
  ./cmd/migration-check
)
for index in "${!binary_names[@]}"; do
  CGO_ENABLED=0 GOOS=linux GOARCH="${target_arch}" go -C "${root_dir}/backend" build \
    -trimpath -ldflags="-s -w" \
    -o "${release}/bin/${binary_names[index]}" \
    "${binary_packages[index]}"
done

cp -R "${root_dir}/apps/web/dist/." "${release}/web/"
cp "${root_dir}/deploy/direct/runtime.env.example" "${release}/config/runtime.env.example"
cp "${root_dir}/deploy/direct/emby-auto-api.service" "${release}/config/emby-auto-api.service"
cp "${root_dir}/deploy/direct/emby-auto-worker.service" "${release}/config/emby-auto-worker.service"
cp "${root_dir}/deploy/direct/nginx.conf" "${release}/config/nginx.conf"
cp "${root_dir}/deploy/nginx/locations.conf" "${release}/config/emby-auto-locations.conf"
cp "${root_dir}/docs/direct-deployment.md" "${release}/DEPLOYMENT.md"
cp "${root_dir}/docs/direct-deployment.en.md" "${release}/DEPLOYMENT.en.md"
cp "${root_dir}/LICENSE" "${release}/LICENSE"
chmod 0755 "${release}/bin/"*

(
  cd "${release}"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
)

rm -rf "${output_dir}"
mkdir -p "$(dirname "${output_dir}")"
mv "${release}" "${output_dir}"
(
  cd "$(dirname "${output_dir}")"
  release_name="$(basename "${output_dir}")"
  archive="${release_name}-linux-${target_arch}.tar"
  rm -f "${archive}" "${archive}.gz"

  binary_paths=()
  binary_excludes=()
  for binary in "${binary_names[@]}"; do
    binary_paths+=("${release_name}/bin/${binary}")
    binary_excludes+=(--exclude="${release_name}/bin/${binary}")
  done
  tar --mode=0755 -cf "${archive}" "${binary_paths[@]}"
  tar --mode='u=rwX,go=rX' "${binary_excludes[@]}" -rf "${archive}" "${release_name}"
  gzip "${archive}"
)

printf 'release=%s\narchive=%s-linux-%s.tar.gz\narch=%s\n' \
  "${output_dir}" "${output_dir}" "${target_arch}" "${target_arch}"
