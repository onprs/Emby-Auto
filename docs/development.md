# 开发与发布维护

本文面向源码贡献者和发布维护者。公开用户的安装与使用请阅读 [README](../README.md) 和 [Linux 直接部署](direct-deployment.md)。

## 开发环境

需要 Go 1.26+、Node.js 24+、npm 11+，以及 PostgreSQL 17 或 Docker Compose。

在仓库根目录安装依赖与固定版本开发工具：

~~~bash
npm ci
npm run tools:download
npm run generate
npm run db:start
npm run db:migrate
npm run db:river:migrate
~~~

分别启动 API、Worker 和 Web：

~~~bash
npm run start:api
npm run start:worker
npm run dev --workspace @emby-auto/web
~~~

开发网页默认监听 `http://127.0.0.1:5173`，并将 `/api` 代理到 `http://127.0.0.1:8080`。本地凭据写入未跟踪的 `.env` 或安装生成的 `bootstrap.json`。

## 组件与接口

API 负责安装、认证、查询和短事务；Worker 独立执行下载、映射、转码、字幕处理和入库任务。PostgreSQL 保存状态与配置，River 管理持久化任务队列。原生部署可由 API 同时提供静态网页与接口；容器部署通过 Nginx 提供网页、API 代理、SSE 和媒体预览。

[OpenAPI 契约](../contracts/openapi.yaml) 是 HTTP 接口的唯一契约。修改接口或数据库查询后执行：

~~~bash
npm run generate
npm run check:generated
~~~

生成目录 `backend/internal/transport/httpapi/api.gen.go`、`backend/db/sqlc` 和 `apps/web/src/api/generated` 由生成器维护。

## 检查

| 命令 | 范围 |
| --- | --- |
| `npm run check` | 静态分析、单元测试、构建和生成文件漂移 |
| `npm run check:integration` | 数据库迁移与 PostgreSQL 集成测试 |
| `npm run test:e2e --workspace @emby-auto/web` | 浏览器测试 |
| `npm run deploy:check` | Compose 配置 |
| `npm run deploy:direct:check` | Linux 直接部署模板 |
| `npm run deploy:install:check` | 安装器语法、安装与重试、校验失败和配置保护 |

安装器单元测试在临时目录中模拟系统服务与软件包操作，不会修改本机的服务、数据库或媒体目录。CI 还会在 GitHub 托管的 Ubuntu 22.04/24.04 临时设备上，用本次构建的发布包验证真实的软件包安装、systemd 服务、数据库连接和重复安装。`scripts/deploy/check-install-systemd.sh` 仅用于这些临时 CI 设备。

## 构建 Linux 发布包

构建维护环境还需要 GNU coreutils、`file`、`tar` 和 `gzip`。在仓库根目录执行：

~~~bash
npm ci
SKIP_NPM_CI=1 TARGET_GOARCH=amd64 bash scripts/deploy/build-direct-release.sh runtime/release/emby-auto-direct-amd64
TARGET_GOARCH=amd64 DIRECT_RELEASE_DIR=runtime/release/emby-auto-direct-amd64 npm run deploy:direct:check-release
SKIP_NPM_CI=1 TARGET_GOARCH=arm64 bash scripts/deploy/build-direct-release.sh runtime/release/emby-auto-direct-arm64
TARGET_GOARCH=arm64 DIRECT_RELEASE_DIR=runtime/release/emby-auto-direct-arm64 npm run deploy:direct:check-release
~~~

发布包包含 API、Worker、迁移检查工具、网页、安装器、服务模板、部署文档和包内校验和。

`v*` 标签触发 Release 工作流。公开 Release 附带 `install.sh`、两种架构的发布包及 `SHA256SUMS`；安装器依赖以下公开文件名：

~~~text
install.sh
emby-auto-direct-amd64-linux-amd64.tar.gz
emby-auto-direct-arm64-linux-arm64.tar.gz
SHA256SUMS
~~~

变更上述名称时需要同步安装器与发布流程。首次发布安装器后，README 中的一键安装地址才会在 GitHub Release 上可用。
