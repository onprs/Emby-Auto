#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

api_file="$repo_root/backend/internal/transport/httpapi/api.gen.go"
sdk_dir="$repo_root/apps/web/src/api/generated"
sqlc_dir="$repo_root/backend/db/sqlc"

if [ ! -f "$api_file" ] || [ ! -d "$sdk_dir" ] || [ ! -d "$sqlc_dir" ]; then
  echo "Generated files are missing. Run npm run generate first." >&2
  exit 1
fi

mkdir -p "$tmp_dir/sdk" "$tmp_dir/sqlc"
cp "$api_file" "$tmp_dir/api.gen.go"
cp -R "$sdk_dir/." "$tmp_dir/sdk/"
cp -R "$sqlc_dir/." "$tmp_dir/sqlc/"

cd "$repo_root"
npm run generate

diff -u "$tmp_dir/api.gen.go" "$api_file"
diff -ru "$tmp_dir/sdk" "$sdk_dir"
diff -ru "$tmp_dir/sqlc" "$sqlc_dir"
