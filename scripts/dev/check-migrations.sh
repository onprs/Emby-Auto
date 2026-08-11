#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

set -a
if [ -f "$repo_root/.env" ]; then
  # shellcheck disable=SC1091
  . "$repo_root/.env"
else
  # shellcheck disable=SC1091
  . "$repo_root/.env.example"
fi
set +a

exec "$script_dir/run-go.sh" run ./cmd/migration-check
