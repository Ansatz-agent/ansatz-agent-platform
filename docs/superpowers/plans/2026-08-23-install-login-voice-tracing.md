# Install-First Login, Voice Trace, and Admin Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship an independently named offline Hermes Desktop that installs its complete runtime before login, automatically uploads authenticated full Voice/text Traces through a secure Gateway, and lets one dedicated administrator inspect them in public Langfuse.

**Architecture:** The Desktop keeps the existing c2sml account bridge but changes the packaged startup order and adds an Electron-owned loopback Trace forwarder that refreshes opaque upload tokens without exposing them to the renderer or Relay. A containerized Django auth service owns token issuance/introspection/revocation; a Go Gateway validates OTLP protobuf, overwrites canonical identity, and forwards with server-only Langfuse credentials. Langfuse, Gateway, and auth remain separate services behind Nginx Proxy Manager.

**Tech Stack:** Electron 40, React 19, TypeScript 6, Node 26, Python 3.11, Django, NeMo Relay 0.7.1, Go 1.24+, OpenTelemetry OTLP protobuf, Langfuse v4, Podman Compose, Nginx Proxy Manager.

**Spec:** `ansatz-agent-platform/docs/superpowers/specs/2026-08-23-install-login-voice-tracing-design.md`

## Global Constraints

- Client base is exactly `5d0e5488d1ecda66ba952c69e45482bbbc057296` from `origin/integration/main-auth-voice-base`.
- Client branch is exactly `feature/install-login-voice-trace` in the existing isolated worktree.
- Product identity is `Ansatz Voice Trace Client` with app ID `cn.c2sml.ansatz.voice-trace-client` and scheme `ansatz-voice-trace`.
- Product runtime roots are `~/.ansatz-voice-trace-client` and `%LOCALAPPDATA%\AnsatzVoiceTraceClient`; existing Hermes roots are never adopted.
- Upload endpoint is `POST https://api.c2sml.cn/v1/traces`; admin UI is `https://trace.c2sml.cn`.
- Upload tokens are opaque, upload-only, introspected, and expire after 900 seconds.
- Gateway request limit is 8 MiB; queue limit is 128 batches or 32 MiB; item lifetime is 15 minutes; shutdown flush is 3 seconds.
- Full user/model/tool semantics are uploaded; credentials and raw audio are not.
- Trace upload cannot be disabled through normal UI, config, or environment input in this product.
- No Langfuse key, administrator password, fixed upload token, or service secret enters the client, package, Git, logs, report, or chat.
- macOS and Windows artifacts come from the same clean commit through the official repository-local scripts and Electron Builder hooks; GitHub Actions is not used.
- Python tests use `bash scripts/run_tests.sh`; the local `dl` environment must not be modified. Until an approved pytest-capable interpreter exists, Python RED/GREEN evidence runs in a task-scoped container or existing isolated remote test runtime.
- Shell scripts are invoked with `bash`; local SSH/SCP/rsync/checksum operations use `rtk proxy`.
- No destructive Git command, volume deletion, global image prune, unrelated container mutation, or overwrite of existing Downloads artifacts.

---

### Task 1: Preserve source custody and create isolated server workspaces

**Files:**
- Create: `agent-langfuse-server/auth-service/` from the sanitized remote source snapshot
- Modify: `ansatz-agent-platform/components.lock.yaml`
- Create: `ansatz-agent-platform/docs/evidence/2026-08-23-auth-source-provenance.json`

**Interfaces:**
- Consumes: remote `/opt/agent-history-portal` at commit `8b1b1bfaf8b2ab702b9f28abee90d1bf6bb246e0`.
- Produces: a secret-free source tree, archive hash, original file manifest, and `agent-langfuse-server` branch `feature/auth-trace-ingest`.

- [ ] **Step 1: Re-verify the remote snapshot without reading secrets**

Run:

```bash
rtk proxy ssh hermes 'cd /opt/agent-history-portal && git rev-parse HEAD && git branch --show-current && git status --short --branch && git config --get remote.origin.url || true'
```

Expected: commit `8b1b1bf...`, branch `feature/token-usage-dashboard`, clean status, and no remote URL.

- [ ] **Step 2: Create a sanitized archive and file manifest on the remote host**

Use a task-scoped directory under `/root/ansatz-agent/auth-source-20260823`, exclude `.env`, databases, media/uploads, logs, caches, certificates, private keys, `.git`, and owner state. Run the archive command through `rtk proxy ssh`; do not print file contents.

Expected: archive plus SHA-256 and a sorted relative-path manifest containing only source, templates, migrations, tests, docs, and deployment declarations.

- [ ] **Step 3: Transfer with RTK and verify the same hash locally**

Run `rtk proxy scp` into `ansatz-agent-platform/tmp/auth-source-20260823/`, then `rtk proxy shasum -a 256` on the archive. Extract only after validating every member is relative, non-link, and outside excluded categories.

Expected: local and remote hashes match; secret-name scan is empty.

- [ ] **Step 4: Create an isolated Langfuse worktree and server branch**

Run:

```bash
rtk git worktree add /Users/yuxiaoy/Projects/Ansatz-agent/tmp/agent-langfuse-auth-trace \
  -b feature/auth-trace-ingest main
```

Expected: clean worktree on `feature/auth-trace-ingest`.

- [ ] **Step 5: Import the sanitized tree and provenance record**

Place the files at `auth-service/` without the remote `.git`. Record original branch/commit/path, absent remote, archive hash, excluded patterns, and imported file count. Add the same source pin to `components.lock.yaml`.

- [ ] **Step 6: Verify custody invariants**

Run secret-name/content scans, `git diff --check`, and confirm `find auth-service -name .git` is empty.

Expected: no secret paths, nested repository, database, user content, or symlink.

- [ ] **Step 7: Commit only custody files**

Commit the imported service on `feature/auth-trace-ingest`; commit only the lock/evidence records in the platform repository using `Seauagain <1580065969@qq.com>`.

---

### Task 2: Make the product identity and runtime root independent

**Files:**
- Create: `apps/desktop/electron/ansatz-product.ts`
- Create: `apps/desktop/electron/ansatz-product.test.ts`
- Modify: `apps/desktop/package.json`
- Modify: `apps/desktop/electron/main.ts`
- Modify: `apps/desktop/electron/desktop-uninstall.ts`
- Modify: `apps/desktop/electron/desktop-uninstall.test.ts`
- Modify: `scripts/build-desktop-dmg.sh`
- Modify: `scripts/desktop-dmg-contract.mjs`
- Modify: `scripts/desktop-dmg-contract.test.mjs`
- Modify: `scripts/desktop-windows-contract.mjs`
- Modify: `scripts/desktop-windows-contract.test.mjs`

**Interfaces:**
- Produces: `ANSATZ_PRODUCT` constants and `resolveAnsatzRuntimeRoot(platform, home, localAppData)`.
- Consumes: the constants in Electron startup, uninstall, and packaging audits.

- [ ] **Step 1: Write failing identity behavior tests**

Add tests that assert:

```ts
assert.equal(ANSATZ_PRODUCT.productName, 'Ansatz Voice Trace Client')
assert.equal(ANSATZ_PRODUCT.appId, 'cn.c2sml.ansatz.voice-trace-client')
assert.equal(resolveAnsatzRuntimeRoot('darwin', '/Users/a', ''), '/Users/a/.ansatz-voice-trace-client')
assert.equal(resolveAnsatzRuntimeRoot('win32', 'C:\\Users\\a', 'C:\\Users\\a\\AppData\\Local'),
  'C:\\Users\\a\\AppData\\Local\\AnsatzVoiceTraceClient')
assert.notEqual(resolveAnsatzRuntimeRoot('darwin', '/Users/a', ''), '/Users/a/.hermes')
```

Extend uninstall tests so only `Ansatz Voice Trace Client.app` and `AnsatzVoiceTraceClient.exe` resolve as removable product paths; `Hermes.app` and a `Hermes` install directory return null.

- [ ] **Step 2: Run RED**

Run:

```bash
npx vitest run --project electron electron/ansatz-product.test.ts electron/desktop-uninstall.test.ts
npm run test:desktop:windows-contract
```

Expected: fail because the new module/identity does not exist and current contracts require Hermes names.

- [ ] **Step 3: Implement the minimum product identity**

Create a frozen constant object and pure runtime-root resolver. Update Electron Builder fields:

```json
{
  "name": "ansatz-voice-trace-client",
  "productName": "Ansatz Voice Trace Client",
  "build": {
    "appId": "cn.c2sml.ansatz.voice-trace-client",
    "productName": "Ansatz Voice Trace Client",
    "executableName": "AnsatzVoiceTraceClient",
    "artifactName": "Ansatz-Voice-Trace-Client-${version}-${os}-${arch}.${ext}"
  }
}
```

Change protocol, DMG title, macOS bundle display/executable/name, Windows shortcut/install/uninstall identity, and updater channel consistently. Make `main.ts` use only the product runtime root by default; retain `HERMES_HOME` solely for explicit developer/test overrides.

- [ ] **Step 4: Update official build/audit paths**

Change the official scripts' expected packaged app, DMG, Windows executable, and artifact patterns. Do not change their dependency preparation, payload builders, Electron Builder invocation, or audit sequence.

- [ ] **Step 5: Run GREEN and regression checks**

Run the RED commands plus:

```bash
npm run typecheck --workspace apps/desktop
npx vitest run --project electron electron/desktop-installation.test.ts
```

Expected: all pass with no old product path accepted as the new product.

- [ ] **Step 6: Commit the client identity change**

Commit on `feature/install-login-voice-trace` as `feat(desktop): isolate Ansatz product identity`.

---

### Task 3: Prepare the complete packaged runtime before login

**Files:**
- Create: `apps/desktop/electron/install-first-runtime.ts`
- Create: `apps/desktop/electron/install-first-runtime.test.ts`
- Modify: `apps/desktop/electron/main.ts`
- Modify: `apps/desktop/electron/desktop-runtime-gate.ts`
- Modify: `apps/desktop/electron/desktop-runtime-gate.test.ts`
- Modify: `apps/desktop/src/components/auth-gate.tsx`
- Modify: `apps/desktop/src/components/auth-gate.test.tsx`
- Modify: `apps/desktop/src/i18n/en.ts`
- Modify: `apps/desktop/src/i18n/zh.ts`
- Modify: `apps/desktop/src/i18n/zh-hant.ts`
- Modify: `apps/desktop/src/i18n/ja.ts`
- Modify: `apps/desktop/src/i18n/types.ts`

**Interfaces:**
- Produces: `prepareInstallFirstRuntime({ resolveBackend, ensureRuntime, runtimeGate })`.
- Invariant: `startDesktopAuthRuntime()` starts the auth bridge only after that promise resolves.

- [ ] **Step 1: Write failing startup-order tests**

Tests record calls and require this exact order:

```ts
assert.deepEqual(events, [
  'resolve-complete-runtime',
  'ensure-runtime:runtime',
  'mark-runtime-ready',
  'start-auth-bridge'
])
```

Add cases for an already usable product runtime, failed preparation, retry, and logout preserving `runtimeGate.ready`. Renderer tests require login form absence while full runtime bootstrap is active and presence immediately after completion, before account authentication.

- [ ] **Step 2: Run RED**

Run:

```bash
npx vitest run --project electron electron/install-first-runtime.test.ts electron/desktop-runtime-gate.test.ts
npx vitest run --project ui src/components/auth-gate.test.tsx
```

Expected: current code prepares only auth scope pre-login and invalidates full runtime readiness on logout.

- [ ] **Step 3: Implement install-first orchestration**

Make fresh packaged startup call `ensureRuntime(candidate, {scope: 'runtime'})`. Remove post-login dependency installation from `prepareAuthenticatedDesktopRuntime`; login now enables capabilities and starts the backend against the already ready runtime. Logout invalidates the auth epoch and backend but not the installed runtime readiness.

AuthGate consumes sanitized bootstrap state and shows full runtime preparation before rendering credential fields. Delete the old authenticated “preparing full runtime” branch. Add the mandatory full-Trace notice under the fixed account-server block.

- [ ] **Step 4: Run GREEN and broad auth regression**

Run the RED commands plus:

```bash
npx vitest run --project electron electron/auth-coordinator.test.ts electron/task4-auth-runtime.contract.test.ts
npx vitest run --project ui src/components/task4-auth-ui.contract.test.tsx src/i18n/auth-catalog.test.ts
```

Expected: install-first order, hard gate, password clearing, and translated catalogs pass.

- [ ] **Step 5: Commit**

Commit as `feat(desktop): install complete runtime before login`.

---

### Task 4: Extend the account bridge with Trace upload credentials

**Files:**
- Modify: `hermes_cli/client_auth/client.py`
- Modify: `hermes_cli/client_auth/runtime.py`
- Modify: `hermes_cli/client_auth/bridge.py`
- Modify: `tests/hermes_cli/client_auth/test_client.py`
- Modify: `tests/hermes_cli/client_auth/test_runtime.py`
- Modify: `tests/hermes_cli/client_auth/test_bridge.py`
- Modify: `apps/desktop/electron/auth-bridge.ts`
- Modify: `apps/desktop/electron/auth-bridge.test.ts`

**Interfaces:**
- Python produces `TraceCredential(access_token: str, expires_at: str, expires_in: int, installation_id: str)`.
- Bridge method: `trace_token({installation_id, client_version, telemetry_schema_version})`.
- TypeScript produces `DesktopAuthBridge.traceToken(request): Promise<TraceCredential>` with strict response validation.

- [ ] **Step 1: Read the test rules before editing tests**

Read `ansatz-agent-platform/docs/superpowers/test-driven-development/writing-good-tests.md` completely.

- [ ] **Step 2: Write failing Python client and bridge tests**

Use the existing `httpx.MockTransport` fixtures to assert the issue route, session/CSRF headers, exact response keys, `Cache-Control: no-store`, field bounds, and rejection of malformed/extra fields. Bridge tests assert token output is allowed only for an authenticated owner and never appears in error output.

- [ ] **Step 3: Run Python RED in the isolated pytest-capable runtime**

Run through `bash scripts/run_tests.sh` with `HERMES_PYTHON` set to the verified remote/task interpreter. Expected failures name missing `AuthClient.trace_token`, `account_trace_token`, and bridge method.

- [ ] **Step 4: Write failing TypeScript protocol tests**

Add tests for valid credential, malformed token, stale/extra fields, timeout, child exit, and diagnostic redaction.

Run:

```bash
npx vitest run --project electron electron/auth-bridge.test.ts
```

Expected: fail because `traceToken` and response type do not exist.

- [ ] **Step 5: Implement minimal bridge behavior**

Add fixed `TRACE_TOKEN_PATH = /agent/api/trace-token/`, validate UUID/version/schema, use current cookies and CSRF, and return the exact response. The runtime owner keeps no long-term token; Electron owns the returned token. Extend bridge dispatch with strict allowed params and a credential-specific validator/generic request return type.

- [ ] **Step 6: Run GREEN**

Run both Python and TypeScript commands. Expected: all pass and secret sentinel scans remain empty.

- [ ] **Step 7: Commit**

Commit as `feat(auth): issue desktop trace credentials`.

---

### Task 5: Add the Electron-owned loopback Trace forwarder

**Files:**
- Create: `apps/desktop/electron/trace-forwarder.ts`
- Create: `apps/desktop/electron/trace-forwarder.test.ts`
- Create: `apps/desktop/electron/trace-forwarder-queue.ts`
- Create: `apps/desktop/electron/trace-forwarder-queue.test.ts`
- Modify: `apps/desktop/electron/main.ts`
- Modify: `apps/desktop/electron/backend-env.ts`
- Modify: `apps/desktop/electron/backend-env.test.ts`
- Modify: `tests/tools/test_local_env_blocklist.py`

**Interfaces:**
- `TraceCredentialProvider.current(): Promise<TraceCredential>` refreshes at `expires_at - 60s`.
- `TraceForwarder.start(epoch): Promise<{endpoint: string; localBearer: string}>`.
- `TraceForwarder.stop({flushMs: 3000}): Promise<TraceForwarderSummary>`.
- Forwarded request preserves protobuf bytes and adds public bearer plus required canonical headers.

- [ ] **Step 1: Write failing queue tests**

Use fake clocks and transports to assert FIFO, 128/32 MiB limits, oldest-drop, retry schedule `1/2/4/8/16/30s`, 15-minute expiry, identical byte reuse, no concurrent duplicate send, and auth-epoch discard.

- [ ] **Step 2: Write failing HTTP boundary tests**

Bind the real server to loopback port zero. Assert non-loopback simulation/invalid local bearer/media type/encoding/oversize/stale epoch are rejected; valid protobuf bytes reach the fake upstream once; one upstream 401 causes one credential refresh and one identical resend.

- [ ] **Step 3: Run RED**

Run:

```bash
npx vitest run --project electron electron/trace-forwarder-queue.test.ts electron/trace-forwarder.test.ts electron/backend-env.test.ts
```

Expected: new modules absent.

- [ ] **Step 4: Implement queue and forwarder**

Use `node:http`, `crypto.randomBytes(32)`, `fetch`, monotonic timers, bounded buffers, and `AbortController`. Never log request headers/body/token. Return only counters and safe reason codes.

Set child-only environment:

```text
HERMES_NEMO_RELAY_PLUGINS_TOML=<product file>
ANSATZ_TRACE_LOCAL_ENDPOINT=http://127.0.0.1:<port>/v1/traces
ANSATZ_TRACE_LOCAL_AUTHORIZATION=Bearer <local epoch token>
ANSATZ_TRACE_INSTALLATION_ID=<uuid>
ANSATZ_TRACE_ENTRYPOINT=desktop
```

Add the new secret names to Hermes subprocess stripping tests.

- [ ] **Step 5: Wire auth lifecycle**

After login, start/refresh the forwarder before backend spawn. Logout first closes admission, flushes for 3 seconds, clears credentials/queue, then stops the backend. Stale completion callbacks compare runtime instance and auth epoch.

- [ ] **Step 6: Run GREEN and auth regression**

Run RED plus auth coordinator, backend start, and scope-token suites. Run the Python blocklist test through the isolated interpreter.

- [ ] **Step 7: Commit**

Commit as `feat(desktop): forward authenticated trace batches`.

---

### Task 6: Force the product-owned NeMo Relay lifecycle and privacy policy

**Files:**
- Create: `config/ansatz-voice-trace/plugins.toml`
- Create: `agent/ansatz_trace_policy.py`
- Create: `tests/agent/test_ansatz_trace_policy.py`
- Modify: `plugins/observability/nemo_relay/__init__.py`
- Modify: `tests/plugins/test_nemo_relay_plugin.py`
- Modify: `agent/relay_runtime.py`
- Modify: `agent/relay_llm.py`
- Modify: `agent/relay_tools.py`
- Modify: `apps/desktop/scripts/build-backend-payload.mjs`
- Modify: `apps/desktop/scripts/build-backend-payload.test.mjs`

**Interfaces:**
- `ansatz_product_trace_enabled()` is true only with the signed product runtime marker and valid local forwarder environment.
- `redact_trace_value(value, key_path)` removes credentials and raw audio while retaining semantic text/tool data.
- Relay config uses local endpoint/auth header env and schema version 1.

- [ ] **Step 1: Write failing policy tests**

Assert complete prompts/replies/tool args/results survive; password/cookie/authorization/API-key/private-key/audio bytes are replaced; redaction is recursive and bounded. Assert no supported false/disable env overrides forced product mode.

- [ ] **Step 2: Write failing Relay activation tests**

Create a signed product marker fixture and assert the bundled plugin becomes enabled automatically only in that product runtime, uses one coordinator for Desktop and Voice, and configures the loopback exporter once.

- [ ] **Step 3: Run RED through `scripts/run_tests.sh`**

Expected: policy module and forced product mode absent.

- [ ] **Step 4: Implement minimum policy and product activation**

Keep ordinary Hermes opt-in behavior unchanged. Product mode validates the marker and fixed config path, ignores user disable lists for only this bundled plugin, and rejects alternate endpoints. Add redaction at request/result serialization boundaries without mutating conversation history.

- [ ] **Step 5: Include config in offline payload**

Extend the existing backend payload allowlist/manifest so the product config is archived and hash-verified. Do not add external downloads.

- [ ] **Step 6: Run GREEN and duplicate-call regression**

Run targeted Relay/session/LLM/tool/Voice tests plus payload builder tests. Assertions count physical callbacks and logical observations rather than source patterns.

- [ ] **Step 7: Commit**

Commit as `feat(relay): enforce full product trace export`.

---

### Task 7: Implement upload-token issuance, introspection, and revocation in Django

**Files (server worktree):**
- Create: `auth-service/history/trace_tokens.py`
- Create: `auth-service/history/migrations/0005_trace_upload_token.py`
- Create: `auth-service/history/tests/test_trace_tokens.py`
- Modify: `auth-service/history/models.py`
- Modify: `auth-service/history/auth_views.py`
- Modify: `auth-service/history/urls.py`
- Modify: `auth-service/config/settings.py`
- Modify: `auth-service/.env.example`

**Interfaces:**
- Model `TraceUploadToken`: digest, token_id, user, session_key_digest, installation_id, scope, audience, created_at, expires_at, revoked_at.
- Public issue view and private introspection view match spec section 9.
- Logout revokes current-session tokens before session deletion.

- [ ] **Step 1: Write failing Django behavior tests**

Test anonymous/CSRF rejection, issue/rotation, exact 900-second expiry, digest-only storage, no-store response, inactive cases, internal credential check, disabled user, logout, and device revoke. Use sentinel secrets and assert response/log capture excludes them.

- [ ] **Step 2: Build the unmodified auth container and run RED tests**

Build from the fixed snapshot in a task-scoped image and run `python manage.py test history.tests.test_trace_tokens`. Expected: missing model/routes.

- [ ] **Step 3: Implement the model and migration**

Use `secrets.token_urlsafe(32)`, SHA-256 digests, `timezone.now()`, UUID validation, constant-time service-secret comparison, and additive indexes for digest/expiry/user-installation. Never store raw token.

- [ ] **Step 4: Implement issue/introspection/logout behavior**

Public issue uses Django session+CSRF and active user. Internal view returns a uniform inactive response and is not routed through Nginx public locations. Logout marks current-session rows revoked before calling Django logout.

- [ ] **Step 5: Run GREEN and all auth regressions**

Run the new suite plus existing admin auth, settings security, dashboard, import, and deployment tests in the container.

- [ ] **Step 6: Commit**

Commit on `feature/auth-trace-ingest` as `feat(auth): issue revocable trace upload tokens`.

---

### Task 8: Implement the Go Trace Gateway with canonical OTLP identity

**Files:**
- Create: `ansatz-agent-platform/services/trace-gateway/go.mod`
- Create: `ansatz-agent-platform/services/trace-gateway/go.sum`
- Create: `ansatz-agent-platform/services/trace-gateway/cmd/gateway/main.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/auth/introspector.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/auth/introspector_test.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/otlp/canonicalize.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/otlp/canonicalize_test.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/server/server.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/server/server_test.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/redact/redact.go`
- Create: `ansatz-agent-platform/services/trace-gateway/internal/redact/redact_test.go`
- Create: `ansatz-agent-platform/services/trace-gateway/Containerfile`

**Interfaces:**
- `Introspector.Introspect(ctx, bearer) (Principal, error)`.
- `Canonicalize(req, principal, headers, gatewayRequestID) error`.
- `Server.Handler()` exposes only `/healthz` and `/v1/traces`.

- [ ] **Step 1: Write failing introspection tests**

Use an `httptest.Server`; verify service header, timeout, exact JSON shape, inactive mapping, no token in errors, and no caching beyond expiry.

- [ ] **Step 2: Write failing protobuf canonicalization tests**

Construct real OTLP requests with forged canonical keys at resource/scope/span levels. Assert all forged values disappear and exactly one trusted resource set remains; validated correlation headers and Gateway ID are present; raw audio and credential-shaped values are removed.

- [ ] **Step 3: Write failing HTTP contract tests**

Cover 400/401/413/415/429/502/503, valid forward, Basic Langfuse header only upstream, no-store errors, 8 MiB boundary, identity encoding, identical retry digest, and log redaction.

- [ ] **Step 4: Run RED**

Run:

```bash
go test ./...
```

Expected: packages/functions absent.

- [ ] **Step 5: Implement minimal Gateway**

Use standard `net/http`, `http.MaxBytesReader`, bounded rate buckets, `proto.Unmarshal/Marshal`, and OpenTelemetry generated types. Basic Langfuse authorization is constructed at startup from server-only environment values. Logs contain request ID/status/size/duration only.

- [ ] **Step 6: Run GREEN, race, and vet**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Expected: zero failures/races/vet errors.

- [ ] **Step 7: Container build and non-root check**

Build a pinned multi-stage image, run health and upload contract tests against it, and inspect that it runs non-root with no shell/package manager in the runtime layer if the chosen base permits.

- [ ] **Step 8: Commit exact Gateway files**

Commit in the platform repository as `feat: add authenticated OTLP trace gateway` without staging unrelated existing files.

---

### Task 9: Add controlled Compose, secrets, and reverse-proxy contracts

**Files:**
- Create: `ansatz-agent-platform/deploy/voice-trace/docker-compose.yml`
- Create: `ansatz-agent-platform/deploy/voice-trace/.env.example`
- Create: `ansatz-agent-platform/deploy/voice-trace/nginx/api.c2sml.cn.conf`
- Create: `ansatz-agent-platform/deploy/voice-trace/nginx/trace.c2sml.cn.conf`
- Create: `ansatz-agent-platform/tests/test_voice_trace_compose_contract.py`
- Create: `ansatz-agent-platform/tests/test_voice_trace_proxy_contract.py`
- Create: `ansatz-agent-platform/scripts/bootstrap-voice-trace-secrets.sh`
- Create: `ansatz-agent-platform/tests/test-bootstrap-voice-trace-secrets.sh`

**Interfaces:**
- Compose services: `auth-service`, `trace-gateway`, existing/pinned Langfuse Web/Worker and data services.
- Public host binding: none except NPM; service host binds are loopback only where NPM cannot join the network.

- [ ] **Step 1: Write failing Compose/proxy/secret contract tests**

Assert separate credentials, no literal defaults, no `latest`, only expected loopback binds, no data ports, correct `NEXTAUTH_URL`, disabled signup, private introspection route, Authorization passed only to Gateway, POST caching off, 8 MiB/15-second limits, and HTTP-to-HTTPS.

- [ ] **Step 2: Run RED**

Run platform `bash tests/run.sh`. Expected: new deployment files absent.

- [ ] **Step 3: Implement Compose and examples**

Reuse current pinned Langfuse/data image variables and volumes. Add Gateway/auth images and networks with least connectivity. `.env.example` contains names only and explicit generation instructions.

- [ ] **Step 4: Implement owner-only secret bootstrap**

Generate service secret, Langfuse project credentials, NextAuth secrets, DB/data secrets, and dedicated admin password via OpenSSL/OS randomness into `.secrets/voice-trace/` with `umask 077`. Never print values.

- [ ] **Step 5: Run GREEN and secret scan**

Run all platform tests, Compose config rendering with redacted fixture values, and repository secret-pattern scans.

- [ ] **Step 6: Commit exact deployment/test files**

Commit as `feat: deploy voice trace gateway and Langfuse`.

---

### Task 10: Deploy private services and validate multi-user synthetic ingestion

**Files:**
- Create: `ansatz-agent-platform/scripts/deploy-voice-trace.sh`
- Create: `ansatz-agent-platform/scripts/check-voice-trace.sh`
- Create: `ansatz-agent-platform/scripts/send-gateway-trace.py`
- Create: `ansatz-agent-platform/scripts/verify-voice-trace-e2e.py`
- Create: corresponding platform shell/Python contract tests

**Interfaces:**
- Deployment root: `/root/ansatz-agent/voice-trace-20260823` on `hermes`.
- Exact project name: `ansatz-voice-trace-20260823`.

- [ ] **Step 1: Write failing script contract tests**

Use fake `rtk`, SSH, container, and HTTP commands. Assert exact host/project/root, no secret echo, no global cleanup, health-before-ingest, and safe idempotent rerun.

- [ ] **Step 2: Run RED then implement scripts**

Follow platform shell patterns and always invoke nested scripts with `bash`. Run tests to GREEN before remote mutation.

- [ ] **Step 3: Back up auth database and inventory current admins**

Create owner-only, timestamped, hash-recorded backups. Query only usernames/emails/enabled/admin flags; do not read password hashes or sessions.

- [ ] **Step 4: Build and deploy auth/Gateway images**

Use L40S only if build resources require it; verify actual host/hardware first. Transfer by hash, import on Hermes, run migrations, and start the exact Compose project without touching the existing source Langfuse project until cutover is proven.

- [ ] **Step 5: Test two synthetic users/installations**

Create two non-admin test users through a controlled Django management command with passwords delivered only via owner files. Issue tokens through real sessions, upload real OTLP protobuf containing forged identities, and query Langfuse to prove canonical separation and full semantic content.

- [ ] **Step 6: Test revocation, retry, and restart persistence**

Revoke/logout/disable cases must return 401; identical retry must not duplicate spans; restart the exact project without volumes and prove both Traces remain.

- [ ] **Step 7: Record non-secret evidence**

Store image IDs, commits, trace IDs, non-sensitive user/install IDs, HTTP statuses, and restart query counts.

---

### Task 11: Create the sole dedicated Langfuse administrator

**Files:**
- Create: local `.secrets/voice-trace/langfuse-admin-20260823.txt` (ignored, mode 600)
- Create: remote `/root/ansatz-agent/voice-trace-20260823/secrets/langfuse-admin.txt` (mode 600)
- Create: `ansatz-agent-platform/docs/evidence/2026-08-23-langfuse-admin.json` without password/session

**Interfaces:**
- Username/email starts with `trace-admin-20260823` and is unique.
- Exactly one enabled Langfuse administrator at acceptance.

- [ ] **Step 1: Inventory and back up before mutation**

Record only enabled/admin identity fields and database backup hash. If another admin is enabled, create a reversible enabled-state record.

- [ ] **Step 2: Generate credential without stdout/argv exposure**

Generate at least 32 random bytes inside an owner-only script/file boundary. Create or rotate the dedicated identity through supported initialization/API/database operations without passing the password as a command argument.

- [ ] **Step 3: Reversibly disable other enabled Langfuse users**

Do not delete rows. Record before/after IDs and verify ordinary c2sml test users have no Langfuse identity.

- [ ] **Step 4: Verify exact-one query and file modes**

Expected: one enabled dedicated administrator; both secret files are owner-only; Git/status/log/Trace scans contain no password.

- [ ] **Step 5: Verify real browser login**

After HTTPS exists, use the dedicated account in a real browser session. Evidence records URL, account email, timestamp, and resulting authorized page without cookies/password.

---

### Task 12: Provision exact public DNS/TLS routes

**Files:**
- Modify only external DNS records for `api.c2sml.cn` and `trace.c2sml.cn`
- Add exact NPM proxy hosts using the tested configuration
- Update: `ansatz-agent-platform/docs/evidence/2026-08-23-public-https.json`

**Interfaces:**
- Both records resolve to the existing c2sml public host.
- Separate valid certificates cover each hostname.

- [ ] **Step 1: Recheck DNS and certificates**

Use independent external resolution and `rtk proxy curl`; expected current RED is DNS failure.

- [ ] **Step 2: Create only the two approved DNS records**

Use available registrar credentials/UI if accessible. Do not change root, mail, or unrelated records. If no registrar authority is available, retain the exact prepared values and continue all non-public work; this is the only external infrastructure blocker.

- [ ] **Step 3: Add NPM hosts and request certificates**

Route API to Gateway and Trace to Langfuse; preserve the current root-domain host. Apply tested advanced settings, then reload Nginx and inspect exact config.

- [ ] **Step 4: Verify external HTTPS behavior**

Assert valid hostname/date/chain, HTTP redirect, unauthenticated Dashboard redirect, unauthenticated API 401, POST no-cache, and no direct data-service reachability.

- [ ] **Step 5: Record evidence without secrets**

Store resolved addresses, certificate SAN/expiry/issuer, status codes, and proxy upstream names only.

---

### Task 13: Complete packaging identity audits and official local builds

**Files:**
- Modify/add only packaging audit tests/scripts in the client branch as required by observed failures
- Produce: `apps/desktop/release/Ansatz-Voice-Trace-Client-0.17.0-mac-arm64-<commit>.dmg`
- Produce: `apps/desktop/release/Ansatz-Voice-Trace-Client-0.17.0-win-x64-<commit>.exe`

**Interfaces:**
- Both artifacts use one clean client commit.
- Official entry points remain root `build:desktop:dmg` and `build:desktop:windows`.

- [ ] **Step 1: Run complete client verification before commit**

Run Desktop typecheck, lint, full UI/electron tests, Windows contracts, relevant Python/Relay suites, payload tests, audit, and secret scans. Fix failures through RED/GREEN cycles.

- [ ] **Step 2: Commit the final client source**

Verify clean worktree and record full commit. Do not build from dirty tracked files.

- [ ] **Step 3: Re-audit collisions**

List `/Applications`, `~/Applications`, Downloads, Electron config, bundle IDs, runtime/userData paths, protocols, update channel, NSIS IDs, shortcuts, and uninstall locations. Fail if any final destination already exists.

- [ ] **Step 4: Build macOS through the official local pipeline**

Run:

```bash
npm run build:desktop:dmg
```

Expected: official prepare payload -> build -> Electron Builder DMG -> contract audit, with exit 0. Do not use GitHub Actions, Pake, or manual DMG assembly.

- [ ] **Step 5: Audit Mac artifact**

Verify artifact provenance, app/bundle name/ID, arm64 executables and embedded Python/Relay, no external runtime source download requirement, signature/notarization/Gatekeeper state, and absence of secrets.

- [ ] **Step 6: Build Windows x64 through the official local pipeline**

Run:

```bash
npm run build:desktop:windows
```

Expected: official locked portable prerequisites -> offline payload -> Electron Builder NSIS -> Windows audit, with exit 0.

- [ ] **Step 7: Audit Windows artifact**

Run Windows contract tests, payload manifest audit, PE machine check on packaged application executable, NSIS name/uninstall identity checks, and secret scan. State that native install/login/conversation/uninstall remains unverified if no Windows host exists.

- [ ] **Step 8: Copy without overwrite and calculate hashes**

Append the 12-character source commit to final names, verify each target does not exist, copy via `rtk proxy cp`/approved `cp`, then run `rtk proxy shasum -a 256`. Record absolute path, byte size, SHA-256, commit, architecture, and signature status.

---

### Task 14: Install the Mac package and run real packaged-app E2E

**Files:**
- Create: `ansatz-agent-platform/docs/reports/2026-08-23-install-login-voice-tracing-e2e.md`
- Create: secret-free evidence under `ansatz-agent-platform/docs/evidence/install-login-voice-tracing/`

**Interfaces:**
- Application under test is the new installed app, not source/dev/existing Hermes.
- Trace is queried through the public Dashboard by the dedicated admin.

- [ ] **Step 1: Record pre-install state**

Capture existing app/package names, hashes where relevant, existing Hermes bundle IDs, and product-specific paths' absence. Do not delete or rename anything.

- [ ] **Step 2: Mount and install the DMG normally**

Use macOS installer behavior and copy the independent app to an available Applications location. Do not disable Gatekeeper. Start from the installed path and verify process executable, bundle ID, userData, and runtime root.

- [ ] **Step 3: Verify install-before-login behavior**

On a fresh product runtime, observe complete offline preparation before credential fields become active. Monitor network destinations to prove no GitHub/source/runtime download. Confirm backend/model process absent before login.

- [ ] **Step 4: Run user A text and Voice conversations**

Enter credentials only in the local UI. Perform one real text prompt with one read-only tool call and one Voice prompt when microphone prerequisites work. No manual Relay command is allowed.

- [ ] **Step 5: Logout and run user B conversation**

Verify backend/forwarder epoch teardown, sign in as user B, and perform a distinct read-only tool conversation. Record only non-sensitive user/install/session/run IDs.

- [ ] **Step 6: Inspect through public Langfuse**

Log in with the dedicated administrator, filter by user/installation/session/run, open all new Traces, and verify full input/reply/tool args/result, Voice transcript/metadata, no raw audio, no credentials, and one logical record per physical call.

- [ ] **Step 7: Restart persistence and permissions**

Restart only the exact Compose project, query the same Trace IDs, recheck anonymous/ordinary-user/upload-token Dashboard denial, and recheck non-public data ports.

- [ ] **Step 8: Write the report**

Include source/image/artifact commits and hashes, commands and exit statuses, non-secret Trace IDs, screenshots with secret areas excluded, Windows verification boundary, and every acceptance result.

---

### Task 15: Final documentation, branch publication, and completion audit

**Files:**
- Modify: `ansatz-agent-platform/docs/00-overview.md` only if stable scope changed
- Modify: `ansatz-agent-platform/docs/02-progress.md`
- Modify: `ansatz-agent-platform/docs/03-file-index.md`
- Create: `ansatz-agent-platform/docs/runbooks/install-login-voice-tracing.md`
- Modify: `ansatz-agent-platform/components.lock.yaml`

**Interfaces:**
- Handoff maps each explicit prompt/spec requirement to authoritative evidence.

- [ ] **Step 1: Write the public operations runbook**

Document deploy/check/rollback, token rotation/revoke, admin credential safe-read path, certificate renewal, queue diagnostics, Langfuse query fields, package rebuild, and Windows native-test boundary. Include no secret values.

- [ ] **Step 2: Update routing/status documents**

Put mutable milestone status only in `02-progress.md`, new authoritative paths only in `03-file-index.md`, and stable product scope only in `00-overview.md` when needed.

- [ ] **Step 3: Run fresh full verification**

Run all client, auth container, Gateway, platform, Compose, package, external HTTPS, permissions, real E2E, persistence, and secret-scan commands from this plan. Capture full output and exit status.

- [ ] **Step 4: Audit every spec acceptance criterion**

Create a 12-row table matching spec section 20. For each row cite current files, command output, HTTP/browser result, artifact metadata, or Trace query. Missing/indirect evidence remains incomplete.

- [ ] **Step 5: Commit exact final docs and publish authorized branches**

Commit only task files. Push `feature/install-login-voice-trace` and `feature/auth-trace-ingest` without force only after clean status and full verification. Do not push unrelated platform working-tree content.

- [ ] **Step 6: Handoff**

Report:

- client/auth/platform commits and remote branches;
- Gateway/auth/Langfuse image IDs and deployment state;
- both Downloads artifact paths, bytes, SHA-256, architecture, signing status;
- admin URL/email and owner-only local credential path plus safe read command, never password;
- real packaged Mac Trace IDs and Dashboard evidence;
- exact Windows native-test boundary;
- any external fact that remains unproven.

- [ ] **Step 7: Finish the development branch**

Invoke `superpowers:finishing-a-development-branch`, re-run its required verification, and leave worktrees/remotes in the user-approved final state.
