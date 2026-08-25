# Ansatz Agent Platform

[English](#english) | [中文](#中文)

## English

Ansatz Agent Platform is the integration and operations repository for a
client/server Agent observability system. Hermes-based clients run Agent work
locally, authenticate through a shared account service, and upload traces to a
durable gateway. Langfuse stores and visualizes those traces while the platform
keeps ordinary-user and administrator access separate.

### Architecture

```text
Hermes clients
  |-- authenticate ------------------------------> Auth service (/auth)
  |-- encrypted local trace outbox
          `--> Trace Gateway (/trace-ingest)
                   |-- durable inbox and receipts
                   `--> Langfuse
                          |-- personal dashboard (/traces)
                          `-- admin console (/langfuse)
```

The platform is designed around three boundaries:

- **Local-first execution:** temporary authentication or network failures must
  not stop an existing local conversation.
- **Durable trace delivery:** clients retain unsent traces in an encrypted
  outbox; the gateway acknowledges a batch only after durable admission and
  delivers it asynchronously with stable idempotency.
- **Scoped access:** ordinary users can see only traces owned by their stable
  account ID, while administrators use a separate Langfuse account.

### What this repository owns

| Path | Responsibility |
|---|---|
| `services/trace-gateway/` | Trace-token validation, durable inbox, idempotent receipts, and asynchronous delivery |
| `deploy/voice-trace/` | Compose and edge-routing contracts for the Voice Trace stack |
| `scripts/` | Bootstrap, deployment, migration, health-check, and support utilities |
| `tests/` | Platform, routing, deployment, and protocol contract tests |
| `docs/` | Requirements, current status, runbooks, reports, and authoritative file routing |
| `components.lock.yaml` | Reviewed revisions of independently maintained component repositories |

The Desktop client, authentication service, Hermes reference runtime, and
NeMo Relay are maintained in separate repositories. They are pinned here for
integration review rather than vendored into this repository.

### Getting started

Clone the repository and run the platform contract suite:

```bash
git clone git@github.com:Ansatz-agent/ansatz-agent-platform.git
cd ansatz-agent-platform
bash tests/run.sh
```

Run the Trace Gateway unit and integration tests separately:

```bash
cd services/trace-gateway
go test ./...
```

Deployment is environment-specific and handles authentication credentials,
Langfuse keys, persistent storage, and existing workloads. Before operating a
stack, read the relevant runbook instead of treating the example environment
file as a production recipe.

### Documentation

- [Current progress and delivery boundaries](docs/02-progress.md)
- [Authoritative file and component index](docs/03-file-index.md)
- [Storage, authentication, and trace-access requirements](docs/requirements/2026-08-24-server-storage-auth-trace-access.md)
- [Storage, authentication, and personal-trace operations](docs/runbooks/storage-auth-personal-traces.md)
- [Offline trace delivery and recovery](docs/runbooks/trace-upload-continuity.md)
- [Latest continuity verification report](docs/reports/2026-08-25-auth-trace-continuity-e2e.md)

Project status changes independently from deployment status. Check the progress
document and its linked evidence before treating a feature as released or
running in production.

### Security

Never commit `.secrets/`, runtime state, authentication caches, trace payloads,
wrapped encryption keys, service environment files, or production credentials.
Diagnostics and evidence must remain payload-free and secret-free.

---

## 中文

Ansatz Agent Platform 是一个面向客户端/服务端 Agent 可观测系统的集成与运维仓库。
基于 Hermes 的客户端在本地执行 Agent 任务，通过统一账号服务认证，并将 Trace 上传到具备
持久化能力的 Gateway。Langfuse 负责保存和展示 Trace；平台同时隔离普通用户和管理员的
访问入口。

### 系统架构

```text
Hermes 客户端
  |-- 认证 --------------------------------------> 认证服务（/auth）
  |-- 本地加密 Trace outbox
          `--> Trace Gateway（/trace-ingest）
                   |-- 持久化 inbox 与 receipt
                   `--> Langfuse
                          |-- 个人 Dashboard（/traces）
                          `-- 管理员控制台（/langfuse）
```

平台围绕三个边界设计：

- **本地优先执行：**临时认证故障或网络中断不能阻止已有用户继续本地对话。
- **可靠 Trace 交付：**客户端用加密 outbox 保存未上传 Trace；Gateway 只有在持久接收后
  才确认批次，并通过稳定幂等语义异步投递。
- **分级访问：**普通用户只能查看稳定账号 ID 所属的 Trace；管理员使用独立的
  Langfuse 账号。

### 本仓库负责什么

| 路径 | 职责 |
|---|---|
| `services/trace-gateway/` | Trace token 校验、持久 inbox、幂等 receipt 和异步投递 |
| `deploy/voice-trace/` | Voice Trace 服务栈的 Compose 与边缘路由契约 |
| `scripts/` | 初始化、部署、迁移、健康检查和支持工具 |
| `tests/` | 平台、路由、部署与协议契约测试 |
| `docs/` | 需求、当前状态、运行手册、报告和权威文件索引 |
| `components.lock.yaml` | 独立组件仓库经过审阅的集成版本 |

Desktop 客户端、认证服务、Hermes 参考运行时和 NeMo Relay 均在独立仓库维护。
本仓库只固定其集成版本，不复制这些组件的源码。

### 快速开始

克隆仓库并运行平台契约测试：

```bash
git clone git@github.com:Ansatz-agent/ansatz-agent-platform.git
cd ansatz-agent-platform
bash tests/run.sh
```

单独运行 Trace Gateway 的单元测试和集成测试：

```bash
cd services/trace-gateway
go test ./...
```

部署流程依赖具体环境，并涉及认证凭据、Langfuse 密钥、持久存储和宿主机上的既有服务。
执行部署或运维操作前，请先阅读对应 runbook，不要把示例环境文件直接当作生产部署方案。

### 文档导航

- [当前进展与交付边界](docs/02-progress.md)
- [权威文件与组件索引](docs/03-file-index.md)
- [存储、认证与 Trace 分级访问需求](docs/requirements/2026-08-24-server-storage-auth-trace-access.md)
- [存储、认证与个人 Trace 运维手册](docs/runbooks/storage-auth-personal-traces.md)
- [Trace 离线补传与恢复手册](docs/runbooks/trace-upload-continuity.md)
- [最新连续性验证报告](docs/reports/2026-08-25-auth-trace-continuity-e2e.md)

代码状态和部署状态会独立变化。在把某项能力视为已经发布或运行于生产环境前，请核对
当前进展文档及其引用的验收证据。

### 安全说明

禁止提交 `.secrets/`、运行状态、认证缓存、Trace payload、封装后的加密密钥、服务环境
文件或生产凭据。诊断材料和验收证据不得包含 payload 或 secret。
