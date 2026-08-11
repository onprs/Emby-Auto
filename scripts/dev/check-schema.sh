#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
schema_test="$repo_root/backend/db/test/core_schema.sql"

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
  docker compose -f "$repo_root/compose.yaml" exec -T postgres \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" < "$schema_test"
  exit 0
fi

if command -v wsl.exe >/dev/null 2>&1; then
  export MSYS_NO_PATHCONV=1
  export MSYS2_ARG_CONV_EXCL='*'
  wsl.exe -d "${EMBY_AUTO_WSL_DISTRO:-Ubuntu-22.04}" -- \
    env PGPASSWORD="$POSTGRES_PASSWORD" \
    psql -h 127.0.0.1 -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
    < "$schema_test"
  exit 0
fi

echo "Schema checks require Docker Compose or WSL2 on this machine." >&2
exit 127
