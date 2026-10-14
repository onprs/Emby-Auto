# Linux 直接部署

**简体中文** | [English](direct-deployment.en.md)

本文档面向在自有 Linux 设备上安装 Emby Auto 的用户。可以使用一键安装器自动准备运行环境，也可以下载预编译发布包手动安装。qBittorrent 与 Emby Server 需要提前准备。

## 一键安装

支持 Debian 12/13、Ubuntu 22.04/24.04/26.04 的 64 位 `amd64` 和 `arm64` 系统。设备需要运行 systemd，具备 root 或 sudo 权限，并已安装 Bash、curl。网络需要能访问 GitHub、发行版软件源，以及需要时使用的 PostgreSQL 官方软件源。

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s --
```

root 账户执行时，可将 `sudo bash` 替换为 `bash`。安装器自动下载并校验发布包，安装 PostgreSQL 17、Nginx、FFmpeg 与 ffprobe，创建非 root 账户和存储目录，并启用开机启动。目标设备无需 Git、Go 或 Node.js。

| 选项 | 默认值 | 用途 |
| --- | --- | --- |
| `--version` | 最新正式 Release | 指定 `v` 开头的版本号 |
| `--user` | 自动创建 `emby-auto` | 使用已有的媒体文件所有者账户 |
| `--port` | `18080` | 网页回环端口 |
| `--api-port` | `18081` | API 回环端口 |
| `--database-port` | `18082` | 独立 PostgreSQL 回环端口 |
| `--help` | — | 显示帮助 |

选项附加在命令末尾，例如使用现有媒体账户：

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s -- --user 媒体账户
```

三个端口必须互不相同，范围为 `1024–65535`。已有媒体目录会保留原权限；指定账户需要具备目录读写权限。硬件转码用户还需将服务账户加入设备要求的 `render`、`video` 等组。

安装器为应用创建名为 `17/emby_auto` 的独立 PostgreSQL 实例，默认仅监听 `127.0.0.1:18082`。数据库连接由安装器写入受限的运行配置，网页向导会跳过数据库步骤。凭据不会显示在安装日志中。

### 打开网页并完成设置

同机浏览器打开 `http://127.0.0.1:18080`。远程部署时，在自己的电脑建立 SSH 隧道：

```bash
ssh -L 18080:127.0.0.1:18080 用户@设备地址
```

随后打开同一网页地址。修改网页端口时，相应调整隧道和浏览器地址。

在网页向导中创建管理员，填写 qBittorrent Web UI 地址与凭据、Emby 地址与 API key、TMDb API Read Access Token，并确认下载与媒体目录。地址必须能够从部署设备访问；qBittorrent 的下载绝对路径必须与向导一致。位于其它设备的下载目录与媒体库需要先挂载到部署设备。

默认目录为 `/srv/emby-auto/downloads`、`/srv/emby-auto/work`、`/srv/emby-auto/staging`、`/srv/emby-auto/media/anime` 和 `/srv/emby-auto/media/movies`。已有媒体库应在向导中填写实际路径，并确保服务账户 UID 与媒体文件所有者一致。

网络对外访问需要先配置 HTTPS，并将 `/etc/emby-auto/runtime.env` 中的 `SESSION_COOKIE_SECURE` 改为 `true`，再重启 API。默认回环入口适用于本机访问和 SSH 隧道。

### 重试、日志与备份

安装中断后可用相同参数重试，已生成的数据库凭据、配置和媒体目录会保留。已完成的安装会检查当前服务并显示访问地址，保留原版本。安装器不接管手动部署，升级请按本文的升级步骤操作。

```bash
systemctl status emby-auto-api emby-auto-worker postgresql@17-emby_auto --no-pager
journalctl -u emby-auto-api -u emby-auto-worker --since today
```

备份需要包含应用数据库、`/etc/emby-auto`、`/var/lib/emby-auto` 和媒体目录。自动安装的数据库可由 PostgreSQL 管理员备份：

```bash
sudo -u postgres pg_dump -p 18082 -Fc emby_auto > emby-auto.dump
```

数据库端口自定义时需同步调整。备份包含敏感信息，应加密保存并限制访问权限。

## 手动部署

以下步骤用于自行管理运行依赖和服务账户的部署。使用一键安装器的用户已经完成这些配置，可以直接查看 HTTPS、备份与升级说明。

### 部署拓扑

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

### 前置条件

目标主机需要：

- 64 位 Linux（`amd64` 或 `arm64`）与 systemd。
- PostgreSQL 17。
- 发行版提供的 Nginx。
- FFmpeg 与 ffprobe，并包含转码配置所需的编码器。
- 可访问的 qBittorrent Web API 与 Emby Server。
- TMDb API Read Access Token。
- 与 Emby 媒体文件所有者一致的非 root 服务账户；手动部署步骤使用 `emby`。
- Bash、curl、GNU coreutils、tar 和 gzip。

### 1. 下载与校验发布包

在目标设备下载对应架构的预编译发布包。以下命令用于 `amd64`；`arm64` 主机将 `target_arch` 改为 `arm64`：

```bash
target_arch=amd64
archive="emby-auto-direct-${target_arch}-linux-${target_arch}.tar.gz"
release_url=https://github.com/onprs/Emby-Auto/releases/latest/download
curl -fL "${release_url}/${archive}" -o "${archive}"
curl -fL "${release_url}/SHA256SUMS" -o SHA256SUMS
awk -v name="${archive}" '$2 == name' SHA256SUMS | sha256sum --check --strict -
tar -xzf "${archive}"
cd "emby-auto-direct-${target_arch}"
sha256sum -c SHA256SUMS
```

逐条确认命令成功后再继续，校验失败的发布包不得安装。发布包包含应用、网页、服务模板、安装器和部署文档。指定版本时，将下载地址中的 `latest/download` 替换为 `download/v版本号`。

从源码构建的维护流程见 [开发与发布文档](https://github.com/onprs/Emby-Auto/blob/main/docs/development.md)。

### 2. 准备服务账户与存储目录

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

### 3. 配置 PostgreSQL

以 PostgreSQL 管理员身份创建专用角色和数据库。密码通过交互提示输入：

```bash
sudo -u postgres createuser --pwprompt emby_auto
sudo -u postgres createdb --owner=emby_auto emby_auto
```

PostgreSQL 不应暴露到公网。同机数据库可通过 `127.0.0.1:5432` 访问；远程数据库应位于受信网络并启用适当的 TLS 模式。

应用会在首次安装和后续启动时依次执行应用迁移与 River 迁移。迁移表由应用管理，不应手工修改。

### 4. 安装版本目录

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

### 5. 安装运行环境与 systemd 服务

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

### 6. 安装 Nginx

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

### 7. 完成首次安装

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

### 8. 配置 HTTPS

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

### 9. 备份与监控

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

### 10. 升级

从 GitHub Release 下载并校验新发布包，将其安装到新的 `/opt/emby-auto/releases/<release-id>` 目录。升级前应完成备份，并确认下载后处理、转码、入库和 Agent 操作均已结束。

一键安装使用的账户配置保存在 systemd 的 `installer.conf` drop-in 中，更新 unit 后仍然生效。使用自定义网页或 API 端口时，需要在下列 Nginx 模板安装后、配置检查前恢复实际端口。

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

### 11. 回滚与卸载

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
