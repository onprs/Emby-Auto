#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)
target=${1:-}
action=${2:-}

set -a
if [ -f "$repo_root/.env" ]; then
  # shellcheck disable=SC1091
  . "$repo_root/.env"
else
  # shellcheck disable=SC1091
  . "$repo_root/.env.example"
fi
set +a

case "$target:$action" in
  app:up)
    "$script_dir/go-tool.sh" migrate -path db/migrations -database "$DATABASE_URL" up
    ;;
  app:down)
    "$script_dir/go-tool.sh" migrate -path db/migrations -database "$DATABASE_URL" down 1
    ;;
  river:up)
    "$script_dir/go-tool.sh" river migrate-up --line main --database-url "$DATABASE_URL"
    ;;
  river:down)
    "$script_dir/go-tool.sh" river migrate-down --line main --database-url "$DATABASE_URL" --max-steps 1
    ;;
  river:status)
    "$script_dir/go-tool.sh" river migrate-list --line main --database-url "$DATABASE_URL"
    ;;
  *)
    echo "usage: $0 {app|river} {up|down|status}" >&2
    exit 2
    ;;
esac
