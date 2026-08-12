## 变更摘要 / Summary

<!-- 说明变更目的、关联 Issue 和用户可见结果。 / Describe the purpose, linked issue, and user-visible outcome. -->

## 兼容性与风险 / Compatibility and Risk

<!-- 说明 API、数据库迁移、任务状态、媒体文件、部署或回滚影响。无影响时明确填写“无 / None”。 -->

## 验证结果 / Verification

<!-- 列出实际执行的命令及结果。 / List the commands that were actually run and their results. -->

- [ ] `npm run check`
- [ ] 涉及数据库时已运行 `npm run check:integration` / Ran for database changes
- [ ] 涉及 Web 交互时已运行浏览器测试 / Ran browser tests for Web changes
- [ ] 涉及部署时已运行对应 build 与 release 检查 / Ran the applicable deployment build and release checks
- [ ] 已补充与风险范围匹配的测试 / Added tests matching the risk scope
- [ ] 生成文件已通过生成器更新 / Updated generated files through the generators

## 提交检查 / Submission Checklist

- [ ] 改动范围集中，不包含无关重构或格式化 / The change is focused and excludes unrelated churn
- [ ] 已说明迁移、升级和回滚要求 / Migration, upgrade, and rollback requirements are documented
- [ ] 不包含凭据、真实媒体、运行数据或未脱敏诊断信息 / No credentials, real media, runtime data, or unredacted diagnostics are included
- [ ] 文档与用户可见文案已同步更新 / Documentation and user-facing text are updated
