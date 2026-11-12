#!/usr/bin/env bash
set -euo pipefail

# 校验 Compose 发布包能在没有仓库源码的临时目录中按公开步骤使用。
# config 模式只做静态与 Compose 解析检查；build 模式还会实际构建镜像。
# 用法：check-compose-bundle.sh [config|build]

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mode="${1:-config}"
case "${mode}" in
  config|build) ;;
  *)
    printf '用法：%s [config|build]\n' "$0" >&2
    exit 2
    ;;
esac

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

# 按发布流程组装，再打包解压到另一个目录，模拟用户下载后的实际使用位置。
bash "${root_dir}/scripts/deploy/assemble-compose-bundle.sh" "${work}/bundle" >/dev/null
tar -C "${work}/bundle" -czf "${work}/emby-auto-compose-test.tar.gz" .
mkdir -p "${work}/extracted"
tar -C "${work}/extracted" -xzf "${work}/emby-auto-compose-test.tar.gz"
bundle="${work}/extracted"

# 发布包不得夹带仓库元数据、依赖、构建产物或本地运行数据。
for forbidden in .git node_modules runtime apps/web/node_modules apps/web/dist apps/web/test-results; do
  if [[ -e "${bundle}/${forbidden}" ]]; then
    printf '发布包包含不应分发的路径：%s\n' "${forbidden}" >&2
    exit 1
  fi
done

# Dockerfile 与 Compose 在解压目录中读取的输入必须齐全。
required=(
  deploy/compose.yaml
  deploy/.env.example
  deploy/docker/backend.Dockerfile
  deploy/docker/web.Dockerfile
  deploy/docker/nginx.conf
  deploy/nginx/locations.conf
  DEPLOYMENT.md
  DEPLOYMENT.en.md
  LICENSE
  .dockerignore
  package.json
  package-lock.json
  go.work
  go.work.sum
  scripts/web/check-bundle-budget.mts
  backend/go.mod
  backend/go.sum
  backend/cmd/api
  backend/internal
  apps/web/package.json
  apps/web/index.html
  apps/web/src
  apps/web/tsconfig.json
  apps/web/vite.config.ts
)
for file in "${required[@]}"; do
  if [[ ! -s "${bundle}/${file}" && ! -d "${bundle}/${file}" ]]; then
    printf '发布包缺少构建所需路径：%s\n' "${file}" >&2
    exit 1
  fi
done

# 构建上下文与 Dockerfile 路径必须能在包根解析，不能依赖仓库父目录。
grep -Fq 'context: ..' "${bundle}/deploy/compose.yaml"
grep -Fq 'dockerfile: deploy/docker/backend.Dockerfile' "${bundle}/deploy/compose.yaml"
grep -Fq 'dockerfile: deploy/docker/web.Dockerfile' "${bundle}/deploy/compose.yaml"
grep -Fq 'COPY scripts/web/ scripts/web/' "${bundle}/deploy/docker/web.Dockerfile"
grep -Fq 'COPY deploy/nginx/locations.conf /etc/nginx/emby-auto-locations.conf' "${bundle}/deploy/docker/web.Dockerfile"

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  if [[ "${mode}" == build ]]; then
    printf '%s\n' 'build 模式需要 Docker Compose。' >&2
    exit 1
  fi
  printf '%s\n' 'Docker Compose 不可用；发布包文件检查通过。'
  exit 0
fi

compose=(docker compose --env-file "${bundle}/deploy/.env.example" -f "${bundle}/deploy/compose.yaml")
"${compose[@]}" config --quiet

if [[ "${mode}" == build ]]; then
  "${compose[@]}" build
  docker image inspect emby-auto-backend:local emby-auto-web:local >/dev/null
  printf '%s\n' '发布包在无仓库源码的临时目录中完成镜像构建。'
else
  printf '%s\n' '发布包配置与构建路径检查通过。'
fi
