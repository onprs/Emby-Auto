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
  echo "缺少生成文件，请先运行 npm run generate。" >&2
  exit 1
fi

mkdir -p "$tmp_dir/backend/db"
cp "$repo_root/backend/sqlc.yaml" "$tmp_dir/backend/sqlc.yaml"
cp -R "$repo_root/backend/db/query" "$repo_root/backend/db/migrations" "$tmp_dir/backend/db/"

# 保留生成选项，仅将临时配置的输出位置改到隔离目录。
output_configured=false
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    output:*)
      printf 'output: %s\n' "$tmp_dir/api.gen.go"
      output_configured=true
      ;;
    *) printf '%s\n' "$line" ;;
  esac
done < "$repo_root/contracts/oapi-codegen.yaml" > "$tmp_dir/oapi-codegen.yaml"
if [ "$output_configured" != true ]; then
  echo "无法定位 OpenAPI 输出配置，请更新生成检查脚本。" >&2
  exit 1
fi

cd "$repo_root"
bash scripts/dev/go-tool.sh oapi-codegen --config "$tmp_dir/oapi-codegen.yaml" "$repo_root/contracts/openapi.yaml"
bash scripts/dev/go-tool.sh sqlc generate --file "$tmp_dir/backend/sqlc.yaml"
npm run generate:sdk -- --output "$tmp_dir/sdk"

diff -u "$api_file" "$tmp_dir/api.gen.go"
diff -ru "$sdk_dir" "$tmp_dir/sdk"
diff -ru "$sqlc_dir" "$tmp_dir/backend/db/sqlc"
