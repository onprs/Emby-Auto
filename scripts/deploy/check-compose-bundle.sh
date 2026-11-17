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

# 包根文档已在组装时把语言互链改为 DEPLOYMENT*.md，所有相对链接必须仍能解析；
# 仓库 docs 的源文档链接同样必须有效，两个方向共同防漂移。
check_relative_links() {
  node -e '
const fs = require("node:fs");
const path = require("node:path");
let failed = 0;
for (const file of process.argv.slice(1)) {
  const text = fs.readFileSync(file, "utf8");
  const linkPattern = /\]\(([^)]*)\)/g;
  let match;
  while ((match = linkPattern.exec(text)) !== null) {
    let target = match[1].trim();
    if (target.startsWith("<")) {
      const end = target.indexOf(">");
      target = end === -1 ? target.slice(1) : target.slice(1, end);
    } else {
      target = target.split(/\s+/)[0];
    }
    if (target === "" || target.startsWith("#") || target.startsWith("/")) continue;
    if (/^[a-z][a-z0-9+.-]*:/i.test(target)) continue;
    const rawPath = target.split("#")[0];
    if (rawPath === "") continue;
    let decoded;
    try {
      decoded = decodeURIComponent(rawPath);
    } catch {
      console.error(`链接编码无效：${target}（${file}）`);
      failed += 1;
      continue;
    }
    if (!fs.existsSync(path.resolve(path.dirname(file), decoded))) {
      console.error(`链接目标不存在：${target}（${file}）`);
      failed += 1;
    }
  }
}
if (failed > 0) {
  console.error(`相对链接检查失败：${failed} 个目标缺失`);
  process.exit(1);
}
' "$@"
}
check_relative_links "${bundle}/DEPLOYMENT.md" "${bundle}/DEPLOYMENT.en.md"
check_relative_links "${root_dir}/docs/deployment.md" "${root_dir}/docs/deployment.en.md"

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  if [[ "${mode}" == build ]]; then
    printf '%s\n' 'build 模式需要 Docker Compose。' >&2
    exit 1
  fi
  printf '%s\n' 'Docker Compose 不可用；发布包文件检查通过。'
  exit 0
fi

compose=(docker compose --env-file "${bundle}/deploy/.env.example" -f "${bundle}/deploy/compose.yaml")

# 项目名与镜像引用都取自 Compose 的实际解析结果：默认标签 local、shell 覆盖或
# env-file 覆盖都按同一次解析生效，避免断言名与构建产物脱钩。
parsed="$("${compose[@]}" config --format json | node -e '
let input = "";
process.stdin.on("data", (chunk) => (input += chunk)).on("end", () => {
  const config = JSON.parse(input);
  const image = (service) => (config.services?.[service]?.image ?? "");
  process.stdout.write([config.name, image("api"), image("worker"), image("web")].join("\t") + "\n");
});
')"
IFS=$'\t' read -r project_name api_image worker_image web_image <<< "${parsed}"

# 项目名与卷名固定，与部署文档描述一致，防止文档与配置漂移。
[[ "${project_name}" == emby-auto ]] || {
  printf 'Compose 项目名意外变化：%s（文档固定为 emby-auto）\n' "${project_name}" >&2
  exit 1
}
for image in "${api_image}" "${worker_image}" "${web_image}"; do
  [[ -n "${image}" ]] || {
    printf '%s\n' 'Compose 未解析出 api/worker/web 的镜像引用。' >&2
    exit 1
  }
done
[[ "${worker_image}" == "${api_image}" ]] || {
  printf 'worker 与 api 镜像引用不一致：%s / %s\n' "${worker_image}" "${api_image}" >&2
  exit 1
}
[[ "${api_image}" == emby-auto-backend:* && "${web_image}" == emby-auto-web:* ]] || {
  printf '镜像仓库意外变化：api=%s web=%s\n' "${api_image}" "${web_image}" >&2
  exit 1
}

if [[ "${mode}" == build ]]; then
  "${compose[@]}" build
  docker image inspect "${api_image}" "${web_image}" >/dev/null
  printf '发布包在无仓库源码的临时目录中完成镜像构建：%s %s\n' "${api_image}" "${web_image}"
else
  printf '%s\n' '发布包配置与构建路径检查通过。'
fi
