# Ansatz-agent-platform 当前进展

最后更新：**2026-09-03**

本文件只维护当前阶段、里程碑、下一步和边界；详细证据链接到专项报告。

## 当前阶段

**阶段：Trace Token/Cost 计量链路与 SJTU Gateway 已完成服务端生产验收；等待外部 `wangzihe` 客户端更新后完成真实账号新 Trace 验收。**

Trace accounting 修复已合并：客户端 `main@41f892309` 捕获 Relay 解码前的 provider usage，
平台 `main@df12eda064` 将逻辑 LLM accounting 投影到最终物理 generation，个人 Trace UI
`main@5fae11d253` 不再把缺失证据显示为真实零。生产 Gateway 镜像为
`localhost/ansatz-trace-gateway:main-20260901-df12eda064`，镜像 ID 为
`c77c636834b282209d96722297587c5635e2e956d23c984f873ee9a99562388a`。生产测试账号通过
公网完整链路写入 `168` tokens 和 `$0.000321`，owner 页面返回 200 并显示 `168 tokens` /
`$0.00032`，另一账号返回 404。完整证据和既有 17 条 `wangzihe` Trace 无法精确回填的边界见
[`reports/2026-09-01-trace-accounting-production.md`](reports/2026-09-01-trace-accounting-production.md)。

大会话 502/OOM 修复已发布：服务端 `main@44cb69235f` 通过惰性 IO 只加载当前选中的
observation；平台 `main@1b7adab99a` 为 ClickHouse 默认 profile 加入 1 GiB 内存、64 MiB
结果、35 秒和 2 线程上限及对应最大约束。生产 Auth 镜像为
`localhost/ansatz-auth-service:main-20260829-44cb69235f`，镜像 ID 为
`b9a322969ae1bd06ee96431d6f2e91af5e2b3237f052e8684c9592bc434b0201`。最大生产
session（843 observations、570,830,633 bytes 事件数据）的 Session、Trace overview、4.3 MiB
和 1.1 MiB step 均返回 200；没有新增内核 OOM。完整证据见
[`reports/2026-08-29-bounded-trace-query-production.md`](reports/2026-08-29-bounded-trace-query-production.md)。

Hermes 上 Voice Trace ClickHouse 的诊断日志失控已完成生产修复：文件日志降为 warning 并限制为 100 MiB × 3，内存/CPU/查询 profiler 关闭，Langfuse 不消费的高量 `system.*` 日志关闭并清空，保留日志设置七天 TTL。认证服务、其他 Voice Trace 容器以及主机上的其他容器均未重建。

普通用户个人分析页与 session-first Trace Explorer 的上一版发布基线为：服务端远端与本地
`main@01c73ca1ad3ca61ee67129fd2304ad49d0778018`，生产镜像为
`localhost/ansatz-auth-service:main-20260827-01c73ca1ad`，镜像 ID 为
`4b45d69aca97042d9e6b531308d9a6dbf5b9d3721b671a57ab85f519d0193c0d`。生产账号的
30 天 session 索引、session 详情和 trace 详情均完成真实 owner-scoped 数据验收；32 万字符级
step 的完整 Input 被限制在 420 px 内部滚动区，整页高度保持 981 px；全栈私网/公网健康检查通过。

客户端、认证服务与 Trace Gateway 的本地开发分支已实现并验证：认证服务暂时不可用时保持既有登录和本地对话能力；客户端重启可先从受保护缓存恢复；只有 Sign out 或可信且身份匹配的结构化撤销停止能力；Trace token 不再阻塞本地 Hermes；未上传 Trace 进入加密持久 outbox，恢复后 FIFO 补传；Gateway 在持久接收后才返回幂等 receipt。

CLI、独立 Web Dashboard 与 Desktop 的统一 Trace 上传链路已在本地任务分支完成：三者通过同一
native auth owner 获取账号/Session/installation 绑定、Trace token、entrypoint-bound loopback
lease、OTLP ingress、AES-GCM 持久 outbox、FIFO 幂等恢复与 Gateway receipt；入口值严格为
`cli`、`dashboard`、`desktop`，不存在缺省 Desktop 或伪造 relabel。客户端实现截止
`50b62d28f`，Gateway 三入口规范化/receipt 契约截止 `62c1605`。Python 455 项、Desktop
1793 项、Gateway 普通/race/vet 门禁通过；详细设计与计划见本文件索引。该结果表示已提交至
远端 feature branch 的评审候选与本地验证，**不表示已经 merge、installer package 或 deploy**。

这些变更目前只存在于本地分支，**没有 push、PR、merge、installer package 或 deploy**。已完成一次未打包的客户端 production build 与本地 Playwright 验证；现网仍以既有部署报告为准。

## 里程碑

| ID | 里程碑 | 状态 | 结果或入口 |
|---|---|---|---|
| `M-001` | Hermes → NeMo Relay → Langfuse MVP | Done | 历史 MVP 已完成；证据由既有 2026-08-23 报告维护 |
| `M-009` | 多 Hermes 客户端认证与个人 Trace 接入 | Review | 既有部署状态与真实对话验收仍由历史报告维护 |
| `M-011` | `/data`、`/auth`、个人 `/traces` 与独立 `/langfuse` | Review | 既有生产部署，不等同于本轮连续性代码已发布 |
| `M-012` | 认证连续性与 Trace 离线补传 | Done | 本地三仓实现与自动化验证完成；见 [`reports/2026-08-25-auth-trace-continuity-e2e.md`](reports/2026-08-25-auth-trace-continuity-e2e.md) |
| `M-013` | Hermes ClickHouse 日志失控修复 | Done | 生产证据：`/data/ansatz-agent/voice-trace/evidence/clickhouse-logging-20260826T052311Z`；运维入口见 [`runbooks/storage-auth-personal-traces.md`](runbooks/storage-auth-personal-traces.md) |
| `M-014` | 普通用户 Dashboard 与 Model Analytics | Done | 服务端 `main@aa4278d270` 已部署；生产真实账号 Dashboard/Model Analytics 均返回 200 |
| `M-015` | Session-first Trace Explorer | Done | PR #7 与 #8 已合并；服务端 `main@01c73ca1ad`、生产镜像 `main-20260827-01c73ca1ad`；真实长 payload 生产验收通过 |
| `M-016` | Bounded Trace 查询与 ClickHouse OOM 护栏 | Done | 服务端/平台 PR #9 已合并；生产镜像 `main-20260829-44cb69235f`；最大 session 与大 payload 回归、全栈健康、无新增 OOM 均通过 |
| `M-017` | Trace Token/Cost accounting | Review | 客户端/平台/Auth PR #24/#11/#10 已合并；Gateway/Auth 已部署且生产测试账号完整链路通过；待 `wangzihe` 更新客户端后的新 Trace 验收 |
| `M-018` | CLI/Dashboard/Desktop 统一 Trace 上传 | Done | 本地客户端与 Gateway 契约实现、三入口真实边界 E2E 和全量自动化验证完成；未 push、PR、merge、打包或部署 |

## 当前已确认设计

- 已登录用户在 timeout、断网、DNS/VPN/代理、429、5xx 或异常响应期间保持授权 scope、本地 Hermes 和当前对话。
- 客户端先恢复受保护的认证缓存，再后台静默验证；只有 Sign out 或可信、身份匹配的账号/Session 撤销是终态。
- Sign out 不删除 SessionDB、附件、本地对话或 Trace outbox。
- Trace token 异步 single-flight 获取，不是本地后端启动前置条件；401 只刷新 Trace token 并重发同批次。
- 客户端采用 write-ahead 加密 outbox 与 Gateway durable receipt 竞速。成功 receipt 后不保留 Trace/span payload，只保留有界、无 payload 的 receipt tombstone。
- Outbox 每账号 2 GiB、30 天、64 MiB 最大加密 record；FIFO 补传并在安全的 streaming idle 窗口做有界 compaction。
- Gateway 以稳定 `account_id + batch_id` 持久幂等，accepted/duplicate 均可转移持久所有权并唤醒后台投递。
- CLI、独立 Dashboard 与 Desktop 只通过 auth owner 的显式 entrypoint lease 接入同一 Trace 服务；旧 Electron outbox 仅无接纳端口地恢复历史密文。

## 下一步

1. 让 `wangzihe` 的 Windows Desktop 运行正常更新流程到客户端 `main@41f892309` 或更高版本，并产生一条新对话。
2. 在生产 ClickHouse 与 owner-scoped `/traces` 页面同时验证该新 generation 的 usage/cost 非空；不得用测试账号证据替代真实账号验收。
3. 认证连续性与 Trace 离线补传的剩余发布边界继续按专项报告处理。
4. 持续观察 SJTU ClickHouse 日志、查询拒绝、容器重启次数与 `/data` 使用率；任何 Gateway 重建必须同时带 base Compose 与 `docker-compose.sjtu.yml`。
5. 三入口统一 Trace 分支进入评审前，先同步远端主线并按一任务一 PR 提交；合并前不得打包或部署。

## 已知边界

- 本次大会话修复已经生产部署；认证连续性与 Trace 离线补传没有随之发布，现网不会自动获得该新协议。
- 既有 17 条 `wangzihe` generation 没有 provider usage/cost 原始证据，不能精确回填；新客户端只修复更新后的调用。
- 生产测试账号已经证明服务端链路会显示非零 Token/Cost，但 `wangzihe` 的外部 Windows 安装尚未提供更新后的新 Trace，真实账号验收仍未完成。
- 没有完成 live Windows packaged-runtime 验收；现有 Windows 证据限于代码级、路径级和静态/自动化测试。
- 本地 Playwright 使用受控服务，Gateway E2E 使用临时 bbolt/HTTP harness；它们证明代码契约，不证明公网或生产拓扑。
- 三入口统一上传已完成本地代码与自动化验证，但真实新安装包对话、Windows packaged-runtime、发布和生产验收仍未完成。
- Podman 3.3.1 原地启动 ClickHouse 时会输出“transient timer unit already exists”，但 SQL 探活及全部 Voice Trace 私网/公网健康检查通过；不要把该 stderr 单独当作服务失败。

## 状态更新规则

- 可变状态只更新本文件；稳定背景和路径索引不在此复制。
- `Done` 必须说明完成边界。本轮 `M-012 Done` 仅表示本地实现与验证完成，不表示已发布或部署。
- 专项设计、计划、runbook 和验证细节链接到对应文档。
