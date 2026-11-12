#!/usr/bin/env bash
set -euo pipefail

# 组装 Compose 发布包。包内保留仓库相对路径，使 deploy/compose.yaml 的构建上下文、
# Dockerfile 路径与 COPY 指令在解压后仍然成立，用户无需克隆仓库即可构建。
# 用法：assemble-compose-bundle.sh <目标目录>

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
destination="${1:?用法: assemble-compose-bundle.sh <目标目录>}"

mkdir -p "$destination"
destination="$(cd "$destination" && pwd)"
[[ "$destination" != "$root_dir" ]] || {
  printf '%s\n' '发布包目标目录不能是仓库根目录。' >&2
  exit 2
}

# 只复制构建所需内容，排除依赖、构建产物和本地运行数据。
copy_paths() {
  tar -C "$root_dir" \
    --exclude='node_modules' \
    --exclude='dist' \
    --exclude='coverage' \
    --exclude='test-results' \
    --exclude='playwright-report' \
    -cf - "$@" | tar -C "$destination" -xf -
}

copy_paths deploy/docker deploy/nginx backend apps/web scripts/web
for file in \
  deploy/compose.yaml \
  deploy/.env.example \
  .dockerignore \
  go.work \
  go.work.sum \
  package.json \
  package-lock.json \
  LICENSE; do
  cp -a "$root_dir/$file" "$destination/$file"
done

# 发布包把部署文档放在包根，命令中的 deploy/ 路径与仓库布局一致。
cp -a "$root_dir/docs/deployment.md" "$destination/DEPLOYMENT.md"
cp -a "$root_dir/docs/deployment.en.md" "$destination/DEPLOYMENT.en.md"

printf '%s\n' "Compose 发布包已组装：$destination"
