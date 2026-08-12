# Linux 直接部署

**简体中文** | [English](direct-deployment.en.md)

本文档说明如何在不使用 Docker 的 Linux 主机上部署 Emby Auto。API 与 Worker 由两个独立 systemd 服务运行，Nginx 托管 Web 静态文件并反向代理 API。PostgreSQL、qBittorrent 与 Emby Server 由部署者独立维护。

## 部署拓扑

| 组件 | 默认位置或端口 | 管理方式 |
| --- | --- | --- |
| API | `127.0.0.1:18081` | `emby-auto-api.service` |
| Worker | 无监听端口 | `emby-auto-worker.service` |
| Web / Nginx | `127.0.0.1:18080` | 主机 Nginx |
| 版本目录 | `/opt/emby-auto/releases/<release-id>` | root 只读管理 |
| 当前版本 | `/opt/emby-auto/current` | 原子符号链接 |
| 运行状态 | `/var/lib/emby-auto` | systemd `StateDirectory` |
| 运行环境 | `/etc/emby-auto/runtime.env` | root 管理 |

标准直接部署不安装宿主控制器。Dashboard 中依赖宿主控制的 Worker 启停功能不可用，服务生命周期由 `systemctl` 管理。

## 前置条件

目标主机需要：

- 64 位 Linux（`amd64` 或 `arm64`）与 systemd。
- PostgreSQL 17。
- Nginx 1.24 或更高版本。
- FFmpeg 与 ffprobe，并包含转码配置所需的编码器。
- 可访问的 qBittorrent Web API 与 Emby Server。
- TMDb API Read Access Token。
- 与 Emby 媒体文件所有者一致的非 root 服务账户；本文使用 `emby`。

从源码构建发布包还需要：

- Go 1.26+。
- Node.js 24+ 与 npm 11+。
- GNU coreutils、`file`、`tar` 和 `gzip`。

构建机与目标机可以是同一主机。

## 1. 构建与校验发布包

在仓库根目录构建默认的 Linux `amd64` 发布包：

```bash
npm run deploy:direct:build
npm run deploy:direct:check-release
```

构建 Linux `arm64` 发布包：

```bash
TARGET_GOARCH=arm64 npm run deploy:direct:build
TARGET_GOARCH=arm64 npm run deploy:direct:check-release
```

输出位置：

```text
runtime/release/emby-auto-direct/
runtime/release/emby-auto-direct-linux-<arch>.tar.gz
```

发布包包含 API、Worker、迁移检查工具、Web 静态文件、systemd、Nginx 和运行环境模板、中英文部署文档、MIT 许可证以及 `SHA256SUMS`。

将对应架构的压缩包传输到目标主机后执行：

```bash
tar -xzf emby-auto-direct-linux-amd64.tar.gz
cd emby-auto-direct
sha256sum -c SHA256SUMS
```

`arm64` 主机应替换为对应文件名。校验失败的发布包不得继续安装。

## 2. 准备服务账户与存储目录

确认服务账户的 UID/GID 均大于 0：

```bash
id emby
id -u emby
id -g emby
```

如果媒体文件由其它账户管理，部署时需要统一调整：

- 两个 systemd 服务的 `User` 与 `Group`。
- `/etc/emby-auto/runtime.env` 中的 `EMBY_MEDIA_OWNER_UID`。
- 状态、下载、工作、暂存和媒体目录的所有权。

API 与 Worker 使用同一服务账户，以便共同读取权限为 `0600` 的安装配置；Worker 同时以媒体文件所有者身份创建和修复入库文件。两个服务都不得使用 root，也不需要 `CAP_CHOWN`。

以下命令创建示例目录：

```bash
sudo install -d -o emby -g emby -m 0770 \
  /srv/emby-auto/downloads \
  /srv/emby-auto/work \
  /srv/emby-auto/staging \
  /srv/emby-auto/media/anime \
  /srv/emby-auto/media/movies
```

已有媒体库应保留现有权限策略，不应执行无差别递归 `chown` 或 `chmod`。qBittorrent 的下载保存路径必须与 Worker 配置的绝对路径完全一致；qBittorrent 位于容器中时也需要使用同路径挂载。

使用 VAAPI、NVENC 或其它硬件编码器时，应将服务账户加入对应的 `render`、`video` 或设备专用组，并重启服务以应用组成员关系。

## 3. 配置 PostgreSQL

以 PostgreSQL 管理员身份创建专用角色和数据库。密码通过交互提示输入：

```bash
sudo -u postgres createuser --pwprompt emby_auto
sudo -u postgres createdb --owner=emby_auto emby_auto
```

PostgreSQL 不应暴露到公网。同机数据库可通过 `127.0.0.1:5432` 访问；远程数据库应位于受信网络并启用适当的 TLS 模式。

应用会在首次安装和后续启动时依次执行应用迁移与 River 迁移。迁移表由应用管理，不应手工修改。

## 4. 安装版本目录

在校验通过的发布目录中执行：

```bash
release_source="$PWD"
release_id="$(date -u +%Y%m%d%H%M%S)"

sudo install -d -o root -g root -m 0755 /opt/emby-auto/releases
sudo install -d -o root -g root -m 0755 "/opt/emby-auto/releases/${release_id}"
sudo cp -a "${release_source}/." "/opt/emby-auto/releases/${release_id}/"
sudo chown -R root:root "/opt/emby-auto/releases/${release_id}"

sudo ln -sfn "releases/${release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
```

版本目录由 root 管理，服务账户仅需读取二进制、模板和 Web 文件。运行状态由 systemd 写入 `/var/lib/emby-auto`，不保存在版本目录中。

## 5. 安装运行环境与 systemd 服务

创建运行环境文件：

```bash
sudo install -d -o root -g root -m 0755 /etc/emby-auto
sudo install -o root -g emby -m 0640 \
  /opt/emby-auto/current/config/runtime.env.example \
  /etc/emby-auto/runtime.env
sudoedit /etc/emby-auto/runtime.env
```

至少设置 `EMBY_MEDIA_OWNER_UID`。通过本机回环或 SSH tunnel 完成首次安装时，可暂时保持 `SESSION_COOKIE_SECURE=false`；HTTPS 可用后必须改为 `true`。

安装 systemd unit：

```bash
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service \
  /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service \
  /etc/systemd/system/emby-auto-worker.service
sudo systemctl daemon-reload
```

默认 unit 使用 `emby:emby`。使用其它服务账户时，应为两个服务分别创建持久化 drop-in，而不是直接修改发布包中的 unit：

```bash
sudo systemctl edit emby-auto-api.service
sudo systemctl edit emby-auto-worker.service
```

两个 drop-in 均写入：

```ini
[Service]
User=<media-user>
Group=<media-group>
```

同时将 `/etc/emby-auto/runtime.env` 的 group 和 `EMBY_MEDIA_OWNER_UID` 调整为对应账户。

加载并启动服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-api.service
sudo systemctl enable --now emby-auto-worker.service
```

Worker 会在首次安装完成前等待安装配置，不会领取后台任务。

## 6. 安装 Nginx

安装共享 location 配置与直接部署站点：

```bash
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-locations.conf \
  /etc/nginx/emby-auto-locations.conf
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/nginx.conf \
  /etc/nginx/conf.d/emby-auto.conf
sudo nginx -t
sudo systemctl reload nginx
```

如果发行版未加载 `/etc/nginx/conf.d/*.conf`，应将站点配置安装到该发行版启用的 Nginx server 配置目录。共享 `emby-auto-locations.conf` 的 include 路径必须保持一致。

默认站点监听 `127.0.0.1:18080`，API 监听 `127.0.0.1:18081`。验证两个入口：

```bash
curl --fail http://127.0.0.1:18081/api/v1/health/live
curl --fail http://127.0.0.1:18080/api/v1/health/live
```

## 7. 完成首次安装

远程管理机可通过 SSH tunnel 访问回环站点：

```bash
ssh -L 8080:127.0.0.1:18080 user@example-host
```

随后在管理机打开 `http://127.0.0.1:8080`。同机部署的典型安装参数如下：

```text
PostgreSQL host   127.0.0.1
PostgreSQL port   5432
PostgreSQL db     emby_auto
PostgreSQL user   emby_auto
PostgreSQL SSL    disable（仅限同机回环）
qBittorrent URL   http://127.0.0.1:<qbittorrent-port>
Emby URL          http://127.0.0.1:<emby-port>/emby
ffmpegPath        /usr/bin/ffmpeg
ffprobePath       /usr/bin/ffprobe
downloadRoot      /srv/emby-auto/downloads
workRoot          /srv/emby-auto/work
stagingRoot       /srv/emby-auto/staging
animeLibraryRoot  /srv/emby-auto/media/anime
movieLibraryRoot  /srv/emby-auto/media/movies
```

安装向导会将数据库连接和生成的配置加密密钥写入 `/var/lib/emby-auto/bootstrap.json`，文件权限为 `0600`。该文件包含敏感配置，不应复制到非受信位置、输出到日志或提交到版本控制。

安装完成后执行就绪检查：

```bash
curl --fail http://127.0.0.1:18081/api/v1/health/ready
sudo -u emby bash -c '
  set -a
  source /etc/emby-auto/runtime.env
  set +a
  exec /opt/emby-auto/current/bin/emby-auto-worker --check
'
```

使用其它服务账户时，应替换命令中的 `emby`。同时确认两个服务均为 active，日志中不存在重复启动或连接错误：

```bash
systemctl status emby-auto-api emby-auto-worker --no-pager
journalctl -u emby-auto-api -u emby-auto-worker --since today
```

## 8. 配置 HTTPS

局域网或公网访问应在 `127.0.0.1:18080` 前部署 Caddy、Nginx、Traefik 或其它 TLS 入口。外层代理必须保留：

- `/api/v1/events` 的长连接 SSE 与无缓冲响应。
- 媒体产物请求的 `Range`、`If-Range` 与流式传输。
- `/api/` 代理、静态资源缓存和 React SPA 回退。

TLS 验证完成后：

```bash
sudoedit /etc/emby-auto/runtime.env
sudo systemctl restart emby-auto-api.service
```

确认 `SESSION_COOKIE_SECURE=true`，并验证会话 Cookie 包含 `HttpOnly`、`SameSite=Strict` 和 `Secure`。防火墙不应开放 API 端口 `18081`；默认的 `18080` 也应保持回环监听，由外层 TLS 入口对外提供服务。

## 9. 备份与监控

备份范围至少包括：

- PostgreSQL 一致性备份。
- `/etc/emby-auto/runtime.env`。
- `/var/lib/emby-auto`，其中包含安装配置。
- 媒体目录及应用工作目录，按存储系统策略处理。

运行状态与日志：

```bash
systemctl status emby-auto-api emby-auto-worker --no-pager
journalctl -u emby-auto-api -u emby-auto-worker -f
curl --fail http://127.0.0.1:18081/api/v1/health/ready
```

备份可能包含数据库连接与外部服务凭据，必须加密保存并限制访问权限。

## 10. 升级

先在构建机生成并校验新发布包，再将其安装到新的 `/opt/emby-auto/releases/<release-id>` 目录。升级前应完成备份，并确认下载后处理、转码、入库和 Agent 操作均已结束。

切换版本：

```bash
new_release_id="<installed-release-id>"

sudo systemctl stop emby-auto-worker.service
sudo systemctl stop emby-auto-api.service

sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current

sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service \
  /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service \
  /etc/systemd/system/emby-auto-worker.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-locations.conf \
  /etc/nginx/emby-auto-locations.conf
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/nginx.conf \
  /etc/nginx/conf.d/emby-auto.conf

sudo systemctl daemon-reload
sudo nginx -t
sudo systemctl start emby-auto-api.service
curl --fail http://127.0.0.1:18081/api/v1/health/ready
sudo systemctl start emby-auto-worker.service
sudo systemctl reload nginx
```

不得使用新模板覆盖 `/etc/emby-auto/runtime.env`。systemd drop-in 会在 unit 更新后继续生效。API 就绪检查失败时，应保持 Worker 停止并检查迁移日志。

## 11. 回滚与卸载

只有在旧二进制与当前数据库结构兼容时，才能直接将 `current` 原子切回旧版本。发生不兼容数据库迁移时，应停止 API 与 Worker，并从同一时间点恢复 PostgreSQL 和 `/var/lib/emby-auto` 备份后再切换版本。

卸载服务但保留数据：

```bash
sudo systemctl disable --now emby-auto-worker.service emby-auto-api.service
sudo rm -f /etc/systemd/system/emby-auto-worker.service
sudo rm -f /etc/systemd/system/emby-auto-api.service
sudo rm -rf /etc/systemd/system/emby-auto-worker.service.d
sudo rm -rf /etc/systemd/system/emby-auto-api.service.d
sudo rm -f /etc/nginx/conf.d/emby-auto.conf
sudo rm -f /etc/nginx/emby-auto-locations.conf
sudo systemctl daemon-reload
sudo nginx -t && sudo systemctl reload nginx
```

仅在确认不再需要安装数据时，才可删除 PostgreSQL 数据库、`/var/lib/emby-auto`、`/etc/emby-auto` 和版本目录。媒体库不属于应用卸载范围。
