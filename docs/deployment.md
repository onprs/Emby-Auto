# Docker Compose 部署

**简体中文** | [English](deployment.en.md)

本文档说明如何使用项目提供的 Docker Compose 配置部署 Emby Auto。Compose 管理 PostgreSQL、API、Worker 和 Web；qBittorrent 与 Emby Server 作为外部服务接入。

## 部署拓扑

| 服务 | 网络入口 | 持久化内容 |
| --- | --- | --- |
| `postgres` | 仅 Compose 内部网络 | `postgres-data` 命名卷 |
| `api` | 仅 Compose 内部网络 | `app-data` 与媒体目录 |
| `worker` | 无监听端口 | `app-data` 与媒体目录 |
| `web` | 默认 `127.0.0.1:8080` | 无 |

API 与 Worker 是独立进程，共用 PostgreSQL、安装配置和媒体目录。Compose 不会发布 PostgreSQL 或 API 端口，也不会向应用挂载 Docker socket 或宿主 init system。

## 前置条件

- 64 位 Linux 主机。
- Docker Engine 24 或更高版本。
- Docker Compose v2。
- 可访问的 qBittorrent Web API 与 Emby Server。
- TMDb API Read Access Token。
- 五个可由应用读写的绝对目录。

## 1. 配置环境

复制环境模板并限制文件权限：

```bash
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
```

至少检查以下配置：

| 变量 | 说明 |
| --- | --- |
| `POSTGRES_PASSWORD` | PostgreSQL 专用长随机密码 |
| `APP_UID` / `APP_GID` | Emby 媒体文件所有者的非 root UID/GID |
| `DOWNLOAD_ROOT` | qBittorrent 下载与应用读取共用目录 |
| `WORK_ROOT` | 媒体处理中间文件目录 |
| `STAGING_ROOT` | 入库前暂存目录 |
| `ANIME_LIBRARY_ROOT` | Emby 动漫媒体库目录 |
| `MOVIE_LIBRARY_ROOT` | Emby 电影媒体库目录 |
| `APP_BIND_ADDRESS` / `APP_PORT` | Web 在宿主机上的监听地址与端口 |
| `SESSION_COOKIE_SECURE` | HTTPS 启用后设置为 `true` |

可使用以下命令查询 Emby 服务账户：

```bash
id -u emby
id -g emby
```

`APP_UID` 和 `APP_GID` 必须大于 0。应用专用目录应提前创建，并允许该 UID/GID 读写。已有媒体库应遵循现有权限策略，不应为部署应用而执行无差别递归 `chown` 或 `chmod`。

所有媒体目录都必须使用绝对路径。Compose 会在宿主机与 API/Worker 容器中保留相同路径；如果 qBittorrent 运行在另一个容器中，其下载目录也必须挂载到相同的容器路径。

Web 默认使用宿主端口 `8080`。该端口已被 qBittorrent 或其它服务占用时，应在 `deploy/.env` 中为 `APP_PORT` 配置空闲端口。

## 2. 启动与首次安装

构建并启动服务：

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

查看服务状态与日志：

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
docker compose --env-file deploy/.env -f deploy/compose.yaml logs -f api worker
```

打开 `http://127.0.0.1:<APP_PORT>` 完成首次安装。安装向导中的 PostgreSQL 参数如下：

```text
主机        postgres
端口        5432
数据库      POSTGRES_DB 的值
用户        POSTGRES_USER 的值
密码        POSTGRES_PASSWORD 的值
SSL         disable
```

媒体工具与目录使用以下值：

```text
ffmpegPath       /usr/bin/ffmpeg
ffprobePath      /usr/bin/ffprobe
downloadRoot     DOWNLOAD_ROOT 的值
workRoot         WORK_ROOT 的值
stagingRoot      STAGING_ROOT 的值
animeLibraryRoot ANIME_LIBRARY_ROOT 的值
movieLibraryRoot MOVIE_LIBRARY_ROOT 的值
```

同一宿主机上的外部服务可通过 `host.docker.internal` 访问，例如：

```text
qBittorrent URL  http://host.docker.internal:<qbittorrent-port>
Emby URL         http://host.docker.internal:<emby-port>/emby
```

端口和 Emby 基础路径应以实际服务配置为准。

安装向导会把数据库连接和生成的配置加密密钥写入 `app-data` 卷中的 `bootstrap.json`。Worker 会在安装完成前等待，不会领取后台任务。

## 3. 配置 HTTPS

局域网或公网访问应在 Web 服务前部署 TLS 反向代理，并保持 `APP_BIND_ADDRESS=127.0.0.1`。外层代理的上游地址为 `http://127.0.0.1:<APP_PORT>`。

反向代理必须保留以下行为：

- `/api/v1/events` 使用长连接 SSE，并禁用响应缓冲。
- `/api/v1/tasks/{taskId}/artifacts/{video|subtitle}` 透传 `Range` 与 `If-Range`。
- 其它 `/api/` 请求与 Web 路由均转发到 Web 服务。

TLS 验证完成后，在 `deploy/.env` 中设置：

```dotenv
SESSION_COOKIE_SECURE=true
```

重新创建后端服务以应用配置：

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --force-recreate api worker
```

## 4. 日常运维

### 服务管理

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
docker compose --env-file deploy/.env -f deploy/compose.yaml logs --tail=200 api worker
docker compose --env-file deploy/.env -f deploy/compose.yaml stop worker
docker compose --env-file deploy/.env -f deploy/compose.yaml start worker
```

标准 Compose 部署不包含宿主控制器，因此 Dashboard 中依赖宿主控制的 Worker 启停功能不可用。Worker 生命周期由 Compose 管理。

### 备份

升级或维护前应备份：

- `postgres-data` 中的 PostgreSQL 数据，包括业务数据与安装状态；优先使用 `pg_dump` 生成一致性备份。
- `app-data` 中的 `bootstrap.json`。
- `deploy/.env`。
- 媒体目录，按存储系统的既有策略独立备份。

备份文件包含数据库连接或外部服务凭据时，应加密保存并限制访问权限。

### 升级

在应用处于空闲状态且备份完成后执行：

```bash
git pull --ff-only
docker compose --env-file deploy/.env -f deploy/compose.yaml build
docker compose --env-file deploy/.env -f deploy/compose.yaml stop worker
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --wait postgres api web
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T api \
  curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d worker
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
```

API 启动时会依次执行应用迁移与 River 迁移。就绪检查失败时，应保持 Worker 停止并检查 API 日志；不得让旧 Worker 连接迁移失败或不兼容的数据库。

### 停止与删除容器

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml down
```

该命令保留命名卷。仅在确认永久删除 PostgreSQL 数据和安装配置时才可使用 `down --volumes`。

## 故障排查

- `api` 为 `unhealthy`：检查 PostgreSQL 健康状态、API 日志和 `bootstrap.json` 所在卷。
- Worker 无法读取下载：核对 qBittorrent 与 Worker 使用的绝对路径是否完全一致。
- 入库出现权限错误：核对 `APP_UID`、`APP_GID` 与媒体目录所有权。
- HTTPS 登录后会话丢失：确认 `SESSION_COOKIE_SECURE=true`、外层代理传递原始协议且浏览器通过 HTTPS 访问。
