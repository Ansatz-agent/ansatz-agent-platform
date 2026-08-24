# 服务端存储、统一认证与 Trace 分级访问需求

状态：已批准
日期：2026-08-24

## 1. 目标

- 将 Hermes 上的 Podman 存储和平台持久化数据迁移到独立磁盘 `/data`。
- 下线旧 `/agent` 门户和认证接口，保留现有账号库并统一使用 `/auth`。
- 客户端直接请求 `/auth`，登录后继续默认上传真实对话的完整 Trace。
- 普通用户通过轻量 `/traces` Dashboard 仅查看自己的数据；管理员通过 `/langfuse` 查看全部 Trace。

## 2. 功能需求

### 2.1 存储迁移

- Podman 镜像、层、构建缓存、CNI 状态及平台持久化数据物理迁移到 `/data`；为兼容当前 Podman 3.3.1，可保留 `/var/lib/containers/storage` 逻辑路径，但必须用 `findmnt` 证明其物理来源为 `/data`。
- PostgreSQL、ClickHouse、Redis、MinIO 和认证 SQLite 数据完整保留。
- 迁移提供预检、停机切换、校验、回滚和迁移后磁盘检查。

### 2.2 统一认证

- 保留现有账号库，不要求用户重新注册。
- `/agent` 页面和认证接口全部下线，不保留兼容代理。
- 登录、登出、会话状态和 Trace 上传令牌统一位于 `/auth`。
- 认证身份包含不可变用户标识 `sub`、用户名和角色。
- 仍使用 `/agent` 的客户端必须升级；更新后的客户端只请求 `/auth`。

### 2.3 Trace 身份与权限

- Trace Gateway 验证上传令牌，以认证服务返回的 `sub` 覆盖客户端身份并写入 Langfuse `userId`。
- 用户名写入 Trace metadata 供展示，不能作为授权主键。
- 普通用户请求中的 `userId` 不可信，Dashboard 后端必须强制使用当前登录用户的 `sub`。
- Langfuse Project API Key 只保存在服务端，不下发给浏览器或客户端。

### 2.4 轻量 Dashboard

- `/traces` 使用独立轻量页面，不向普通用户开放 Langfuse 项目界面。
- 页面参考 [NVIDIA Tokenomics Dashboard](https://dashboard.aitoolsanalytics.nvidia.com/) 的信息层级：顶部导航、日期范围、可收起筛选区、概览卡、趋势图、分布卡片和明细表。
- 首屏显示会话数、Trace 数、Token、Cost 和活跃天数。
- 支持个人会话检索、时间范围筛选、会话内 Trace 时间线、完整输入/输出、工具调用和 Trace 详情。
- 使用 Ansatz 品牌，不复制 NVIDIA 商标、Logo、专有文案或内部数据。

## 3. 权限模型

| 角色 | 入口 | 数据范围 |
|---|---|---|
| 普通用户 | `/traces` | 仅 `userId = 当前登录用户 sub` |
| 平台管理员 | 使用独立 Langfuse 管理员账号登录 `/langfuse` | 项目接收的全部 Trace |

管理员身份与普通用户的 `/auth` 账号体系保持分离；本阶段不做 SSO，也不向普通用户授予 Langfuse 项目成员或 API Key。

## 4. 验收标准

- 根盘不再承载 Podman graphroot 和平台主要持久化数据，`/data` 成为实际读写位置。
- 迁移前后账号与历史 Trace 数据一致，当前服务和 HTTPS 路由恢复健康。
- `/agent` 返回 404；客户端通过 `/auth` 登录、取上传令牌并上传真实 Trace。
- 两个普通用户不能互相查看 Trace，修改 URL 或参数也不能越权。
- 普通用户能查看完整 Trace、Token 和 Cost；管理员能在 `/langfuse` 查看全部用户数据。
