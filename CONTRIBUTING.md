# 贡献指南

**简体中文** | [English](CONTRIBUTING.en.md)

本文档规定 Emby Auto 的代码贡献流程与质量要求。所有改动都应保持现有接口契约、任务边界和媒体安全规则。

## 提交前准备

- 搜索现有 Issue 和 Pull Request，避免重复工作。
- 使用最小、可复现的案例描述缺陷或行为差异。
- 对跨模块功能、接口兼容性或数据模型有显著影响的改动，应先通过 Issue 说明目标与方案。
- 安全漏洞不得在公开 Issue 中披露，应遵循 [安全策略](SECURITY.md)。

## 开发流程

1. 从最新目标分支创建独立分支。
2. 将改动限定在一个明确问题内，避免混入无关重构或格式化。
3. 先更新契约或迁移，再实现服务端、Worker 和 Web 行为。
4. 为变更补充与风险范围匹配的测试。
5. 运行代码生成器及适用的质量检查。
6. 提交包含行为、兼容性、风险和验证结果的 Pull Request。

## 架构契约

以下约束适用于所有贡献：

- API 与 Worker 保持独立进程。API 不执行下载、转码、字幕处理、文件移动、入库或清理等长任务。
- [`contracts/openapi.yaml`](contracts/openapi.yaml) 是唯一 HTTP 接口契约，不维护重复的手写客户端或服务端类型。
- 任务状态只由后端状态机转换。状态转换、River 任务创建和事件记录必须位于同一数据库事务。
- River 任务需要稳定幂等键、唯一性、重试、超时、取消、心跳和错误记录。
- 只有视频转码使用专用并发槽位；其它后台任务不得等待转码槽位。
- 确定性规则负责标准流程、安全边界和输出校验；Agent 仅作为受控的非典型场景补充能力。
- 媒体输出先写入目标文件系统的临时文件，校验通过后再原子重命名。
- 不引入 Redis、非必要微服务、Docker socket 或宿主级特权依赖。

## 接口、数据库与生成文件

### OpenAPI

修改 HTTP 行为时，先更新 `contracts/openapi.yaml`，再执行：

```bash
npm run generate
npm run check:generated
```

不得直接编辑：

- `backend/internal/transport/httpapi/api.gen.go`
- `backend/db/sqlc`
- `apps/web/src/api/generated`

### 数据库

数据库变更必须新增连续迁移。已经发布的迁移不得改写或重新编号。迁移应同时覆盖升级路径、必要的回滚语义以及从受支持历史版本升级的测试。

sqlc 查询发生变化时，应更新 `backend/db/query` 中的源查询并重新运行生成器。

## 代码规范

- Go 代码使用 `gofmt`，并通过仓库固定版本的 `golangci-lint`。
- TypeScript 保持严格类型，不使用手写类型替代生成的 OpenAPI SDK。
- 注释用于说明不明显的约束、失败语义和设计原因，不复述代码行为。
- 用户可见文案应明确、可操作，不暴露内部路径、堆栈或原始上游响应。
- 新依赖需要说明必要性、维护状态、许可证和对发布体积的影响。

## 测试与验证

测试期望应来自独立、具体的业务案例，不得调用被测实现生成期望值。

| 改动范围 | 最低验证要求 |
| --- | --- |
| 所有代码改动 | `npm run check` |
| 数据库、状态机或持久化任务 | `npm run check:integration` |
| 并发敏感的 Go 代码 | `npm run test:race` |
| Web 交互 | `npm run test:e2e --workspace @emby-auto/web` |
| 跨进程完整流程 | `npm run test:e2e:full` |
| Docker 部署模板 | `npm run deploy:check`、`npm run deploy:build`、`npm run deploy:check-release` |
| Linux 直接部署模板 | `npm run deploy:direct:check`、`npm run deploy:direct:build`、`npm run deploy:direct:check-release` |

完整 E2E 必须使用隔离测试环境。自动测试不得连接生产 qBittorrent、Emby、TMDb 账户或正式媒体目录。外部服务验收所需凭据只能通过进程环境注入。

## 分支与提交规范

- 分支命名：`feat/<描述>`、`fix/<描述>`、`docs/<描述>`、`chore/<描述>`、`refactor/<描述>`、`test/<描述>`。
- 提交消息遵循 Conventional Commits（`feat:`、`fix:`、`docs:`、`chore:`、`test:`、`refactor:`），描述具体、与改动一致。
- 维护者在仓库内创建短生命周期分支；外部贡献者 fork 后在自己的 fork 中创建分支。

## Pull Request 要求

Pull Request 应包含：

- 变更目的及关联 Issue。
- 用户可见行为与接口变化。
- 数据库迁移、兼容性和回滚影响。
- 主要风险及对应测试。
- 已执行的完整验证命令。
- 需要部署者执行的升级步骤。

提交前确认工作树不包含无关生成漂移、依赖锁文件变化或格式化噪声。

## 合并规则

- 所有 PR 必须通过 CI 全部状态检查：`verify`、`container-build`、`direct-build (amd64)`、`direct-build (arm64)`。
- 必须由至少 1 名维护者 review 并 approve，对话全部解决，且分支基于最新 `main`。
- 只允许 squash merge，保持 `main` 线性历史；禁止直接推送 `main`。
- 版本发布使用 `v0.x.y` 标签，`v*` 标签触发 CI 构建公开发布包并附加到 GitHub Release；发布节奏与内容由维护者决定。

## 仓库数据边界

不得提交：

- 本地 `.env` 运行文件（脱敏的 `.env.example` 模板除外）、真实密码、Token、Cookie、数据库连接、私有订阅地址或代理配置。
- 真实媒体、字幕、下载缓存、日志、数据库 dump、备份或运行时状态。
- 生产主机信息、用户数据、外部服务原始响应或未脱敏诊断材料。
- 与变更无关的本地工具输出、编辑器状态或临时文件。

## 许可证

提交贡献即表示贡献者有权提供相关内容，并同意其按照项目的 [MIT License](LICENSE) 发布。
