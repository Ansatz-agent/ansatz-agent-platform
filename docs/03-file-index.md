# Ansatz-agent-platform 文件索引

最后核对：**2026-09-03**

本文件只维护权威文档和实现入口。当前状态见 [`02-progress.md`](02-progress.md)。

## 项目与组件边界

| 路径 | 责任 |
|---|---|
| `../agent-hermes-client/` | Desktop/Hermes 客户端、认证缓存、Electron 协调器、加密 Trace outbox 与恢复上传 |
| `../agent-langfuse-server/` | 稳定 account identity、持久 Client Session、结构化撤销与 Session-bound Trace token |
| `services/trace-gateway/` | Trace token introspection、持久 bbolt inbox、幂等 receipt 与异步 FIFO 投递 |
| `deploy/voice-trace/` | Gateway/auth/Langfuse 既有 Compose 契约；本轮未部署 |
| `tests/` | 平台 Compose、脚本、路由和 Trace 协议契约测试 |
| `docs/` | 需求、设计、计划、runbook、报告与轻量路由文档 |

组件仓库独立提交和演进；平台仓库不 vendoring 它们。当前连续性实现只在三个本地 feature branch，尚未 push、PR、merge 或 deploy。

## 当前连续性文档

| 用途 | 权威入口 |
|---|---|
| 本地实现与验证证据、精确 refs 和边界 | [`reports/2026-08-25-auth-trace-continuity-e2e.md`](reports/2026-08-25-auth-trace-continuity-e2e.md) |
| Trace outbox/inbox 运维、诊断、备份、compaction 与回滚 | [`runbooks/trace-upload-continuity.md`](runbooks/trace-upload-continuity.md) |
| 认证/Trace 连续性架构 | [`superpowers/specs/2026-08-24-auth-trace-continuity-design.md`](superpowers/specs/2026-08-24-auth-trace-continuity-design.md) |
| 客户端/服务端认证连续性任务计划 | [`superpowers/plans/2026-08-24-authentication-continuity.md`](superpowers/plans/2026-08-24-authentication-continuity.md) |
| 客户端 outbox 与 Gateway inbox 任务计划 | [`superpowers/plans/2026-08-24-trace-upload-continuity.md`](superpowers/plans/2026-08-24-trace-upload-continuity.md) |
| CLI/Dashboard/Desktop 统一 Trace 设计 | [`superpowers/specs/2026-09-02-unified-client-trace-entrypoints-design.md`](superpowers/specs/2026-09-02-unified-client-trace-entrypoints-design.md) |
| CLI/Dashboard/Desktop 统一 Trace 实施计划 | [`superpowers/plans/2026-09-02-unified-client-trace-entrypoints.md`](superpowers/plans/2026-09-02-unified-client-trace-entrypoints.md) |

## 既有生产与历史文档

| 用途 | 权威入口 |
|---|---|
| 服务端存储、统一认证与 Trace 分级访问需求 | [`requirements/2026-08-24-server-storage-auth-trace-access.md`](requirements/2026-08-24-server-storage-auth-trace-access.md) |
| Hermes `/data`、`/auth`、个人 `/traces` 运维与回滚 | [`runbooks/storage-auth-personal-traces.md`](runbooks/storage-auth-personal-traces.md) |
| 既有安装、登录与 Voice Trace 设计 | [`superpowers/specs/2026-08-23-install-login-voice-tracing-design.md`](superpowers/specs/2026-08-23-install-login-voice-tracing-design.md) |
| 既有存储/认证/个人 Trace 设计 | [`superpowers/specs/2026-08-24-storage-auth-personal-traces-design.md`](superpowers/specs/2026-08-24-storage-auth-personal-traces-design.md) |
| 既有安装、登录与 Voice Trace 计划 | [`superpowers/plans/2026-08-23-install-login-voice-tracing.md`](superpowers/plans/2026-08-23-install-login-voice-tracing.md) |
| 既有存储/认证/个人 Trace 计划 | [`superpowers/plans/2026-08-24-storage-auth-personal-traces.md`](superpowers/plans/2026-08-24-storage-auth-personal-traces.md) |
| Content-first Trace Explorer 设计 | [`superpowers/specs/2026-08-26-content-first-agent-trace-explorer-design.md`](superpowers/specs/2026-08-26-content-first-agent-trace-explorer-design.md) |
| Content-first Trace Explorer 计划 | [`superpowers/plans/2026-08-26-content-first-agent-trace-explorer.md`](superpowers/plans/2026-08-26-content-first-agent-trace-explorer.md) |
| Session-first Trace Inspector 设计 | [`superpowers/specs/2026-08-27-session-first-trace-inspector-design.md`](superpowers/specs/2026-08-27-session-first-trace-inspector-design.md) |
| Session-first Trace Inspector 计划 | [`superpowers/plans/2026-08-27-session-first-trace-inspector.md`](superpowers/plans/2026-08-27-session-first-trace-inspector.md) |
| Bounded Trace query/OOM 修复设计 | [`superpowers/specs/2026-08-29-bounded-trace-query-design.md`](superpowers/specs/2026-08-29-bounded-trace-query-design.md) |
| Bounded Trace query/OOM 修复任务书 | [`superpowers/plans/2026-08-29-bounded-trace-query-task-book.md`](superpowers/plans/2026-08-29-bounded-trace-query-task-book.md) |
| Bounded Trace query/OOM 生产交付证据 | [`reports/2026-08-29-bounded-trace-query-production.md`](reports/2026-08-29-bounded-trace-query-production.md) |
| Trace Token/Cost accounting 生产交付证据 | [`reports/2026-09-01-trace-accounting-production.md`](reports/2026-09-01-trace-accounting-production.md) |

历史部署状态不能替代 2026-08-25 连续性分支的交付状态；新报告也不能反向证明既有生产已经运行新代码。

## 当前实现入口

| 范围 | 入口 |
|---|---|
| 客户端认证桥与协调器 | `../agent-hermes-client/apps/desktop/electron/auth-bridge.ts`、`auth-coordinator.ts` |
| 客户端认证 gate | `../agent-hermes-client/apps/desktop/src/components/auth-gate.tsx` |
| 客户端 Trace outbox | `../agent-hermes-client/apps/desktop/electron/trace-outbox-store.ts`、`trace-outbox-journal.ts`、`trace-outbox-crypto.ts` |
| 客户端 Trace forward/recovery | `../agent-hermes-client/apps/desktop/electron/trace-forwarder.ts`、`trace-recovery-controller.ts`、`trace-runtime-startup.ts` |
| 客户端最终接线 | `../agent-hermes-client/apps/desktop/electron/main.ts` |
| 共享 Auth Owner Trace 核心 | `../agent-hermes-client/hermes_cli/client_auth/trace/`、`hermes_cli/client_auth/runtime.py` |
| CLI 与独立 Dashboard Trace 引导 | `../agent-hermes-client/hermes_cli/client_auth/trace/bootstrap.py`、`hermes_cli/main.py`、`cli.py` |
| Desktop 共享 ingress lease 与旧 outbox 恢复 | `../agent-hermes-client/apps/desktop/electron/auth-bridge.ts`、`main.ts`、`trace-forwarder.ts` |
| 三入口真实边界 E2E | `../agent-hermes-client/tests/hermes_cli/client_auth/trace/test_three_entrypoint_e2e.py` |
| 服务端 Session/撤销/Trace token | `../agent-langfuse-server/auth-service/history/client_sessions.py`、`trace_tokens.py`、`auth_views.py` |
| 个人 Dashboard 与 Trace Explorer | `../agent-langfuse-server/auth-service/history/trace_analytics.py`、`trace_views.py`、`templates/traces/` |
| Gateway durable inbox | `services/trace-gateway/internal/inbox/` |
| Gateway admission/delivery | `services/trace-gateway/internal/server/`、`services/trace-gateway/internal/delivery/` |
| Gateway 三入口 canonicalization 契约 | `services/trace-gateway/internal/otlp/canonicalize_test.go`、`internal/server/server_test.go` |
| Gateway Compose limits | `deploy/voice-trace/docker-compose.yml` |
| ClickHouse log/profiler retention | `deploy/voice-trace/clickhouse/` |
| Hermes ClickHouse bounded remediation | `scripts/remediate-clickhouse-logging.sh`、`runbooks/storage-auth-personal-traces.md` |

## 按任务阅读

| 任务 | 顺序 |
|---|---|
| 判断连续性实现是否完成 | 本地验证报告 → 对应 spec/plan → 三仓当前 refs |
| 客户端认证或 Trace 故障排查 | runbook → 客户端 Electron 入口 → 聚焦测试 |
| Gateway inbox/投递故障排查 | runbook → Gateway inbox/server/delivery → Go tests |
| 发布或回滚 | 先确认用户授权和精确 refs → runbook → Compose；不得从本索引直接推断生产命令 |

## 维护规则

- 新增、移动或废弃长期入口时更新本文件。
- 状态只写入 `02-progress.md`；详细验证输出只写入 report。
- 同一用途有多个候选文档时，明确当前权威入口，不复制可变证据。
