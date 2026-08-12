#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
action=${1:-status}

set -a
if [ -f "$repo_root/.env" ]; then
  # shellcheck disable=SC1091
  . "$repo_root/.env"
else
  # shellcheck disable=SC1091
  . "$repo_root/.env.example"
fi
set +a

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  case "$action" in
    start) docker compose -f "$repo_root/compose.yaml" up -d --wait postgres ;;
    stop) docker compose -f "$repo_root/compose.yaml" stop postgres ;;
    status) docker compose -f "$repo_root/compose.yaml" ps postgres ;;
    shell) docker compose -f "$repo_root/compose.yaml" exec postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" ;;
    *) echo "usage: $0 {start|stop|status|shell}" >&2; exit 2 ;;
  esac
  exit 0
fi

if command -v wsl.exe >/dev/null 2>&1; then
  export WSLENV="${WSLENV:+$WSLENV:}POSTGRES_USER:POSTGRES_PASSWORD:POSTGRES_DB:POSTGRES_PORT"
  export MSYS_NO_PATHCONV=1
  export MSYS2_ARG_CONV_EXCL='*'
  exec wsl.exe -d "${EMBY_AUTO_WSL_DISTRO:-Ubuntu-22.04}" -- bash -s -- "$action" < "$script_dir/postgres-wsl.sh"
fi

echo "PostgreSQL requires Docker Compose or WSL2 on this machine." >&2
exit 127
