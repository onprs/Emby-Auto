#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mode="${1:-static}"
compose_file="${root_dir}/deploy/compose.yaml"
env_file="${root_dir}/deploy/.env.example"

case "${mode}" in
  static|release) ;;
  *)
    echo "usage: $0 {static|release}" >&2
    exit 2
    ;;
esac

for file in \
  "${compose_file}" \
  "${env_file}" \
  "${root_dir}/deploy/docker/backend.Dockerfile" \
  "${root_dir}/deploy/docker/web.Dockerfile" \
  "${root_dir}/deploy/docker/nginx.conf" \
  "${root_dir}/deploy/nginx/locations.conf" \
  "${root_dir}/docs/deployment.md" \
  "${root_dir}/docs/deployment.en.md"; do
  if [[ ! -s "${file}" ]]; then
    echo "required deployment file is missing or empty: ${file}" >&2
    exit 1
  fi
done

for service in postgres api worker web; do
  grep -Eq "^  ${service}:$" "${compose_file}"
done

grep -Fq 'command: ["emby-auto-api"]' "${compose_file}"
grep -Fq 'command: ["emby-auto-worker"]' "${compose_file}"
grep -Fq 'EMBY_MEDIA_OWNER_UID: ${APP_UID:-10001}' "${compose_file}"
grep -Fq 'include /etc/nginx/emby-auto-locations.conf;' "${root_dir}/deploy/docker/nginx.conf"
grep -Fq 'COPY deploy/nginx/locations.conf /etc/nginx/emby-auto-locations.conf' "${root_dir}/deploy/docker/web.Dockerfile"
grep -Fq 'COPY LICENSE /usr/share/licenses/emby-auto/LICENSE' "${root_dir}/deploy/docker/backend.Dockerfile"
grep -Fq 'COPY LICENSE /usr/share/licenses/emby-auto/LICENSE' "${root_dir}/deploy/docker/web.Dockerfile"
grep -Fq 'org.opencontainers.image.licenses="MIT"' "${root_dir}/deploy/docker/backend.Dockerfile"
grep -Fq 'org.opencontainers.image.licenses="MIT"' "${root_dir}/deploy/docker/web.Dockerfile"
grep -Fq 'location = /api/v1/events' "${root_dir}/deploy/nginx/locations.conf"
grep -Fq 'proxy_set_header Range $http_range;' "${root_dir}/deploy/nginx/locations.conf"
grep -Fq 'try_files $uri $uri/ /index.html;' "${root_dir}/deploy/nginx/locations.conf"

if grep -Eq '/var/run/docker\.sock|/run/systemd|network_mode:[[:space:]]*host' "${compose_file}"; then
  echo "deployment must not expose privileged host-control surfaces" >&2
  exit 1
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose --env-file "${env_file}" -f "${compose_file}" config --quiet
elif [[ "${mode}" == "release" ]]; then
  echo "Docker Compose is required for release verification" >&2
  exit 1
else
  echo "Docker Compose is unavailable; static deployment checks passed"
  exit 0
fi

if [[ "${mode}" == "release" ]]; then
  docker image inspect emby-auto-backend:local emby-auto-web:local >/dev/null
  echo "deployment configuration and release images passed"
else
  echo "deployment configuration checks passed"
fi
