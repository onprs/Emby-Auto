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

已有安装未使用共享组时，重复执行安装命令并添加 `--share-group` 会实际应用组成员与目录权限，并写入安装记录。使用 `--share-group` 需要 `setfacl`；全新安装时安装器会在 Debian/Ubuntu 上尝试安装 `acl`，已有安装重跑或离线环境请先执行 `sudo apt-get install -y acl`。已有媒体库使用 `--user 媒体账户 --share-group 共享组` 时，媒体账户负责媒体文件所有权，共享组负责下载目录协作；账户 UID 和 GID 仍必须大于 0。

也可以不重跑安装器，手工补充权限（示例使用应用账户同名组 `emby-auto`）：

```bash
sudo apt-get install -y acl
sudo usermod -aG emby-auto qbittorrent
sudo chgrp emby-auto /srv/emby-auto/downloads
sudo chmod 2770 /srv/emby-auto/downloads
sudo setfacl -m g:emby-auto:rwx,d:g:emby-auto:rwx /srv/emby-auto/downloads

# 安装器只调整 downloads、work、staging 顶层目录；下载缓存中已有的子目录与文件
# 按下面的有界命令补齐。命令仅处理本应用的下载目录，不跟随符号链接。
sudo find /srv/emby-auto/downloads -type d -exec chgrp emby-auto {} + \
  -exec chmod g+rwx,g+s {} + \
  -exec setfacl -m g:emby-auto:rwx,d:g:emby-auto:rwx {} +
sudo find /srv/emby-auto/downloads -type f -exec chgrp emby-auto {} + \
  -exec chmod g+rw {} +
sudo systemctl restart qbittorrent
```

默认 ACL 让 qBittorrent 之后使用常见 `umask 022` 创建的子目录仍保留组写权限；已有安装的下载缓存子树不会因顶层默认 ACL 自动修复，需要执行上面的有界补齐命令。只设置共享组而缺少默认 ACL 或 `umask 002` 时，应用仍无法删除这些子目录。Emby 只需要读取和遍历；已有媒体库权限更严格时，可单独为 Emby 账户添加只读 ACL。不要把整个媒体目录改成 `0777`。

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

升级前备份数据库和应用配置。步骤 1 与 3 需在同一 shell 会话执行；在新终端执行步骤 3 时，先设置 `new_release_id` 并重新定义该函数。

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

代码块默认等待 600 秒；迁移耗时更长时，先设置 `EMBY_AUTO_UPGRADE_READY_TIMEOUT`（秒）。自定义端口时，同时设置 `EMBY_AUTO_UPGRADE_READY_URL`。

```bash
(
  set -eE
  : "${new_release_id:?请先执行步骤 1，或在新终端设置 new_release_id}"
  ready_timeout="${EMBY_AUTO_UPGRADE_READY_TIMEOUT:-600}"
  ready_url="${EMBY_AUTO_UPGRADE_READY_URL:-http://127.0.0.1:18080/api/v1/health/ready}"
  [[ "${ready_timeout}" =~ ^[1-9][0-9]*$ ]] || {
    echo 'EMBY_AUTO_UPGRADE_READY_TIMEOUT 必须是正整数秒数。' >&2
    exit 1
  }
  if ! declare -F emby_auto_maintenance >/dev/null; then
    echo '未找到 emby_auto_maintenance；请先按上文定义维护函数。' >&2
    exit 1
  fi
  release_path="/opt/emby-auto/releases/${new_release_id}"
  previous_release_id="$(basename "$(readlink -f /opt/emby-auto/current)")"
  [[ -d "${release_path}" ]] || { echo "版本目录不存在：${release_path}" >&2; exit 1; }
  [[ -n "${previous_release_id}" && -d "/opt/emby-auto/releases/${previous_release_id}" ]] \
    || { echo '无法确定上一版本目录，未停止服务。' >&2; exit 1; }
  (cd "${release_path}" && sha256sum --check --strict SHA256SUMS)
  for service in api worker systemd-control; do
    [[ -s "${release_path}/config/emby-auto-${service}.service" ]] \
      || { echo "新版本缺少 emby-auto-${service}.service" >&2; exit 1; }
  done
  worker_state="$(systemctl show emby-auto-worker.service --property=ActiveState --value 2>/dev/null || true)"
  case "${worker_state}" in
    active|activating|reloading|deactivating)
      echo 'Worker 仍在运行；请先完成步骤 2 的正式 stopped 转换与门禁核验。' >&2
      exit 1
      ;;
  esac
  install_units() {
    local service
    for service in api worker systemd-control; do
      sudo install -o root -g root -m 0644 \
        "/opt/emby-auto/current/config/emby-auto-${service}.service" \
        "/etc/systemd/system/emby-auto-${service}.service" || return 1
    done
    sudo systemctl daemon-reload || return 1
  }
  running_release() {
    local unit=$1 pid exe
    pid="$(systemctl show "${unit}" --property=MainPID --value 2>/dev/null)" || return 1
    [[ "${pid}" =~ ^[1-9][0-9]*$ ]] || return 1
    exe="$(readlink -f "/proc/${pid}/exe" 2>/dev/null)" || return 1
    [[ "${exe}" == /opt/emby-auto/releases/*/bin/* ]] || return 1
    exe="${exe#/opt/emby-auto/releases/}"
    printf '%s\n' "${exe%%/*}"
  }
  wait_ready() {
    local expected=$1 deadline=$((SECONDS + ready_timeout)) remaining request_max
    local state code api_release control_release
    while (( SECONDS < deadline )); do
      state="$(systemctl show emby-auto-api.service --property=ActiveState --value 2>/dev/null || true)"
      if [[ "${state}" == failed || "${state}" == inactive ]]; then
        echo "API 单元在就绪前进入 ${state}。" >&2
        return 1
      fi
      state="$(systemctl show emby-auto-systemd-control.service --property=ActiveState --value 2>/dev/null || true)"
      if [[ "${state}" == failed || "${state}" == inactive ]]; then
        echo "控制服务在就绪前进入 ${state}。" >&2
        return 1
      fi
      api_release="$(running_release emby-auto-api.service || true)"
      control_release="$(running_release emby-auto-systemd-control.service || true)"
      if [[ "${api_release}" == "${expected}" && "${control_release}" == "${expected}" ]]; then
        remaining=$((deadline - SECONDS))
        (( remaining > 0 )) || break
        request_max=$(( remaining < 25 ? remaining : 25 ))
        code="$(curl --silent --show-error --noproxy '*' \
          --connect-timeout 2 --max-time "${request_max}" \
          --output /dev/null --write-out '%{http_code}' "${ready_url}" 2>/dev/null || true)"
        if [[ "${code}" == 200 ]]; then
          echo "版本 ${expected} 已就绪。"
          return 0
        fi
        echo "等待版本 ${expected} 就绪（HTTP ${code:-连接失败}）。" >&2
      else
        echo "等待版本 ${expected} 启动（当前 API=${api_release:-无}，控制=${control_release:-无}）。" >&2
      fi
      sleep 2
    done
    echo "等待版本 ${expected} 就绪超过 ${ready_timeout} 秒；可按实际迁移耗时调大 EMBY_AUTO_UPGRADE_READY_TIMEOUT。" >&2
    return 1
  }
  rollback_failed() {
    local reason=$1
    echo "回滚失败：${reason}" >&2
    if ! sudo systemctl stop emby-auto-api.service emby-auto-systemd-control.service >/dev/null 2>&1; then
      echo '无法停止 API 或控制服务；请立即执行 sudo systemctl stop emby-auto-api.service emby-auto-systemd-control.service。' >&2
    fi
    echo '服务已停止或已尝试停止，Worker 保持停止。不要重复执行切换；请检查 journalctl -u emby-auto-api -u emby-auto-systemd-control。' >&2
    echo '若失败发生在恢复 current 之前，current 仍指向新版本；不要把它当作已回滚。' >&2
    echo '若新版本已执行数据库迁移，旧二进制可能无法读取新 schema；不要自动恢复或删除数据库，请前滚修复或按升级前的一致性备份走正式恢复流程。' >&2
    exit 2
  }
  rollback_release() {
    trap - ERR
    set +e
    local worker_state
    echo '切换或验证失败，回滚到上一版本。' >&2
    worker_state="$(systemctl show emby-auto-worker.service --property=ActiveState --value 2>/dev/null || true)"
    case "${worker_state}" in
      active|activating|reloading|deactivating)
        echo 'Worker 正在运行；回滚前必须先执行正式 stopped 转换并核对门禁。' >&2
        exit 2
        ;;
    esac
    sudo systemctl stop emby-auto-api.service emby-auto-systemd-control.service || \
      rollback_failed '无法停止新版本 API 或控制服务。'
    sudo ln -sfn "releases/${previous_release_id}" /opt/emby-auto/current.next || \
      rollback_failed '无法写入上一版本 current 链接。'
    sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current || \
      rollback_failed '无法切换 current 到上一版本。'
    install_units || rollback_failed '无法安装上一版本 unit 或重新加载 systemd。'
    sudo systemctl start emby-auto-systemd-control.service emby-auto-api.service || \
      rollback_failed '无法启动上一版本 API 或控制服务。'
    wait_ready "${previous_release_id}" || rollback_failed '上一版本未在预算内就绪。'
    echo '已回滚到上一版本并通过就绪检查；Worker 保持停止。' >&2
    exit 1
  }
  rollback_on_error() { rollback_release; }
  sudo systemctl stop emby-auto-api.service emby-auto-systemd-control.service || {
    echo '无法停止当前 API 或控制服务，尚未切换 current。' >&2
    exit 1
  }
  # 切换 current 之后到后台恢复之前的任何失败都执行回滚：set -eE 使函数内的
  # 错误也触发 ERR trap，覆盖安装 unit、daemon-reload、启动、就绪与迁移检查。
  trap rollback_on_error ERR
  sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
  sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
  install_units
  sudo systemctl start emby-auto-systemd-control.service emby-auto-api.service
  wait_ready "${new_release_id}"
  emby_auto_maintenance emby-auto-migration-check
  trap - ERR
  echo "已切换到 ${new_release_id} 并通过就绪与迁移检查；后台仍保持停止。"
)
```

退出码：`0` 表示切换与验证成功；`1` 表示已回滚到上一版本且旧版本通过独立的就绪与运行版本检查；`2` 表示回滚失败或被门禁阻断，需要先核实实际状态。普通回滚失败会尝试停止 API 与控制服务并在输出中说明结果，但尝试停止不等于已停止。因 Worker 正在运行而被门禁阻断时不会执行回滚：`current` 可能仍指向新版本，API、控制服务与 Worker 也可能仍在运行，输出会明确提示。此时先查询实际状态，执行正式 `stopped` 转换并核对门禁（`state=stopped`、`verified=true`、`transfers.active=0`），再决定前滚修复或按升级前备份恢复；不要根据退出码 2 假定服务已停止或已回滚。就绪等待只在新版本或旧版本自身超时、或单元进入 `failed`/`inactive` 时结束；连接未建立和 HTTP 503 会继续等待。单次请求不超过 25 秒，默认总预算 600 秒。应用没有单独的启动超时：`DATABASE_CONNECT_TIMEOUT` 只限制单次数据库连接，`SHUTDOWN_TIMEOUT` 只用于停止；后台恢复命令的 `--timeout`（默认 3m，最大 10m）与就绪预算相互独立。迁移耗时更长时必须调大 `EMBY_AUTO_UPGRADE_READY_TIMEOUT`。

自动回滚只恢复代码与 unit，不恢复数据库。新版本一旦完成 schema 迁移，旧二进制可能无法读取新 schema；此时回滚会以退出码 2 停止并提示前滚或按升级前一致性备份走正式恢复。数据库恢复与代码回滚分开执行，不要自动 drop/restore 数据库或删除状态。

升级成功后同步安装记录中的版本，避免后续安装器把实际运行的版本当成旧记录（将 `v0.2.0` 换成步骤 1 的 `release_tag`）：

```bash
if [[ -f /etc/emby-auto/installer.env ]]; then
  sudo sed -i 's/^version=.*/version=v0.2.0/' /etc/emby-auto/installer.env
fi
```

切换与 API 验证期间 Worker 保持停止。确认新版本后，再单独执行正式后台恢复：

```bash
emby_auto_maintenance emby-auto-background-runtime --state running
```

该命令会先取消应用 torrent 的暂停标记，再启动 Worker；任一步失败或结果不明时，可能已有 torrent 或 Worker 部分恢复。此时先只读查询，再执行正式 `stopped` 转换并核对门禁：

```bash
emby_auto_maintenance emby-auto-background-runtime --state status
emby_auto_maintenance emby-auto-background-runtime --state stopped
emby_auto_maintenance emby-auto-release-preflight --require-idle
```

确认 `state=stopped`、`verified=true`、`transfers.active=0` 后，再决定前滚修复或按升级前备份回滚；不要直接 `systemctl start emby-auto-worker.service` 代替正式流程，也不要在未核实状态时直接切回旧版本。恢复成功后确认 `verified=true`、`transfers.runtimeMarks=0`，并核验原有任务。数据库迁移失败时保持 Worker 停止。

保留运行环境、初始化文件、控制服务的账户绑定和应用账户 drop-in；自定义端口时调整 `EMBY_AUTO_UPGRADE_READY_URL`。

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

默认只停用并移除应用服务定义（unit），保留程序、数据库、配置和媒体数据：

```bash
sudo systemctl disable --now emby-auto-worker.service emby-auto-api.service emby-auto-systemd-control.service
sudo rm -f /etc/systemd/system/emby-auto-api.service /etc/systemd/system/emby-auto-worker.service /etc/systemd/system/emby-auto-systemd-control.service
sudo rm -rf /etc/systemd/system/emby-auto-api.service.d /etc/systemd/system/emby-auto-worker.service.d
sudo systemctl daemon-reload
sudo rm -rf /run/emby-auto-systemd
```

保留范围包括 `/opt/emby-auto`、`/etc/emby-auto`、`/var/lib/emby-auto`、`/srv/emby-auto`、应用账户和 PostgreSQL 数据库。unit 文件和 `emby-auto-{api,worker}.service.d` 中的 User/Group drop-in 已被删除，重新启用时必须重建。

不要靠重跑安装器自动恢复：`/etc/emby-auto/installed` 仍在，安装器的已安装分支只核验现有服务；unit 已删除时会检查失败，`--no-start` 只返回成功，不会重建 unit。也不要用 `runtime.env.example` 覆盖已保留的 `/etc/emby-auto/runtime.env`。

重新启用按以下顺序执行：

1. 从保留的当前版本重装 unit：

```bash
for service in api worker systemd-control; do
  sudo install -o root -g root -m 0644 \
    "/opt/emby-auto/current/config/emby-auto-${service}.service" \
    "/etc/systemd/system/emby-auto-${service}.service"
done
sudo systemctl daemon-reload
```

2. 重建两个 User/Group drop-in。安装器部署均需要，默认 `emby-auto` 账户也通过 drop-in 覆盖 unit 中的 `emby`。手动部署没有 `installer.env` 时，把 `service_user` 设为实际服务账户：

```bash
service_user="${service_user:-$(sudo sed -n 's/^service_user=//p' /etc/emby-auto/installer.env)}"
[[ -n "$service_user" ]] || { echo '无法读取安装记录中的 service_user。' >&2; exit 1; }
service_group="$(id -gn "$service_user")"
for service in api worker; do
  sudo install -d -m 0755 "/etc/systemd/system/emby-auto-${service}.service.d"
  printf '[Service]\nUser=%s\nGroup=%s\n' "$service_user" "$service_group" | \
    sudo tee "/etc/systemd/system/emby-auto-${service}.service.d/installer.conf" >/dev/null
done
```

原 drop-in 还包含其它设置时一并恢复；`/etc/emby-auto/host-control.env` 的 `EMBY_AUTO_SERVICE_UID`/`EMBY_AUTO_SERVICE_GID` 必须与该账户一致。

3. 核对保留的运行环境与初始化文件，不要重写：

```bash
for file in /etc/emby-auto/runtime.env /var/lib/emby-auto/bootstrap.json /var/lib/emby-auto/bootstrap.json.complete; do
  sudo test -f "$file" || { echo "缺少 $file；请从备份恢复。" >&2; exit 1; }
done
sudo chown "root:$(id -gn "$service_user")" /etc/emby-auto/runtime.env
sudo chmod 0640 /etc/emby-auto/runtime.env
```

4. 启动控制服务和 API，先不启动 Worker：

```bash
sudo systemctl enable --now emby-auto-systemd-control.service emby-auto-api.service
curl --fail http://127.0.0.1:18080/api/v1/setup/status
curl --fail http://127.0.0.1:18080/api/v1/health/ready
```

5. 按正式后台流程恢复 Worker 与暂停的 torrent，并核对状态：

```bash
emby_auto_maintenance emby-auto-background-runtime --state running
emby_auto_maintenance emby-auto-background-runtime --state status
```

`runtime.env` 或初始化文件缺失时从备份恢复；不要用示例文件重建密钥或管理员记录。`/opt/emby-auto` 已被销毁时按首次安装或从备份恢复处理，不要套用本节的重启用步骤。

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

3. 恢复 `/etc/emby-auto` 并修正权限。手动部署可能没有 `installer.env` 或 `host-control.env`，仅在文件存在时修正：

```bash
sudo install -d -o root -g root -m 0755 /etc/emby-auto
sudo cp -a /path/to/backup/etc-emby-auto/. /etc/emby-auto/
for file in installer.env host-control.env; do
  if [[ -f "/etc/emby-auto/$file" ]]; then
    sudo chown root:root "/etc/emby-auto/$file"
    sudo chmod 0600 "/etc/emby-auto/$file"
  fi
done
if [[ -f /etc/emby-auto/runtime.env ]]; then
  sudo chown root:<应用组> /etc/emby-auto/runtime.env
  sudo chmod 0640 /etc/emby-auto/runtime.env
fi
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

`setup/status` 应为 `state=completed`；若 `curl --fail` 返回非零或 HTTP 503 `service_unavailable`，检查 `bootstrap.json` 与 `.complete` 是否成对、文件权限和数据库连接。该接口的状态值只有 `required`、`initializing`、`completed`，没有 `unavailable`。登录确认管理员与外部服务配置可用后，再按正式后台流程恢复 Worker，并核对任务与下载状态。
