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
| `--share-group 共享组` | 与 qBittorrent 等账户共享下载目录的现有用户组 |
| `--port 端口` | 网页与 API 共用的回环端口，默认 `18080` |
| `--archive 文件`、`--checksums 文件` | 校验并使用已下载的发布包与 `SHA256SUMS` |
| `--no-start` | 准备应用和服务，保持服务状态，供迁移使用 |
| `--finalize` | 核验准备后的服务并完成安装登记，保持配置和服务状态 |

端口被占用时，例如使用 `--port 19080`，访问地址和 SSH 隧道也应改用该端口。重复执行保留当前版本、初始化状态和运行配置。更换版本按下文升级步骤操作。

### 账户与目录权限

一键安装默认创建 `emby-auto` 账户；已有媒体库时使用 `--user 媒体账户`。新安装的目录使用以下权限：

| 目录 | 权限 | 用途与要求 |
| --- | --- | --- |
| `/srv/emby-auto/downloads` | `2770`，setgid；使用 `--share-group` 时另有默认 ACL | qBittorrent 写入，应用读取并删除缓存 |
| `/srv/emby-auto/work`、`staging` | `2770`，setgid | 应用处理与入库暂存 |
| `/srv/emby-auto/media/{anime,movies}` | `2775` | 应用写入；Emby 以其它账户读取与遍历 |

qBittorrent、Emby 与应用通常由不同账户运行。推荐在安装时指定一个共享组，并让 qBittorrent 账户加入该组（组需提前创建，例如 `sudo groupadd emby-share`）：

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | \
  sudo bash -s -- --share-group emby-share
sudo usermod -aG emby-share qbittorrent
sudo systemctl restart qbittorrent
```

已有安装未使用共享组时，重复执行安装命令并添加 `--share-group` 会实际应用组成员与目录权限，并写入安装记录。已有媒体库使用 `--user 媒体账户 --share-group 共享组` 时，媒体账户负责媒体文件所有权，共享组负责下载目录协作；账户 UID 和 GID 仍必须大于 0。

也可以不重跑安装器，手工补充权限（示例使用应用账户同名组 `emby-auto`）：

```bash
sudo apt-get install -y acl
sudo usermod -aG emby-auto qbittorrent
sudo chgrp emby-auto /srv/emby-auto/downloads
sudo chmod 2770 /srv/emby-auto/downloads
sudo setfacl -m g:emby-auto:rwx,d:g:emby-auto:rwx /srv/emby-auto/downloads
sudo systemctl restart qbittorrent
```

默认 ACL 让 qBittorrent 使用常见 `umask 022` 创建的子目录仍保留组写权限；只设置共享组而缺少默认 ACL 或 `umask 002` 时，应用仍无法删除这些子目录。Emby 只需要读取和遍历；已有媒体库权限更严格时，可单独为 Emby 账户添加只读 ACL。不要把整个媒体目录改成 `0777`。

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
| 后台控制 | `emby-auto-systemd-control.service`、`/etc/emby-auto/host-control.env` |

API 与 Worker 是独立进程。一键安装会配置后台控制服务，Dashboard 可暂停和恢复后台处理。暂停会停止 Worker，并为应用管理的活动下载保留恢复标记；恢复后再启动 Worker。数据库、反向代理、qBittorrent 和 Emby 的生命周期由用户管理。

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
sudo install -d -o emby -g emby -m 2770 \
  /srv/emby-auto/downloads \
  /srv/emby-auto/work \
  /srv/emby-auto/staging
sudo install -d -o emby -g emby -m 2775 \
  /srv/emby-auto/media/anime \
  /srv/emby-auto/media/movies
```

qBittorrent 使用其它账户时，按上文“账户与目录权限”为该账户配置共享组与默认 ACL。媒体库根的 `2775` 允许其它账户的 Emby 遍历和读取，应用仍以所有者身份写入。入库后的标题子树由应用设为目录 `0755`、文件 `0644`，独立 Emby 账户保持只读访问。

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

配置后台控制服务允许使用的应用 UID 和 GID，并安装三个 unit：

```bash
sudo install -o root -g root -m 0600 \
  /opt/emby-auto/current/config/host-control.env.example /etc/emby-auto/host-control.env
printf 'EMBY_AUTO_SERVICE_UID=%s\nEMBY_AUTO_SERVICE_GID=%s\n' "$(id -u emby)" "$(id -g emby)" | \
  sudo tee /etc/emby-auto/host-control.env >/dev/null
for service in api worker systemd-control; do
  sudo install -o root -g root -m 0644 \
    "/opt/emby-auto/current/config/emby-auto-${service}.service" \
    "/etc/systemd/system/emby-auto-${service}.service"
done
```

后台控制服务以 root 运行，只提供固定的 Worker 启停和主机状态查询。应用版本目录及其父目录须由 root 管理，禁止组和其他用户写入。

默认 unit 使用 `emby:emby`。使用其它服务账户时，为两个服务分别创建持久化 drop-in，并将运行环境文件的 group 调整为该账户所属组：

```bash
sudo systemctl edit emby-auto-api.service
sudo systemctl edit emby-auto-worker.service
```

同时将 `/etc/emby-auto/host-control.env` 的 UID 和 GID 改为该账户的实际值。两个应用 drop-in 均写入：

```ini
[Service]
User=<media-user>
Group=<media-group>
```

完成账户配置后重新加载并启动服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-systemd-control.service emby-auto-api.service emby-auto-worker.service
```

以上启动步骤适用于全新部署。迁移时先保持 Worker 停止，按下文迁移流程保留初始化状态。全新部署再通过本机浏览器或 SSH 隧道打开初始化向导。

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
systemctl status emby-auto-api emby-auto-worker emby-auto-systemd-control --no-pager
journalctl -u emby-auto-api -u emby-auto-worker -u emby-auto-systemd-control -f
curl --fail http://127.0.0.1:18080/api/v1/setup/status
curl --fail http://127.0.0.1:18080/api/v1/health/ready
```

初始化完成后，API 就绪接口应通过。应用服务账户可使用运行环境执行 `emby-auto-worker --check`，验证 Worker 配置和数据库连接。维护命令可用以下 Bash 函数加载运行环境：

```bash
emby_auto_maintenance() {
  sudo bash -c 'set -a; source /etc/emby-auto/runtime.env; set +a; command_name=$1; shift; exec "/opt/emby-auto/current/bin/$command_name" "$@"' -- "$@"
}
emby_auto_maintenance emby-auto-background-runtime --state status
emby_auto_maintenance emby-auto-release-preflight --require-idle
```

后台状态的 `verified=true` 表示 Worker 和下载状态通过核验。`transfers.active` 是实际活动 torrent 数量；预检中的 `downloads.active` 是业务记录数量，暂停后仍可保留。状态输出包括计数、下载摘要和进度。

备份范围包括 PostgreSQL 一致性备份、`/etc/emby-auto`、`/var/lib/emby-auto`，以及按既有存储策略备份的媒体和工作目录。`bootstrap.json` 与 `bootstrap.json.complete` 必须一起保留，包含的加密密钥用于读取数据库中保存的外部服务凭据。备份应加密保存并限制访问权限。

## 升级与已有部署迁移

升级前备份数据库和应用配置。以下命令使用本文档前文的 `emby_auto_maintenance` 函数；在新终端执行时，先重新定义该函数。

### 1. 准备并校验新版本目录

下载、校验并放入新的版本目录；已有 `current` 保持不变：

```bash
release_tag="v0.2.0"
target_arch="amd64" # arm64 设备改为 arm64
new_release_id="${release_tag}-$(date -u +%Y%m%d%H%M%S)"
work_dir="$(mktemp -d)"
archive="emby-auto-direct-${target_arch}-linux-${target_arch}.tar.gz"
curl -fL "https://github.com/onprs/Emby-Auto/releases/download/${release_tag}/${archive}" -o "${work_dir}/${archive}"
curl -fL "https://github.com/onprs/Emby-Auto/releases/download/${release_tag}/SHA256SUMS" -o "${work_dir}/SHA256SUMS"
(cd "${work_dir}" && awk -v name="${archive}" '$2 == name' SHA256SUMS | sha256sum --check --strict -)
tar -xzf "${work_dir}/${archive}" -C "${work_dir}"
(cd "${work_dir}/emby-auto-direct-${target_arch}" && sha256sum --check --strict SHA256SUMS)
sudo install -d -o root -g root -m 0755 "/opt/emby-auto/releases/${new_release_id}"
sudo cp -a "${work_dir}/emby-auto-direct-${target_arch}/." "/opt/emby-auto/releases/${new_release_id}/"
sudo chown -R root:root "/opt/emby-auto/releases/${new_release_id}"
rm -rf "${work_dir}"
```

切换前确认目标目录与制品完整：

```bash
release_path="/opt/emby-auto/releases/${new_release_id}"
[[ -d "${release_path}" ]] || { echo "版本目录不存在：${release_path}" >&2; exit 1; }
(cd "${release_path}" && sha256sum --check --strict SHA256SUMS)
for binary in api worker migration-check host-control background-runtime release-preflight \
  recover-stale-operations migrate-runtime-addresses migrate-staging-ownership; do
  [[ -x "${release_path}/bin/emby-auto-${binary}" ]] || { echo "缺少 ${binary}" >&2; exit 1; }
done
```

### 2. 暂停后台并核对

使用维护函数暂停后台：

```bash
emby_auto_maintenance emby-auto-background-runtime --state stopped
emby_auto_maintenance emby-auto-release-preflight --require-idle
```

确认后台输出为 `state=stopped`、`verified=true`、`transfers.active=0`，并核对预检中没有运行中的操作、长任务或 stale operation。若存在中断的 attempt，保持 Worker 停止，等待至少五分钟，再先预览恢复候选：

```bash
emby_auto_maintenance emby-auto-recover-stale-operations --worker-stopped --stale-after 5m
```

核对候选范围后使用相同命令增加 `--apply`，重新检查运行态。

### 3. 切换、验证与回滚

```bash
(
  set -eE
  release_path="/opt/emby-auto/releases/${new_release_id}"
  previous_release_id="$(basename "$(readlink -f /opt/emby-auto/current)")"
  [[ -d "${release_path}" ]] || { echo "版本目录不存在：${release_path}" >&2; exit 1; }
  (cd "${release_path}" && sha256sum --check --strict SHA256SUMS)
  for service in api worker systemd-control; do
    [[ -s "${release_path}/config/emby-auto-${service}.service" ]] \
      || { echo "新版本缺少 emby-auto-${service}.service" >&2; exit 1; }
  done
  install_units() {
    local service
    for service in api worker systemd-control; do
      sudo install -o root -g root -m 0644 \
        "/opt/emby-auto/current/config/emby-auto-${service}.service" \
        "/etc/systemd/system/emby-auto-${service}.service"
    done
    sudo systemctl daemon-reload
  }
  rollback_release() {
    echo '切换或验证失败，回滚到上一版本。' >&2
    sudo ln -sfn "releases/${previous_release_id}" /opt/emby-auto/current.next
    sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
    install_units
    sudo systemctl start emby-auto-systemd-control.service emby-auto-api.service
    if ! curl --fail --max-time 10 http://127.0.0.1:18080/api/v1/health/ready; then
      echo '回滚后旧版本也未通过就绪检查；请运行 journalctl -u emby-auto-api -u emby-auto-worker 排查，不要重复执行切换。' >&2
      exit 2
    fi
    exit 1
  }
  sudo systemctl stop emby-auto-api.service emby-auto-systemd-control.service
  # 切换 current 之后到恢复后台之前的任何失败都执行回滚：set -eE 使函数内的
  # 错误也触发 ERR trap，覆盖安装 unit、daemon-reload、启动与就绪检查。
  rollback_on_error() { rollback_release; }
  trap rollback_on_error ERR
  sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
  sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
  install_units
  sudo systemctl start emby-auto-systemd-control.service emby-auto-api.service
  curl --fail --max-time 10 http://127.0.0.1:18080/api/v1/health/ready
  emby_auto_maintenance emby-auto-migration-check
  emby_auto_maintenance emby-auto-background-runtime --state running
  trap - ERR
)
```

切换后（包括安装 unit、重新加载、启动与就绪检查）的任何失败都会自动把 `current` 指回上一版本、重装旧 unit 并重启旧 API；回滚后仍不健康时输出明确提示并退出。Worker 在整个过程中保持停止。已经执行新版本数据库迁移时，旧版本是否可运行取决于迁移兼容性；必要时恢复升级前的数据库备份。

升级成功后同步安装记录中的版本，避免后续安装器把实际运行的版本当成旧记录（将 `v0.2.0` 换成步骤 1 的 `release_tag`）：

```bash
if [[ -f /etc/emby-auto/installer.env ]]; then
  sudo sed -i 's/^version=.*/version=v0.2.0/' /etc/emby-auto/installer.env
fi
```

保留运行环境、初始化文件、控制服务的账户绑定和应用账户 drop-in；自定义端口时调整检查命令。恢复后台后确认 `verified=true`、`transfers.runtimeMarks=0`，并核验原有任务。恢复失败或结果不明时，先只读查询状态，再执行正式 `stopped` 转换并核对门禁。数据库迁移失败时保持 Worker 停止。

安装器会拒绝版本不兼容的安装记录。旧部署按手动部署步骤准备新版本，保留原有数据库、运行配置和初始化文件，并配置后台控制服务。旧安装器创建的 PostgreSQL 和 Nginx 继续由用户管理。

### 从 Docker 迁移

1. 通过原部署的控制方式停止旧 Worker，确认转码已退出，暂停应用管理的 torrent 并保留恢复标记。保持旧服务停止，避免两个 Worker 同时处理任务。
2. 保存应用数据库的一致性备份、原初始化文件及完成标记、运行配置和旧部署定义。保留原加密密钥、管理员和媒体绝对路径；核对发布包包含当前数据库所需的迁移。
3. 使用 `--no-start` 准备原生应用，保持新 Worker 停止。此命令在包含安装器、对应架构发布包与校验文件的目录执行；示例使用本页的 `emby` 账户，端口冲突时增加 `--port`：

```bash
sudo bash install.sh --no-start --user emby \
  --archive ./emby-auto-direct-amd64-linux-amd64.tar.gz \
  --checksums ./SHA256SUMS
```

4. 将原 `bootstrap.json` 与 `bootstrap.json.complete` 一起复制到原生状态目录，设为服务账户所有、权限 `0600`。配置宿主可访问的数据库连接，并核对外部服务地址和各级目录权限。迁移期间保持配置只有一个写入者。
5. 启动控制服务和 API，验证数据库、网页及 HTTPS；需要数据库迁移时先验证迁移兼容性。通过正式后台恢复命令启动新 Worker，确认下载、字幕、转码、入库和清理行为后保留旧资源作为回滚来源。
6. 服务通过检查后运行 `sudo bash install.sh --finalize` 完成安装登记。此操作核验制品与服务，保留运行配置和服务状态。

发布包提供两个默认预览的迁移工具。`emby-auto-migrate-runtime-addresses` 仅将 qBittorrent 与 Emby 地址中的指定主机替换为回环 IP；`emby-auto-migrate-staging-ownership` 仅处理数据库任务所属暂存树中的 root 所有目录，保留 group、mode 和文件内容。先预览并核对范围，再指定私有备份路径和 `--apply`：

```bash
sudo install -d -o root -g root -m 0700 /var/backups/emby-auto-migration
emby_auto_maintenance emby-auto-migrate-runtime-addresses --container-host-address 127.0.0.1
emby_auto_maintenance emby-auto-migrate-staging-ownership --uid "$(id -u emby)" --container-host-address 127.0.0.1
```

地址工具默认匹配 `host.docker.internal`，其它容器主机名使用 `--from-host` 指定，并单独核对 Agent、代理等配置。`--container-host-address` 仅为迁移命令临时解析该 qBittorrent 地址；数据库连接须已配置为宿主可访问地址。应用变更时使用 `--backup` 保存恢复点，可用 `--expected-changes` 或 `--expected-tasks` 约束预览数量；回滚使用 `--restore` 指定快照并先预览，再加 `--apply`。快照目录须由执行账户所有、权限 `0700`，快照文件权限为 `0600`。这些命令要求 Worker 已停止、应用 torrent 已暂停且下载引用可核实。

## 卸载

默认只停用并移除应用服务与程序，保留数据库、配置和媒体数据：

```bash
sudo systemctl disable --now emby-auto-worker.service emby-auto-api.service emby-auto-systemd-control.service
sudo rm -f /etc/systemd/system/emby-auto-api.service /etc/systemd/system/emby-auto-worker.service /etc/systemd/system/emby-auto-systemd-control.service
sudo rm -rf /etc/systemd/system/emby-auto-api.service.d /etc/systemd/system/emby-auto-worker.service.d
sudo systemctl daemon-reload
sudo rm -rf /run/emby-auto-systemd
```

保留范围包括 `/opt/emby-auto`、`/etc/emby-auto`、`/var/lib/emby-auto`、`/srv/emby-auto`、应用账户和 PostgreSQL 数据库。需要重新启用时，按手动部署步骤安装相同或更新版本，保留原配置与初始化文件。

确认不再需要数据后，再单独执行销毁操作（不可恢复）。媒体目录可能包含用户文件，删除前自行确认：

```bash
sudo rm -rf /opt/emby-auto /etc/emby-auto /var/lib/emby-auto
# 仅在确认媒体与下载数据不再需要时执行
sudo rm -rf /srv/emby-auto
# 仅删除安装器默认创建的账户；--user 指定的媒体所有者不要删除
sudo userdel emby-auto
```

PostgreSQL 数据库与角色由部署者按数据库策略删除。`/run/emby-auto-systemd` 中的 socket 会在控制服务下次启动时重建。

## 从备份恢复

恢复顺序固定为数据库、应用配置、初始化文件、服务。只使用同一套备份中的数据库与 `bootstrap.json`，否则加密密钥和管理员记录会不一致。

1. 停止应用服务，保持 Worker 停止：

```bash
sudo systemctl stop emby-auto-worker.service emby-auto-api.service
```

2. 恢复 PostgreSQL。示例使用自定义格式备份；明文 SQL 备份改用 `psql -f`：

```bash
pg_restore --clean --if-exists --no-owner \
  --host <数据库主机> --username <数据库账户> --dbname <数据库名> \
  /path/to/emby_auto.dump
```

3. 恢复 `/etc/emby-auto` 并修正权限：

```bash
sudo install -d -o root -g root -m 0755 /etc/emby-auto
sudo cp -a /path/to/backup/etc-emby-auto/. /etc/emby-auto/
sudo chown root:root /etc/emby-auto/installer.env /etc/emby-auto/host-control.env
sudo chmod 0600 /etc/emby-auto/installer.env /etc/emby-auto/host-control.env
sudo chown root:<应用组> /etc/emby-auto/runtime.env
sudo chmod 0640 /etc/emby-auto/runtime.env
```

4. 成对恢复初始化文件。两个文件必须来自同一备份，由应用账户所有，不要手工编辑 `.complete`：

```bash
sudo install -d -o <应用账户> -g <应用组> -m 0700 /var/lib/emby-auto
sudo install -o <应用账户> -g <应用组> -m 0600 \
  /path/to/backup/bootstrap.json /var/lib/emby-auto/bootstrap.json
sudo install -o <应用账户> -g <应用组> -m 0600 \
  /path/to/backup/bootstrap.json.complete /var/lib/emby-auto/bootstrap.json.complete
```

5. 按手动部署步骤 3 安装版本目录并把 `current` 指向它（跳过重写已恢复的 `runtime.env`），再启动服务并核对：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-systemd-control.service emby-auto-api.service
curl --fail http://127.0.0.1:18080/api/v1/setup/status
curl --fail http://127.0.0.1:18080/api/v1/health/ready
```

`setup/status` 应为 `state=completed`；若为 `unavailable`，检查 `bootstrap.json` 与 `.complete` 是否成对、文件权限和数据库连接。登录确认管理员与外部服务配置可用后，再按正式后台流程恢复 Worker，并核对任务与下载状态。
