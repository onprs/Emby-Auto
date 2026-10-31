# 开发与发布维护

本文面向源码贡献者和发布维护者。公开用户的安装与使用请阅读 [README](../README.md) 和 [Linux 直接部署](direct-deployment.md)。

## 开发环境

需要 Go 1.27.3+、Node.js 24.15+（24.x）或 26+、npm 11+，以及 PostgreSQL 17 或 Docker Compose。

Windows 开发请在 WSL2 中执行后端和维护命令。部分维护模块依赖 Unix 文件系统接口，原生 Windows 环境不能完成全部 Go 包的构建。

开发工具入口 `scripts/dev/go-tool.sh` 默认使用 `tools/go.mod` 声明的 Go 工具链，首次运行时由 Go 自动获取；显式设置的 `GOTOOLCHAIN` 会保留。应用本身的工具链要求以 `backend/go.mod` 为准。

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

生成目录 `backend/internal/transport/httpapi/api.gen.go`、`backend/db/sqlc` 和 `apps/web/src/api/generated` 由生成器维护。`check:generated` 在临时目录中重新生成并比较，发现缺失、多余或内容不同的文件时失败，保留工作区中的生成文件。

## 代码定位与依赖边界

后端业务服务位于 `backend/internal/service`。读取入口共用 `ReadService`，按资源组织查询和展示组装；获取任务的生命周期推导独立于数据库读取。

| 职责 | 源文件 |
| --- | --- |
| 通用游标与值转换 | `read_service.go` |
| 下载、搜索、获取记录 | `download_read_model.go`、`search_read_model.go`、`acquisition_read_model.go` |
| 获取详情与投影共用的批量数据 | `acquisition_read_data.go` |
| 获取任务阶段、聚合状态与进度 | `acquisition_lifecycle.go` |
| 数据库分页与一致性快照 | `list_pagination.go` |
| 获取任务与 RSS 条目列表投影 | `list_projection_store.go`、`backend/db/query/list_projections.sql` |
| RSS 条目展示与重试能力 | `rss_entry_read_model.go` |
| 操作日志、资源事件 | `operation_read_model.go`、`event_read_model.go` |
| 仪表盘 | `dashboard_read_model.go` |

领域层不依赖具体数据库和基础设施；基础设施与仓储不反向依赖服务、接口和 Worker；服务不依赖接口和 Worker；HTTP 接口通过领域模型和服务访问应用能力。这些边界由 `.golangci.yml` 中的依赖检查约束，测试文件可使用集成夹具。

## 列表分页与投影维护

下载、获取任务和 RSS 条目由数据库筛选、排序并选择最多 `limit + 1` 个 ID，再批量读取当前页及其关联记录。仪表盘在数据库中统计待处理总数，只组装前 5 条详情。键集游标必须属于当前筛选集合，排序相同的记录按 UUID 排序。

获取生命周期和 RSS 分类沿用 Go 中的业务规则，投影保存分页所需的阶段、分组、进度和规范化排序键。业务表触发器在同一事务中标记投影过期；`RegisterProgressCommitHook` 在提交前按获取任务、RSS 条目、RSS 订阅的顺序重算。业务写入先锁涉及的订阅汇总行，再锁获取任务和 RSS 条目投影；锁定订阅行不改变汇总状态。投影补算保持从父到子的锁顺序。

列表在 PostgreSQL Repeatable Read 只读快照中完成选页和详情组装。过期投影会先退出快照，再按每批最多 100 个资源补算；API 启动时执行补算。直接维护 SQL 也会触发标记，后续读取可恢复投影。遇到更新版本的投影时，旧程序拒绝降级覆盖。

修改生命周期、分类、阶段、进度或字符串规范化语义时，递增 `listProjectionVersion`，使既有投影重新计算。订阅汇总语义变化时同步递增 `rssSubscriptionProgressModelVersion`。分页函数和索引通过数据库迁移维护；修改已发布结构或排序函数时新增迁移，并同步 SQLC 查询。生成文件由 `npm run generate:sqlc` 更新。

## 检查

| 命令 | 范围 |
| --- | --- |
| `npm run check` | 静态分析、分层依赖、死代码、单元测试、构建和生成文件漂移 |
| `npm run test:scripts` | 维护脚本的工具链选择、检查退出码和工作区保护 |
| `npm run check:deadcode` | 加载普通与集成测试的调用关系，检查非生成 Go 代码的不可达函数；不执行测试、不连接数据库 |
| `npm run check:integration` | 数据库迁移与 PostgreSQL 集成测试 |
| `npm run test:e2e --workspace @emby-auto/web` | 浏览器测试 |
| `npm run deploy:check` | Compose 配置 |
| `npm run deploy:direct:check` | Linux 直接部署模板 |
| `npm run deploy:install:check` | 安装器语法、安装与重试、校验失败和配置保护 |

读取规则由 `read_service_test.go` 覆盖。`list_sort_integration_test.go` 和 `list_pagination_order_integration_test.go` 检查所有排序方向、跨页完整性、筛选、Unicode 标题和游标边界；排序差分以旧 Go 排序规则为参考。`list_projection_integration_test.go` 和 `list_projection_lock_order_integration_test.go` 检查事务回滚、并发提交快照、批量写入与后台补算的锁顺序、维护 SQL 恢复和版本保护；迁移测试验证回滚后保留业务数据并可重新补算。

`list_pagination_capacity_integration_test.go` 比较 64 条与 8192 条历史数据，在固定页大小下检查 SQL 次数、返回行数、候选 ID、分配量和补算批次。修改列表路径时保持这些约束，详情读取范围应随当前页的关联数据变化。

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

发布包包含 API、Worker、宿主控制器、后台状态命令、发布预检、stale operation 恢复、地址与暂存所有权迁移工具、网页、安装器、服务模板、部署文档和包内校验和。原生控制服务只接受 root 与配置的应用 UID；应用与维护命令使用数据库会话锁串行化后台转换。目录迁移依赖任务范围、设备与 inode 校验，恢复点使用私有权限并在变更前持久化。

`v*` 标签触发 Release 工作流。公开 Release 附带 `install.sh`、两种架构的发布包及 `SHA256SUMS`；安装器依赖以下公开文件名：

~~~text
install.sh
emby-auto-direct-amd64-linux-amd64.tar.gz
emby-auto-direct-arm64-linux-arm64.tar.gz
SHA256SUMS
~~~

变更上述名称时需要同步安装器与发布流程。首次发布安装器后，README 中的一键安装地址才会在 GitHub Release 上可用。
