# Agent 边界、协议与性能测试记录

本文面向开发维护环境，记录 2026-10-18 对 Agent API 客户端、工具循环、7 类业务能力及相关 Service、Worker、HTTP API 的测试。源码基线为 `0841daa`，以下版本、用例计数和性能数据保留测试当时的记录。当前开发要求见 [开发与发布维护](development.md)。

测试环境为 Linux/amd64、Go 1.27.2、独立 PostgreSQL 18.4 测试实例；另使用 Go 1.26.1 完成全仓库检查，并在独立 PostgreSQL 17 实例运行完整集成检查。测试数据库和模拟服务均与实际业务服务隔离。

## 已复现与修复的问题

| 问题 | 修复前的可复现行为 | 修复后的行为 |
| --- | --- | --- |
| DNS 超时 | 配置 20 ms 请求超时，DNS 查询仍等待到外层 1 s 超时 | 请求上下文同时约束 DNS、请求编码、请求与响应读取 |
| 取消后仍提交 | 只读工具取消上下文后，同批后续提交仍成功；执行器取消或超时被归为作用域违规 | 在工具和提交回调周围检查取消，取消不可重试，超时保留可重试分类 |
| 读取错误响应时丢失取消 | 读取 HTTP 429 响应体发生取消或超时，仍返回限流错误 | 保留取消、超时及原始错误原因 |
| 参数形状 | `null`、数组、布尔值、字符串、数字可以进入业务回调 | 仅 JSON 对象进入回调，其他形状返回可修复的工具参数错误 |
| 响应完整性 | 空调用 ID、同批重复 ID、不支持的工具类型，以及被截断或过滤的完成响应仍可能执行工具 | 执行前检查响应角色、调用结构、调用 ID 和完成原因 |
| 坐标整数边界 | RSS 坐标和下载文件提案接受超过 `int32` 上限的季集数，后续写库可能截断或失败 | 前置提交校验统一限制为 `1..2147483647`，覆盖最大合法值、刚越界值及截断后回绕为正数的值 |
| 服务端错误分类 | HTTP 500、408 被归为不支持的协议；连接拒绝等网络错误被误判为超时 | 区分服务不可用、请求超时、连接失败及取消，并保留重试分类 |
| 严格 Schema 兼容性 | RSS 裁决、下载文件解析的可选字段，以及集数映射的对象分支不满足统一 `strict: true` 的约束；无参数工具的 `required` 可能编码为 `null` | 根据 Schema 兼容性选择严格模式，保留可选字段和业务分支，补充对象根类型并将空 `required` 编码为空数组；常量约束转换为枚举 |
| 连通性假成功 | 同一响应同时调用 echo 和提交即可通过，未验证工具结果回传 | echo 和提交分别完成请求，确认工具结果可回传 |
| 空闲连接滞留 | 每次连通性测试及 Agent 执行创建的独立 Transport 在结束后保留空闲连接 | 所有者在操作结束时关闭空闲连接 |
| 配置在模型请求期间变化 | 关闭 Agent、关闭对应能力、切换模型或增加配置版本后，旧提案仍会应用到 RSS 条目 | 应用前重读配置；能力关闭时取消，配置不匹配时过期；实时校验后再次检查配置 |
| 大列表超过工具结果限制 | 50 条合法长标题生成 100–300 KiB 结果，工具循环因超过 64 KiB 而失败 | 可分页的静态列表按字节大小拆页并缓存，每页保留完整字段；根据页数增加工具预算，最高 64 步 |
| 工具历史反复编码 | 每轮重新转义全部既有消息，大结果场景产生显著 CPU 与分配成本 | 单次循环内缓存只追加的消息编码，后续请求继续携带完整历史 |
| 请求体异常增长 | 仅 8 轮自由文本就能生成约 42 MiB 的单次请求 | 发送前限制 JSON 请求体为 16 MiB，超限终止且不自动重试 |
| 超限结果处理开销 | 超过 64 KiB 的工具结果仍先完整扫描 JSON | 优先检查长度，再校验 JSON |

Schema 兼容性测试按照 [OpenAI Function Calling 的严格模式约束](https://developers.openai.com/api/docs/guides/function-calling)检查实际生成的请求。7 类提交工具全部经过该测试；修复前能独立复现 3 类业务提交 Schema 不兼容。最终提交由后端类型解析、资源范围检查和业务校验决定。

配置生命周期集成测试覆盖配置保持不变、关闭 Agent、关闭能力、切换模型、仅版本变化、资源变化，以及配置读取暂时失败。读取失败后复用已保存提案重试，模型调用保持为一次。

分页测试覆盖长中文、引号、反斜杠、换行、无效页码及参数类型。另以真实 HTTP 工具循环读取 120 条长集名，检查完整读取后成功提交。小列表和对象结果保留原有格式；单项本身超过限制时仍由工具结果大小检查拒绝。分页流程受 64 步与任务总超时约束。

## 协议与并发范围

| 范围 | 结果 |
| --- | --- |
| HTTP/1.1 | echo、工具结果回传、提交通过 |
| HTTPS/1.1 | 使用测试证书信任链验证，通过 |
| HTTPS/HTTP/2 | 检查服务端实际协商到 HTTP/2，通过 |
| HTTP、HTTPS 代理 × HTTP、HTTPS 上游 | 4 种组合通过；HTTPS 上游经过真实 CONNECT 隧道 |
| gzip 响应 | 解压后的内容仍受 1 MiB 响应上限约束 |
| 32 个并发工具会话 | 共享客户端完成 64 个请求，会话结果独立，竞态检测通过 |
| OpenAI Responses、Anthropic Messages、SSE 响应体 | 当前 Chat Completions 客户端明确返回无效响应 |
| SOCKS5 等非 HTTP(S) 代理 | 由现有代理校验测试确认拒绝 |

当前实现的模型 API 协议为 OpenAI Chat Completions。上述其他 API 格式属于拒绝性测试。本次没有调用真实模型服务，不能据此声称已完成各服务商、模型或自建网关的在线兼容性验收。

## 验证结果

- 后端单元测试共 29 个包通过，Go 1.27.2 与 Go 1.26.1 均执行通过；2 项要求在隔离 root 容器内验证的主机维护测试按条件跳过。Agent 新增用例全部通过。
- `GOTOOLCHAIN=go1.26.1 npm run check` 通过，包含 OpenAPI 与 Go 静态检查、后端单元测试、36 个文件的 280 项网页测试、前后端构建和生成文件漂移检查。
- PostgreSQL 18.4 上的 Service、Worker、HTTP API 数据库集成测试通过；PostgreSQL 17 上的 `GOTOOLCHAIN=go1.26.1 npm run check:integration` 通过，包含迁移状态、Schema 约束与全部后端集成测试。
- 全仓库 `npm run test:race` 通过；配置生命周期集成测试额外通过竞态检测。
- API 模糊测试以 2 个工作进程运行 15 s，最终完成 113,411 次执行，同时检查请求体与标准 JSON 编码一致、回调范围及工具步数预算。
- 分页模糊测试运行 10 s，完成 229 次执行，检查任意文本和列表长度下的 JSON 有效性、大小限制、顺序、完整性及 continuation；种子包含无效 UTF-8 与控制字符。
- 网页类型检查及 `presentation.test.ts` 的 11 个测试通过。
- Agent API 和 Harness 的语句覆盖率分别为 81.7% 与 85.8%；未达到的分支不能视为已验证。

## 工具循环性能

源码基线和当前工作区分别运行同一基准 3 次，以下取中位数。基准使用本地自定义 HTTP Transport，完整序列化并消费请求体，覆盖响应解析和工具循环；不包含网络传输、模型推理、数据库和真实业务工具耗时。

| 场景 | 修复前耗时/次 | 修复后耗时/次 | 修复后累计请求体/次 | 修复后累计分配/次 |
| --- | ---: | ---: | ---: | ---: |
| 4 步，1 KiB 工具结果 | 37.5 μs | 40.7 μs | 12.5 KiB | 43.5 KiB |
| 16 步，4 KiB 工具结果 | 558 μs | 351 μs | 523 KiB | 781 KiB |
| 64 步，4 KiB 工具结果 | 7.89 ms | 3.13 ms | 8.30 MiB | 9.51 MiB |
| 64 步，64 KiB 工具结果 | 89.5 ms | 21.2 ms | 126.4 MiB | 136.5 MiB |
| 拒绝 1 MiB 超限工具结果 | 391 μs | 8.74 μs | — | 7.08 KiB |

64 步大结果场景的本地耗时降低约 76%，累计分配从约 212.5 MiB 降至 136.5 MiB，降低约 36%。超限结果的拒绝耗时降低约 97.8%。新增校验和缓存使小场景的分配次数增加，4 步样本耗时增加约 3 μs，不能据此宣称所有场景普遍加速。

缓存前的剖析样本中，JSON 字符串编码约占 CPU 的 56%；缓存后的样本约为 6%。缓存后的主要成本转为请求体内存复制和清零，两者约占 CPU 的 42%；请求体组装约占累计分配的 93%。

每轮请求继续发送完整历史。请求体组装和累计传输量仍随轮数呈平方增长，64 步大结果场景仍累计发送约 126 MiB。16 MiB 上限限制单次请求的异常增长。

表中的分配量为 `B/op`，代表一次完整工具循环的累计分配，不能当作进程内存峰值。请求体大小代表所有轮次之和。

## 业务容量与上下文性能

业务基准同样取 3 次中位数，使用已构造的作用域快照，不包含数据库读取与模型调用。

| 场景 | 本地耗时/次 | 累计分配/次 |
| --- | ---: | ---: |
| 50 条 RSS 裁决与 50 条历史的提案校验 | 18.8 μs | 5.46 KiB |
| 128 个视频与 1024 个字幕的提案校验 | 135 μs | 112 KiB |
| 50 条短标题的静态工具构建 | 15.8 μs | 14.8 KiB |
| 50 条长中文标题的分页缓存构建 | 1.05 ms | 1.51 MiB |
| 长标题列表缓存的第一页读取 | 0.36 μs | 624 B |

长标题列表原结果约 306 KiB，修复后分为 5 页，第一页约 61.2 KiB。分页预处理增加了构建成本：同一输入在基线中的构建耗时约 0.324 ms、累计分配约 338 KiB，但其原结果无法通过工具大小限制。缓存后的分页读取复用编码结果，逐页保留完整数据。

这些样本中，RSS 与文件提案校验成本低于多轮请求的组装成本；实际任务耗时仍需结合数据库和真实模型服务测量。

## 复现命令

在仓库根目录执行。数据库集成测试需要事先将 `TEST_DATABASE_URL` 指向独立 PostgreSQL 实例，测试角色需要创建和删除测试数据库的权限；测试会自行创建、迁移并清理临时数据库。

```bash
go -C backend test ./... -count=1

go -C backend test -race ./internal/platform/agentapi ./internal/agentharness ./internal/service ./internal/worker -count=1

go -C backend test -tags=integration -count=1 -timeout=5m ./internal/service ./internal/worker ./internal/transport/httpapi

go -C backend test -tags=integration -race ./internal/service -run '^TestAgentResolutionRechecksConfigurationAfterProviderCallIntegration$' -count=1

go -C backend test ./internal/platform/agentapi -run '^$' -fuzz '^FuzzToolLoopProviderResponses$' -fuzztime=15s -parallel=2

go -C backend test ./internal/service -run '^$' -fuzz '^FuzzAgentStaticListPages$' -fuzztime=10s -parallel=2

go -C backend test ./internal/platform/agentapi -run '^$' -bench 'BenchmarkToolLoop' -benchmem -benchtime=150ms -count=3

go -C backend test ./internal/service -run '^$' -bench 'BenchmarkAgent' -benchmem -benchtime=150ms -count=3

npm run lint:go
npm run test --workspace @emby-auto/web -- src/lib/presentation.test.ts
npm run typecheck --workspace @emby-auto/web
```

新增测试位于 `backend/internal/platform/agentapi/*_test.go`、`backend/internal/agentharness/protocol_schema_test.go` 和 `backend/internal/service/agent*_test.go`。测试使用隔离数据库及本地模拟服务。
