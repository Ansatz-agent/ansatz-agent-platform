# Ansatz-agent-platform 当前进展

最后更新：**2026-08-25**

本文件只维护当前阶段、里程碑、下一步和边界；详细证据链接到专项报告。

## 当前阶段

**阶段：认证连续性与 Trace 离线补传已完成本地实现和验证，等待独立的 Git 交付与发布授权。**

客户端、认证服务与 Trace Gateway 的本地开发分支已实现并验证：认证服务暂时不可用时保持既有登录和本地对话能力；客户端重启可先从受保护缓存恢复；只有 Sign out 或可信且身份匹配的结构化撤销停止能力；Trace token 不再阻塞本地 Hermes；未上传 Trace 进入加密持久 outbox，恢复后 FIFO 补传；Gateway 在持久接收后才返回幂等 receipt。

这些变更目前只存在于本地分支，**没有 push、PR、merge、installer build 或 deploy**。现网仍以既有部署报告为准。

## 里程碑

| ID | 里程碑 | 状态 | 结果或入口 |
|---|---|---|---|
| `M-001` | Hermes → NeMo Relay → Langfuse MVP | Done | 历史 MVP 已完成；证据由既有 2026-08-23 报告维护 |
| `M-009` | 多 Hermes 客户端认证与个人 Trace 接入 | Review | 既有部署状态与真实对话验收仍由历史报告维护 |
| `M-011` | `/data`、`/auth`、个人 `/traces` 与独立 `/langfuse` | Review | 既有生产部署，不等同于本轮连续性代码已发布 |
| `M-012` | 认证连续性与 Trace 离线补传 | Done | 本地三仓实现与自动化验证完成；见 [`reports/2026-08-25-auth-trace-continuity-e2e.md`](reports/2026-08-25-auth-trace-continuity-e2e.md) |

## 当前已确认设计

- 已登录用户在 timeout、断网、DNS/VPN/代理、429、5xx 或异常响应期间保持授权 scope、本地 Hermes 和当前对话。
- 客户端先恢复受保护的认证缓存，再后台静默验证；只有 Sign out 或可信、身份匹配的账号/Session 撤销是终态。
- Sign out 不删除 SessionDB、附件、本地对话或 Trace outbox。
- Trace token 异步 single-flight 获取，不是本地后端启动前置条件；401 只刷新 Trace token 并重发同批次。
- 客户端采用 write-ahead 加密 outbox 与 Gateway durable receipt 竞速。成功 receipt 后不保留 Trace/span payload，只保留有界、无 payload 的 receipt tombstone。
- Outbox 每账号 2 GiB、30 天、64 MiB 最大加密 record；FIFO 补传并在安全的 streaming idle 窗口做有界 compaction。
- Gateway 以稳定 `account_id + batch_id` 持久幂等，accepted/duplicate 均可转移持久所有权并唤醒后台投递。

## 下一步

1. 完成三仓最终 diff/secret 独立审阅，并保留本报告所列本地验证证据。
2. 只有获得单独授权后，才 push 分支、创建 PR、合并、构建安装包或部署。
3. 发布阶段另做 packaged macOS/Windows 与 staging/production acceptance；不得把当前本地 Playwright/Go harness 描述成 Windows 或生产运行时验证。

## 已知边界

- 本轮没有执行生产部署，现网不会自动获得新认证/Trace 协议。
- 没有完成 live Windows packaged-runtime 验收；现有 Windows 证据限于代码级、路径级和静态/自动化测试。
- 本地 Playwright 使用受控服务，Gateway E2E 使用临时 bbolt/HTTP harness；它们证明代码契约，不证明公网或生产拓扑。
- 三入口统一上传、真实新安装包对话、历史 Token/Cost 等既有事项不因本轮连续性实现自动关闭。

## 状态更新规则

- 可变状态只更新本文件；稳定背景和路径索引不在此复制。
- `Done` 必须说明完成边界。本轮 `M-012 Done` 仅表示本地实现与验证完成，不表示已发布或部署。
- 专项设计、计划、runbook 和验证细节链接到对应文档。
