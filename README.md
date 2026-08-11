<p align="center">
  <img src="emby-auto-svg.svg" alt="Emby Auto - 自托管媒体自动化平台" width="800">
</p>

# Emby Auto

**简体中文** | [English](README.en.md)

Emby Auto 是面向 Emby 的自托管媒体自动化平台。系统以统一工作流连接资源检索、RSS 订阅、qBittorrent 下载、TMDb 剧集映射、FFmpeg 媒体处理、人工审核和 Emby 入库，并通过 Web 管理界面提供任务追踪与运行状态。

## 功能概览

- 检索动漫剧集和电影资源，并将候选资源纳入统一任务流程。
- 持续轮询 RSS/Atom 订阅，按关键词、季集坐标和媒体库占用状态执行自动筛选。
- 管理 qBittorrent 下载入队、文件选择、进度同步、取消、重试与缓存清理。
- 通过单集锚点建立源剧集与 TMDb 常规剧集之间的映射关系。
- 为每个源视频生成符合转码配置的视频文件和独立 ASS 字幕。
- 在入库前提供视频与字幕审核，并使用校验和与原子重命名保护目标文件。
- 同步 Emby 媒体库目录，集中展示任务、后台操作、失败信息与实时事件。
- 可选 Agent 能力仅处理确定性规则无法唯一判断的异常场景，提交结果仍由后端规则验证。
- 通过首次安装向导配置管理员、PostgreSQL、外部服务与转码参数。

## 系统架构

```text
Browser -> Web/Nginx -> API <-> PostgreSQL / River <-> Worker
                                                          |
                                                          +-> qBittorrent / TMDb / Emby / FFmpeg
```

| 组件 | 职责 |
| --- | --- |
| Web / Nginx | 提供管理界面、静态资源、API 反向代理、SSE 与媒体预览 |
| API | 处理安装、认证、查询、命令和短事务 |
| Worker | 执行下载、映射、转码、字幕、文件整理、入库和清理任务 |
| PostgreSQL / River | 保存业务状态、事件、配置与持久化任务队列 |

API 与 Worker 始终以独立进程运行。只有视频转码受专用并发槽位限制，其它后台任务使用通用队列。

[`contracts/openapi.yaml`](contracts/openapi.yaml) 是 HTTP 接口的唯一契约。Go 服务端类型与 TypeScript SDK 均由该契约生成。

## 部署

Emby Auto 支持以下 Linux 部署方式：

| 方式 | 适用场景 | 运行组件 | 文档 |
| --- | --- | --- | --- |
| Docker Compose | 由容器统一管理应用与 PostgreSQL | PostgreSQL、API、Worker、Web | [Docker Compose 部署](docs/deployment.md) |
| 直接部署 | 主机已使用 systemd、Nginx 和 PostgreSQL | API、Worker、Web 静态文件 | [Linux 直接部署](docs/direct-deployment.md) |

qBittorrent 与 Emby Server 在两种部署方式中均作为外部服务接入。

### Docker Compose 快速启动

```bash
cp deploy/.env.example deploy/.env
# 配置数据库密码、运行 UID/GID 和媒体目录
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

Web 默认绑定到 `127.0.0.1:8080`。如果该端口已被 qBittorrent 或其它服务占用，应在 `deploy/.env` 中修改 `APP_PORT`。

### Linux 直接部署包

```bash
npm run deploy:direct:build
npm run deploy:direct:check-release
```

构建结果包含 Linux 二进制、Web 静态文件、systemd 与 Nginx 模板、部署文档和 SHA-256 校验和，并支持 `amd64` 与 `arm64`。

### 部署要求

- qBittorrent、Worker 与宿主机必须使用一致的下载绝对路径。
- Worker 使用的非 root UID 必须与 Emby 媒体文件所有者一致。
- API 与 Worker 共享 PostgreSQL 和安装配置，但保持独立进程。
- 对外服务必须通过 HTTPS，并设置 `SESSION_COOKIE_SECURE=true`。
- 数据库、安装配置和媒体目录必须纳入独立备份策略。
- 应用不得获得 Docker socket、宿主根目录、systemd 总线或非必要 Linux capabilities。

## 本地开发

### 环境要求

- Go 1.26+
- Node.js 24+
- npm 11+
- PostgreSQL 17，或可运行 Docker Compose 的本地环境

安装依赖和固定版本的开发工具：

```bash
npm ci
npm run tools:download
npm run generate
```

启动开发数据库并执行应用与 River 迁移：

```bash
npm run db:start
npm run db:migrate
npm run db:river:migrate
```

分别启动 API、Worker 和 Web：

```bash
npm run start:api
npm run start:worker
npm run dev --workspace @emby-auto/web
```

开发 Web 默认监听 `http://127.0.0.1:5173`，并将 `/api` 代理到 `http://127.0.0.1:8080`。本地凭据应写入未跟踪的 `.env` 或安装生成的 `bootstrap.json`。

### 代码生成

修改 OpenAPI 契约或数据库查询后运行：

```bash
npm run generate
npm run check:generated
```

以下目录和文件由生成器维护，不应直接编辑：

- `backend/internal/transport/httpapi/api.gen.go`
- `backend/db/sqlc`
- `apps/web/src/api/generated`

## 质量保证

| 命令 | 检查范围 |
| --- | --- |
| `npm run check` | 格式、静态分析、单元测试、构建与生成文件漂移 |
| `npm run check:integration` | 数据库迁移、约束与 PostgreSQL 集成测试 |
| `npm run test:e2e --workspace @emby-auto/web` | 浏览器界面测试 |
| `npm run deploy:check` | Docker Compose 配置与安全约束 |
| `npm run deploy:direct:check` | Linux 直接部署模板 |
| `npm run deploy:direct:build` | Linux 直接部署发布包 |

## 仓库结构

```text
apps/web/       React/Vite 管理界面
backend/        Go API、Worker、领域代码、迁移与 sqlc 查询
contracts/      OpenAPI 接口契约
deploy/         Compose、systemd、Nginx 和运行环境模板
docs/           部署与运维文档
scripts/        开发、测试、生成、发布与验收脚本
tools/          固定版本的 Go 开发工具模块
```

## 安全

管理会话使用 HttpOnly、SameSite Cookie；外部服务密钥在 PostgreSQL 中加密存储。安全问题应按照 [安全策略](SECURITY.md) 通过私密渠道报告。

## 贡献

提交 Issue 或 Pull Request 前，应阅读 [贡献指南](CONTRIBUTING.md)。

## 第三方服务

Emby Auto 不提供或分发媒体内容，部署者应确保资源获取与处理行为符合适用法律及第三方服务条款。本项目为独立项目，与 Emby 或 qBittorrent 不存在隶属或认可关系。

本项目使用 TMDB API，但未获得 TMDB 的认可或认证。

## 许可证

Emby Auto 基于 [MIT License](LICENSE) 发布。
