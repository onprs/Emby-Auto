<p align="center">
  <img src="emby-auto-svg.svg" alt="Emby Auto - 自托管媒体自动化平台" width="800">
</p>

# Emby Auto

**简体中文** | [English](README.en.md)

Emby Auto 是面向 Emby 的自托管媒体自动化平台，连接资源检索、RSS 订阅、qBittorrent 下载、TMDb 剧集映射、FFmpeg 媒体处理、人工审核和 Emby 入库，并通过 Web 界面管理任务与运行状态。

## 功能

- 检索动漫剧集和电影资源，持续轮询 RSS/Atom 订阅并自动筛选。
- 管理 qBittorrent 下载、文件选择、进度、取消、重试与缓存清理。
- 通过单集锚点建立源剧集与 TMDb 剧集之间的映射。
- 按转码配置生成视频与独立 ASS 字幕，在入库前提供人工审核。
- 同步 Emby 媒体库目录，集中查看任务、后台操作、失败信息和实时事件。
- 通过首次安装向导设置管理员、外部服务、媒体目录和转码参数。
- 可选 Agent 辅助处理确定性规则无法唯一判断的异常场景。

## 一键部署

无需 Docker、克隆仓库或安装 Go、Node.js。安装器会下载预编译发布包，安装 FFmpeg，并配置应用开机启动。网页与 API 由应用直接提供，反向代理可自行选择。

支持 Debian 12/13、Ubuntu 22.04/24.04/26.04，64 位 `amd64` 或 `arm64`，需要运行 systemd、具备 root 或 sudo 权限，并已安装 Bash 和 curl。设备需要能访问 GitHub 与系统软件源。

在目标设备执行：

~~~bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s --
~~~

安装完成后，在设备上打开 `http://127.0.0.1:18080`。远程设备可在自己的电脑执行以下命令，再打开同一网页地址：

~~~bash
ssh -L 18080:127.0.0.1:18080 用户@设备地址
~~~

提前准备 PostgreSQL 数据库和专用账户。按照网页向导填写数据库连接、管理员、qBittorrent Web UI 地址与凭据、Emby 地址与 API key、TMDb API Read Access Token，并确认媒体目录。

已有媒体库时，使用媒体文件所有者账户安装：

~~~bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s -- --user 媒体账户
~~~

重复执行安装命令会保留已有版本、配置与数据。端口冲突时可添加 `--port 端口号`；更多选项、HTTPS、备份和升级见 [Linux 直接部署](docs/direct-deployment.md)。

## 使用条件

- PostgreSQL、qBittorrent 与 Emby Server 由用户自行安装配置，需要提前准备。
- 部署设备必须能访问下载目录和媒体库；qBittorrent 与应用使用的下载绝对路径需要一致。
- 应用服务账户的 UID 必须与 Emby 媒体文件所有者一致。
- 默认通过本机或 SSH 隧道访问。通过网络对外提供服务时，需要配置 HTTPS，并启用安全会话 Cookie。
- 定期备份数据库、安装配置和媒体目录。

## 其它部署方式

- [Docker Compose](docs/deployment.md)：使用容器部署。
- [Linux 手动部署](docs/direct-deployment.md#手动部署)：自行准备运行依赖并安装发布包。
- [GitHub Releases](https://github.com/onprs/Emby-Auto/releases)：下载 `amd64`、`arm64` 发布包和校验文件。

## 安全与贡献

管理会话使用 HttpOnly、SameSite Cookie，外部服务密钥加密存储。安全问题请按照 [安全策略](SECURITY.md) 私密报告。贡献代码请阅读 [贡献指南](CONTRIBUTING.md) 与 [开发文档](docs/development.md)。

## 第三方服务

Emby Auto 不提供或分发媒体内容，部署者应确保资源获取与处理行为符合适用法律及第三方服务条款。本项目为独立项目，与 Emby 或 qBittorrent 不存在隶属或认可关系。

本项目使用 TMDB API，但未获得 TMDB 的认可或认证。

## 许可证

[MIT License](LICENSE)
