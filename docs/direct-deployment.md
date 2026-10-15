# Linux 直接部署

**简体中文** | [English](direct-deployment.en.md)

本文档面向在自有 Linux 设备上安装 Emby Auto 的用户。安装器负责应用、FFmpeg 与 ffprobe，并配置应用开机启动。PostgreSQL、qBittorrent 和 Emby Server 由用户自行安装配置；网页与 API 由应用直接提供，可按需选择反向代理。

## 一键安装

### 使用条件

- Debian 12/13 或 Ubuntu 22.04/24.04/26.04，64 位 `amd64` / `arm64`，正在运行 systemd。
- root 或 sudo 权限，已安装 Bash 与 curl，能够访问 GitHub 和系统软件源。
- 可访问的 PostgreSQL 数据库和应用专用账户。项目的数据库验证基线为 PostgreSQL 17。
- 已准备 qBittorrent Web API、Emby Server 与 TMDb API Read Access Token。

执行：

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s --
```

root 账户执行时，可将 `sudo bash` 替换为 `bash`。目标设备无需 Docker、Git、Go 或 Node.js。安装器保留现有数据库、反向代理及其配置。

已有媒体库时，使用媒体文件所有者账户安装：

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s -- --user 媒体账户
```

默认创建非 root 账户 `emby-auto`，并在 `/srv/emby-auto` 创建下载、工作、暂存和媒体目录。指定现有账户时，该账户必须能够读写所选目录；已有媒体目录的所有权和权限保持原样。

### 打开初始化向导

同机浏览器打开 `http://127.0.0.1:18080`。远程设备可先在自己的电脑建立 SSH 隧道：

```bash
ssh -L 18080:127.0.0.1:18080 用户@设备地址
```

随后打开同一网页地址，依次填写：

1. 自行准备的 PostgreSQL 主机、端口、数据库、账户、密码与 SSL 模式。
2. 管理员账户。
3. qBittorrent、Emby 和 TMDb 信息。
4. 媒体目录与 FFmpeg 工具路径。
5. 转码配置。

应用会在选定数据库中创建所需表结构。服务账户需要能够访问填写的数据库地址和媒体目录。Worker 在初始化完成前等待，不会领取后台任务。

### 安装选项

| 选项 | 说明 |
| --- | --- |
| `--version v版本号` | 安装指定 Release，默认最新正式版本 |
| `--user 账户` | 使用现有媒体所有者账户，默认创建 `emby-auto` |
| `--port 端口` | 网页与 API 共用的回环端口，默认 `18080` |

端口被占用时，例如使用 `--port 19080`，访问地址和 SSH 隧道也应改用该端口。重复执行保留当前版本、初始化状态和运行配置。更换版本按下文升级步骤操作。

## 部署目录与服务

| 内容 | 位置或入口 |
| --- | --- |
| 当前版本 | `/opt/emby-auto/current` |
| 版本目录 | `/opt/emby-auto/releases/` |
| 运行环境 | `/etc/emby-auto/runtime.env` |
| 安装记录 | `/etc/emby-auto/installer.env` |
| 初始化配置 | `/var/lib/emby-auto/bootstrap.json` 与 `.complete` 标记 |
| 网页与 API | 默认 `127.0.0.1:18080` |
| 后台服务 | `emby-auto-api.service`、`emby-auto-worker.service` |

API 与 Worker 是独立进程。数据库、反向代理、qBittorrent 和 Emby 的生命周期由用户管理。原生部署的应用服务由 `systemctl` 启停；Dashboard 中依赖宿主控制器的 Worker 启停功能需要另行配置宿主集成。

## 手动部署

### 1. 准备依赖和发布包

自行准备 PostgreSQL 数据库、专用账户、FFmpeg 和 ffprobe；配置开机启动需要 systemd。下载并校验对应架构的发布包：

```bash
release_tag="<release-tag>"
target_arch="amd64" # arm64 设备改为 arm64
archive="emby-auto-direct-${target_arch}-linux-${target_arch}.tar.gz"
curl -fLO "https://github.com/onprs/Emby-Auto/releases/download/${release_tag}/${archive}"
curl -fLO "https://github.com/onprs/Emby-Auto/releases/download/${release_tag}/SHA256SUMS"
awk -v name="${archive}" '$2 == name' SHA256SUMS | sha256sum --check --strict -
tar -xzf "${archive}"
cd "emby-auto-direct-${target_arch}"
sha256sum -c SHA256SUMS
```

逐条确认命令成功后再继续，校验失败的发布包应重新下载。

### 2. 准备数据库和媒体账户

数据库管理员需要提前创建应用专用数据库和账户，并允许应用账户在该数据库中创建表和扩展。应用使用 `pgcrypto` 扩展；受限的托管数据库应由管理员提前启用。同机数据库可使用回环地址；远程数据库应处于受信网络并配置适当的 TLS 模式。

使用现有媒体所有者账户运行应用。以下示例使用 `emby`；使用其它账户时替换相应账户和组，并查询实际 UID：

```bash
id -u emby
id -g emby
```

服务账户的 UID 必须与入库媒体的所有者一致，并能读写下载、工作、暂存和媒体目录。qBittorrent 与应用使用的下载绝对路径需要一致。已有媒体库按现有权限策略管理。API 与 Worker 使用同一非 root 账户，UID 和 GID 均应大于 0，以便共享权限为 `0600` 的初始化配置。

新部署尚未创建媒体目录时，可使用以下示例；已有目录保持原有权限：

```bash
sudo install -d -o emby -g emby -m 0770 \
  /srv/emby-auto/downloads \
  /srv/emby-auto/work \
  /srv/emby-auto/staging \
  /srv/emby-auto/media/anime \
  /srv/emby-auto/media/movies
```

使用硬件转码时，将服务账户加入设备需要的 `render`、`video` 等组，并重启服务以应用组成员关系。

### 3. 安装版本与运行环境

在校验通过的发布目录中执行：

```bash
release_source="$PWD"
release_id="$(date -u +%Y%m%d%H%M%S)"
sudo install -d -o root -g root -m 0755 "/opt/emby-auto/releases/${release_id}"
sudo cp -a "${release_source}/." "/opt/emby-auto/releases/${release_id}/"
sudo chown -R root:root "/opt/emby-auto/releases/${release_id}"
sudo ln -sfn "releases/${release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
sudo install -d -o root -g root -m 0755 /etc/emby-auto
sudo install -o root -g emby -m 0640 \
  /opt/emby-auto/current/config/runtime.env.example /etc/emby-auto/runtime.env
sudoedit /etc/emby-auto/runtime.env
```

将 `EMBY_MEDIA_OWNER_UID` 设为服务账户的实际非 root UID。默认配置为：

```dotenv
API_ADDRESS=127.0.0.1:18080
WEB_ROOT=/opt/emby-auto/current/web
BOOTSTRAP_CONFIG_PATH=/var/lib/emby-auto/bootstrap.json
SESSION_COOKIE_SECURE=false
```

保留数据库连接和加密密钥为空，首次打开页面时由初始化向导设置。`WEB_ROOT` 指定应用提供的网页目录；独立托管网页时可留空，并自行将 `/api/` 转发到应用。

### 4. 配置应用服务

```bash
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service /etc/systemd/system/emby-auto-worker.service
```

默认 unit 使用 `emby:emby`。使用其它服务账户时，为两个服务分别创建持久化 drop-in，并将运行环境文件的 group 调整为该账户所属组：

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

完成账户配置后重新加载并启动服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-api.service emby-auto-worker.service
```

再通过本机浏览器或 SSH 隧道打开初始化向导。

## HTTPS 与反向代理

本机或 SSH 隧道访问可以直接使用应用入口。通过局域网或公网访问时，由用户选择 Caddy、Nginx、Traefik 或其它反向代理，配置域名和 TLS 证书。代理上游为 `http://127.0.0.1:18080`，自定义端口时相应调整。

反向代理应满足：

- 将网页和 `/api/` 请求转发到同一应用入口，并传递原始 Host 和协议。
- `/api/v1/events` 使用长连接 SSE，并关闭响应缓冲。
- 媒体预览请求透传 `Range` 和 `If-Range`，支持流式响应。

发布包中的 `config/nginx.conf` 是可选回环代理示例，监听 `127.0.0.1:18079`。使用 Nginx 时可按自己的站点和证书配置调整；使用其它代理时按对应产品配置。

HTTPS 可用后，将 `/etc/emby-auto/runtime.env` 的 `SESSION_COOKIE_SECURE` 设为 `true`，并重启 API：

```bash
sudo systemctl restart emby-auto-api.service
```

保持应用回环监听，由反向代理对外提供服务。确认会话 Cookie 包含 `HttpOnly`、`SameSite=Strict` 和 `Secure`。

## 检查与备份

```bash
systemctl status emby-auto-api emby-auto-worker --no-pager
journalctl -u emby-auto-api -u emby-auto-worker -f
curl --fail http://127.0.0.1:18080/api/v1/setup/status
curl --fail http://127.0.0.1:18080/api/v1/health/ready
```

初始化完成后，API 就绪接口应通过。应用服务账户可使用运行环境执行 `emby-auto-worker --check`，验证 Worker 配置和数据库连接。

备份范围包括 PostgreSQL 一致性备份、`/etc/emby-auto`、`/var/lib/emby-auto`，以及按既有存储策略备份的媒体和工作目录。`bootstrap.json` 与 `bootstrap.json.complete` 必须一起保留，包含的加密密钥用于读取数据库中保存的外部服务凭据。备份应加密保存并限制访问权限。

## 升级与已有部署迁移

升级前备份数据库和应用配置，并确认后台工作已暂停、下载和转码已结束或已按原部署机制暂停。下载并校验新发布包，将其放入新的版本目录。

```bash
new_release_id="<installed-release-id>"
sudo systemctl stop emby-auto-worker.service emby-auto-api.service
sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service /etc/systemd/system/emby-auto-worker.service
sudo systemctl daemon-reload
sudo systemctl start emby-auto-api.service
curl --fail http://127.0.0.1:18080/api/v1/health/ready
sudo systemctl start emby-auto-worker.service
```

保留运行环境、初始化文件和账户 drop-in；自定义端口时调整检查命令。数据库迁移失败时保持 Worker 停止，检查 API 日志。是否可以切回旧版本取决于数据库迁移兼容性。

从旧版自动创建数据库和 Nginx 配置的安装器迁移时，现有数据库、数据库服务和 Web 入口继续由用户管理。新安装器会拒绝旧版安装记录，按手动部署步骤准备新应用版本并保留原配置。可以在 API 运行环境中设置 `WEB_ROOT` 来启用应用网页入口；需要调整监听端口时同步更新反向代理上游。

从 Docker 迁移时，保留应用专用数据库、原加密密钥及初始化文件，设置服务账户的文件权限，并将容器专用地址转换为宿主可访问地址。迁移期间保持单一 Worker，完成数据库、网页和媒体流程验证后再移除旧资源。
