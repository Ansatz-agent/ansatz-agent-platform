# Install-First Login, Voice Trace, and Admin Observability Design

Date: **2026-08-23**  
Status: **Approved for implementation by the user's standing instruction on 2026-08-23**

## 1. Decision record

This is an architectural, multi-repository change. The user explicitly instructed the implementation agent to execute the saved prompt end to end, treat the spec and plan as approved, and not stop at the ordinary human review gates. That authorization is recorded here; it does not authorize destructive cleanup, secret disclosure, changing unrelated services, or inventing DNS/registrar access that is not available.

The selected design is:

1. ship the complete Hermes runtime inside each installer and prepare it locally before showing account login;
2. keep `https://c2sml.cn/agent` as the client account authority;
3. extend that authority with short-lived opaque Trace upload tokens and server-side introspection;
4. send OTLP/HTTP protobuf through a dedicated Trace Gateway at `https://api.c2sml.cn/v1/traces`;
5. let only one dedicated Langfuse administrator use `https://trace.c2sml.cn`;
6. keep full semantic Trace content while redacting credentials and raw microphone audio;
7. produce macOS arm64 and Windows x64 artifacts through the repository's official local Electron Builder pipelines from the same clean commit.

## 2. Goals

- A user downloads and installs the complete desktop application without logging in and without Git, GitHub, SSH, Python, Node, a compiler, or an external source download.
- On first launch, the packaged runtime is prepared from the verified embedded payload before the login form is enabled.
- A signed-in user can converse through the existing Desktop and Voice surfaces; an unsigned user cannot start or reuse an Agent backend.
- Every authenticated conversation automatically produces a full NeMo Relay Trace and uploads it in the background. The first release has no user-facing or environment-variable opt-out.
- Multiple c2sml users and installations can upload into one Langfuse project while only the platform administrator can read the data.
- A lost upload response or temporary outage never repeats a model request or tool execution.
- The installed product coexists with existing `Hermes.app`, `Hermes Offline.app`, Hermes installers, and their user data.
- The final macOS package is installed and used for a real account login and Agent conversation whose Trace is opened through the public HTTPS Langfuse Dashboard.

## 3. Non-goals

- Ordinary users do not receive Langfuse accounts, raw Trace read APIs, or row-level Langfuse access.
- Langfuse is not used as the client identity provider.
- This phase does not upload raw microphone audio.
- This phase does not implement end-user Trace retention controls or a Trace-disable switch.
- This phase does not promise production high availability, multi-region failover, billing, or tenant-specific Langfuse projects.
- This phase does not redesign Hermes model/tool execution or create a second Voice implementation.
- The Windows artifact is not claimed as installed or conversation-tested without a Windows host; package and PE checks remain distinct from native Windows E2E.

## 4. Verified baseline

### 4.1 Client source

- Repository: `git@github.com:Ansatz-agent/hermes-agent.git`
- Remote base branch: `origin/integration/main-auth-voice-base`
- Base commit: `5d0e5488d1ecda66ba952c69e45482bbbc057296`
- Feature branch: `feature/install-login-voice-trace`
- Isolated worktree: `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/agent-hermes-install-login-voice-trace`
- The user's existing checkout remains on `release/desktop-dmg-auth-e2e` with unrelated uncommitted Relay changes and is not an implementation surface.
- The base already contains Desktop password authentication, Voice, a bundled backend archive, bundled auth toolchain, NeMo Relay integration, macOS DMG scripts, Windows NSIS scripts, and runtime hard gates.

The existing order is the opposite of the requested product order:

```text
minimal auth runtime install -> login -> complete runtime install -> backend
```

The target order is:

```text
complete bundled runtime install -> login -> Trace token -> backend + Relay
```

The base uses Node `26.7.0`, Electron `40.10.2`, Electron Builder `26.15.3`, and Desktop version `0.17.0`. `npm ci` completed from the lockfile and reproduced the already known six high-severity audit findings. The selected baseline Desktop suites passed 25 Windows contract tests, 41 renderer/auth/Voice tests, and 34 Electron auth/runtime tests. Python baseline execution is not yet available locally because the mandated shared Conda environment `dl` lacks the exact import `pytest`, and the existing Hermes venv also lacks pytest; no environment was created or modified.

### 4.2 Current product collision inventory

The Mac is Apple Silicon (`Darwin arm64`). Before implementation it contains:

- `/Applications/Hermes.app`
- `/Applications/Hermes Offline.app`
- `~/Downloads/Hermes-0.17.0-mac-arm64.dmg`
- `~/Downloads/Hermes-Setup.dmg`
- two existing Windows Hermes installers

`Ansatz Voice Trace Client` is not currently present. The product identity below is therefore available at the design checkpoint but must be checked again immediately before copying artifacts.

### 4.3 Authentication service

The live account service is a Django application in `/opt/agent-history-portal` on SSH alias `hermes`:

- branch: `feature/token-usage-dashboard`
- commit: `8b1b1bfaf8b2ab702b9f28abee90d1bf6bb246e0`
- Git remote: none
- runtime: container `agent-history-web`, Gunicorn, container port 8000
- public prefix: `/agent/`
- current client contract: login page, CSRF cookie, session cookie, logout, and `GET /agent/api/session/`

The source has no trustworthy remote, so implementation will create a sanitized, hash-recorded source snapshot. It will exclude `.env`, SQLite/database files, uploads/media, logs, caches, certificates, private keys, running state, and all user content. The controlled source will be placed at `agent-langfuse-server/auth-service/` without a nested ambiguous `.git`, and its origin commit plus archive hash will be recorded in `components.lock.yaml`.

### 4.4 Langfuse and reverse proxy

`hermes` is host `ecs-2b58`. The existing source-built Langfuse project runs six containers. Only Web is published, at `127.0.0.1:3100`; its Web and Worker images came from Langfuse commit `62751446149b702b419a9292ddfc2280cdf74b8c`. PostgreSQL, ClickHouse, Redis, MinIO, and Worker are not published on host ports.

Nginx Proxy Manager currently serves only `c2sml.cn`. Its active certificate covers `DNS:c2sml.cn` and does not cover subdomains. As of the design checkpoint:

- `https://c2sml.cn/agent/` is reachable and redirects to login;
- `api.c2sml.cn` does not resolve;
- `trace.c2sml.cn` does not resolve;
- no certificate exists for either required subdomain.

The implementation may prepare and deploy loopback/internal services and exact reverse-proxy configuration, but public acceptance for the two subdomains requires external DNS A/AAAA records and successful certificate issuance. It must not silently replace the approved URLs with unrelated paths.

## 5. Alternatives considered

### 5.1 Selected: opaque upload token plus auth-service introspection

The auth service issues a random opaque bearer token after verifying the existing Django session and CSRF protection. Only a hash is stored. Gateway introspection returns the trusted user and installation binding while checking expiry, logout/session binding, account active state, and device revocation.

Benefits: immediate revocation, no signing key in clients, no user password at the Gateway, simple rotation, and a clear separation between authentication and ingestion. Cost: one internal introspection request per upload batch; a short cache capped by token expiry and revocation policy may be added only if tests preserve the maximum invalidation window.

### 5.2 Rejected: embed Langfuse project keys in the client

This is operationally simple but turns every installer into a permanent shared credential. Any user could bypass identity binding, ingest arbitrary data, and keep uploading after account disablement. It also makes project-key rotation a client release problem.

### 5.3 Rejected for phase one: self-contained JWT upload tokens

JWT verification removes the internal introspection call but complicates logout, device revocation, account disablement, signing-key rotation, and maximum invalidation guarantees. The current single-host deployment does not need that optimization.

## 6. Component boundaries

```text
Ansatz Voice Trace Client
  Electron main process
    - complete offline runtime preparation
    - existing account bridge and OS vault
    - installation identity
    - Trace credential broker / local loopback forwarding boundary
  Hermes backend
    - authenticated runtime lease
    - one shared NeMo Relay lifecycle
    - Desktop and Voice conversations
        |
        | OTLP/HTTP protobuf over HTTPS
        v
api.c2sml.cn/v1/traces
  Trace Gateway container
    - request limits and bearer extraction
    - auth-service introspection
    - canonical identity/correlation attributes
    - credential redaction and safe diagnostics
    - idempotent forwarding
        |
        | Basic project credential on private network only
        v
Langfuse /api/public/otel/v1/traces
        |
        v
trace.c2sml.cn
  one dedicated administrator
```

Each component has one authority:

- Django owns user, session, device, token issuance, expiry, and revocation truth.
- Electron owns installation identity, packaged-runtime lifecycle, and secret delivery to its child runtime.
- Hermes/Relay owns semantic Trace construction and bounded background export.
- Gateway owns upload authorization, canonical server-side attributes, request limits, and forwarding.
- Langfuse owns Trace persistence, query, and administrator visualization.

## 7. Client installation and authentication state machine

States are process-visible and monotonic within one launch:

```text
runtime_check
  -> runtime_preparing
  -> runtime_ready_signed_out
  -> authenticating
  -> authenticated_trace_ready
  -> backend_starting
  -> conversational
```

Failure states are `runtime_failed`, `auth_unavailable`, `signed_out`, and `telemetry_degraded`.

Rules:

1. A fresh packaged launch resolves only the product-specific runtime root. It never adopts `~/.hermes`, `%LOCALAPPDATA%/hermes`, an existing Hermes CLI, or another Hermes Desktop install.
2. A missing or stale product runtime is prepared from the package's verified `hermes-backend.tar.gz`, installer, and manifest before the login form becomes interactive.
3. The complete runtime preparation is the only pre-auth executable exception. It never starts `hermes serve`, performs a model call, or loads user tools.
4. Once runtime preparation succeeds, the account bridge starts from that runtime and the fixed `c2sml.cn/agent` login form is displayed.
5. Successful login does not perform a second dependency installation. It obtains a Trace upload credential and then starts the backend.
6. Login status alone is insufficient: the backend requires the existing epoch-bound runtime scope and a current Trace credential context.
7. Logout stops and drains Relay within the bounded flush deadline, terminates the backend, revokes the Trace token, clears its local credential, increments the auth epoch, and leaves the already installed runtime intact for the next user.
8. Switching accounts creates a new auth epoch and Trace credential. No queue item may survive into the next user's context.

## 8. Product identity and data isolation

The selected identity is:

| Surface | Value |
|---|---|
| display/product name | `Ansatz Voice Trace Client` |
| npm package name | `ansatz-voice-trace-client` |
| macOS bundle ID | `cn.c2sml.ansatz.voice-trace-client` |
| executable | `Ansatz Voice Trace Client` / `AnsatzVoiceTraceClient.exe` |
| protocol scheme | `ansatz-voice-trace` |
| macOS userData | Electron path derived from the independent product name |
| Windows install directory | `Ansatz Voice Trace Client` |
| Windows AppUserModelID | `cn.c2sml.ansatz.voice-trace-client` |
| update channel | `ansatz-voice-trace-client` |
| runtime/user root on macOS | `~/.ansatz-voice-trace-client` |
| runtime/user root on Windows | `%LOCALAPPDATA%\AnsatzVoiceTraceClient` |

NSIS uninstall identity and shortcut names derive from the independent app ID/product name and are contract-tested. The product never migrates or imports existing Hermes user data automatically.

Artifact names include version, platform, architecture, and source commit:

- `Ansatz-Voice-Trace-Client-0.17.0-mac-arm64-<12-char-commit>.dmg`
- `Ansatz-Voice-Trace-Client-0.17.0-win-x64-<12-char-commit>.exe`

The name is re-audited immediately before copying to Downloads. Existing files are never overwritten.

## 9. Trace token API

### 9.1 Issue or refresh

`POST /agent/api/trace-token/`

Authentication: existing Django session cookie plus CSRF.  
Content type: `application/json`.

Request:

```json
{
  "installation_id": "uuid-v4",
  "client_version": "0.17.0",
  "telemetry_schema_version": "1"
}
```

Response `201` for a new token and `200` for rotation:

```json
{
  "access_token": "opaque-base64url",
  "expires_in": 900,
  "expires_at": "RFC3339",
  "installation_id": "uuid-v4"
}
```

The token lifetime is 15 minutes. Rotation revokes the prior token for the same Django session and installation. The response is `Cache-Control: no-store` and never includes a Langfuse key.

### 9.2 Internal introspection

`POST /agent/internal/trace-token/introspect/`

This route is reachable only on the private container network. Gateway authenticates with a separately generated internal service credential stored in owner-only secret files. The credential is sent in a header and is never accepted at the public reverse proxy.

Request:

```json
{"token":"opaque-base64url"}
```

Active response:

```json
{
  "active": true,
  "token_id": "server-id",
  "platform_user_id": "stable-user-pk",
  "installation_id": "uuid-v4",
  "expires_at": "RFC3339",
  "scope": "trace:write",
  "audience": "ansatz-trace-gateway"
}
```

All invalid, expired, logged-out, revoked, disabled-user, wrong-scope, and wrong-audience cases return the same inactive shape. Diagnostics do not distinguish token existence to public callers.

### 9.3 Revocation

- The custom logout path revokes every active Trace token bound to the current Django session before clearing the session.
- Disabling a user makes introspection inactive immediately.
- Device revocation marks all tokens for the user/installation inactive.
- Token records store only a SHA-256 digest and a non-secret identifier.
- Expired token records are periodically removed; their lifetime is not extended by use.

## 10. Trace Gateway API

### 10.1 Public ingestion

`POST https://api.c2sml.cn/v1/traces`

Required headers:

- `Authorization: Bearer <opaque token>`
- `Content-Type: application/x-protobuf`
- `X-Hermes-Session-ID`
- `X-Trace-Entrypoint` with one of `desktop`, `voice`, `cli`, `dashboard`
- `X-Trace-Run-ID`
- `X-Telemetry-Schema-Version: 1`

The body is an OTLP `ExportTraceServiceRequest`. Maximum compressed and decompressed sizes are both 8 MiB; content encoding is initially `identity` only. The response preserves the OTLP protobuf response type on success.

### 10.2 Canonicalization

After introspection and protobuf decoding, the Gateway removes every occurrence of the canonical fields from resource, scope, and span attributes, then writes exactly one canonical set at the resource level:

- `platform.user.id` and Langfuse-compatible `user.id`: introspected user ID;
- `client.installation.id`: introspected installation ID;
- `hermes.session.id`: validated request header;
- `hermes.entrypoint`: validated request header;
- `hermes.run.id`: validated request header;
- `telemetry.schema.version`: Gateway-supported version;
- `trace.gateway.request.id`: server-generated request identifier.

Session, entrypoint, and run identifiers are correlation input, not authorization identity. They are syntax-validated, length-bounded, canonicalized, and never allowed to replace user/installation authority.

### 10.3 Idempotency

OTLP Trace/span IDs are the primary idempotency keys. The Gateway computes a request digest over the canonical token ID plus canonicalized protobuf body. A bounded server cache rejects or returns the stored outcome for an identical retry during a 15-minute window. Langfuse receives stable Trace/span IDs, so a response retry cannot generate a second model/tool execution. The client never reruns Agent work because of export status.

### 10.4 Responses and limits

- `200`: accepted/forwarded OTLP response;
- `400`: malformed headers or protobuf;
- `401`: missing, invalid, expired, or revoked token;
- `413`: body too large;
- `415`: wrong media type or encoding;
- `429`: per-token or per-source rate limit;
- `502/503`: introspection or Langfuse unavailable.

Public error bodies are fixed, small JSON or OTLP-safe envelopes with `Cache-Control: no-store`. They never echo headers, tokens, payloads, or upstream credentials.

## 11. Client Trace credential and Relay lifecycle

The bearer token must not enter the renderer. Electron main owns token refresh and exposes only readiness state. The backend receives a local, epoch-bound export capability rather than a reusable Langfuse credential.

The implementation uses a loopback Trace credential broker owned by the authenticated Desktop lifecycle:

1. Electron obtains/refreshes the opaque token through the fixed Python account bridge and OS vault.
2. Electron starts a loopback-only forwarding listener on an ephemeral port with a random per-epoch local bearer.
3. The backend receives only the loopback URL and local epoch bearer through its protected spawn environment.
4. NeMo Relay exports to that loopback listener. The listener adds the current public Gateway bearer and forwards over HTTPS.
5. Electron refreshes the public token before its 15-minute expiry without restarting the model backend or Relay.
6. Logout first rejects new loopback requests, waits at most 3 seconds for in-flight batches, clears both credentials, and then tears down the backend.

The local bearer is never written to config. It is named as a secret so Hermes subprocess environment filtering removes it from tools. The loopback listener rejects non-loopback peers, missing local bearer, wrong content type, oversized bodies, and stale auth epochs.

Relay configuration is a packaged, product-owned file and is enabled automatically for the product runtime. There is no normal UI setting, `config.yaml` option, or supported environment switch that disables upload. Existing upstream Hermes remains unaffected because the forced configuration activates only under the product-specific Desktop runtime marker.

Queue policy:

- maximum 128 batches or 32 MiB, whichever is reached first;
- memory only in phase one, so full Trace content is not persisted at rest;
- exponential backoff with jitter: 1, 2, 4, 8, 16, then 30 seconds;
- maximum item age: 15 minutes and never beyond the owning auth epoch;
- enqueue is non-blocking for the Agent response;
- when full, drop the oldest unsent batch, increment a non-sensitive counter, and surface `telemetry_degraded` to diagnostics;
- shutdown flush deadline: 3 seconds;
- retry reuses identical protobuf bytes and Trace/span IDs.

## 12. Full Trace and privacy rules

Full semantic Trace includes:

- complete user input and Voice transcript;
- complete model response and reasoning fields already allowed by the current Relay schema;
- tool name, arguments, result, status, and error;
- model/provider, latency, token usage, session/run correlation, and exceptions.

Mandatory redaction runs before export and again at the Gateway defense-in-depth boundary. It removes or replaces:

- login password and credential-form fields;
- `Authorization`, `Cookie`, `Set-Cookie`, CSRF, session IDs used for authentication;
- API keys, bearer tokens, private keys, access/refresh tokens, and known provider secrets;
- Langfuse project and administrator credentials.

Raw microphone/audio bytes, file paths to captured audio, and base64 audio payloads are rejected from Trace attributes/events. Voice transcript, timing, selected voice mode, recognition/model metadata, and tool activity are retained.

The login surface includes an explicit notice before submission that full model input, response, and tool parameters/results are uploaded for administrator quality analysis and incident diagnosis.

## 13. Server deployment and HTTPS topology

All production runtime services run on SSH alias `hermes`; L40S is only a build host if a heavy image rebuild is required.

```text
Nginx Proxy Manager :443
  api.c2sml.cn   -> 127.0.0.1:<gateway-port>
  trace.c2sml.cn -> 127.0.0.1:3100

private Compose networks
  trace-gateway -> agent-history-web:8000 introspection
  trace-gateway -> langfuse-web:3000 OTLP
  langfuse-web/worker -> PostgreSQL, ClickHouse, Redis, MinIO
```

Only Nginx Proxy Manager binds public 80/443. Gateway and Langfuse Web bind loopback or a private container network. Data services and Worker have no host/public ports.

Proxy requirements:

- preserve `Host`, `X-Forwarded-Proto=https`, and request IDs;
- pass the client `Authorization` header only to Gateway;
- do not pass client authorization to Langfuse Web;
- disable buffering/caching for Trace POST;
- allow 8 MiB bodies and a 15-second upstream timeout;
- apply per-source connection and request limits;
- redirect HTTP to HTTPS;
- enable HSTS only after both subdomain certificates are valid.

Langfuse uses `NEXTAUTH_URL=https://trace.c2sml.cn`. Public signup is disabled. Direct public access to internal OTLP, health, database, object storage, queue, or worker ports is prohibited.

## 14. Administrator model and credential handoff

The target deployment has exactly one enabled dedicated Langfuse platform administrator for this environment. Before mutation, implementation inventories enabled Langfuse users without reading password hashes or sessions.

Identity format:

- name: `Ansatz Trace Administrator 20260823`
- email: `trace-admin-20260823@c2sml.cn`

If that exact identity already exists, a cryptographically random suffix is added and recorded. The password is generated with at least 32 random bytes and stored only in owner-readable local and remote secret files. It is never put in Git, Compose YAML, command arguments, logs, reports, Trace, or chat.

The handoff includes:

- login URL;
- administrator email;
- local absolute credential-file path;
- safe local read command that the user executes themselves;
- evidence that file mode is owner-only and a real browser login succeeded.

Existing administrators are not deleted automatically. If inventory finds another enabled admin, it is first disabled through a reversible database/API transaction only after a backup and explicit before/after evidence. The final acceptance query must prove exactly one enabled administrator.

## 15. Auth-service containerization and source custody

The sanitized Django snapshot becomes an explicitly versioned service image. Its Compose service remains independent from Gateway and Langfuse, with separate credentials and minimum network access.

Source provenance includes:

- remote host and original path;
- original Git commit and branch;
- proof of absent remote;
- sanitized archive SHA-256 and file manifest;
- local import commit;
- excluded-path manifest;
- container image ID and build time.

The live service is not replaced until its regression tests pass, database backup exists, migrations are reviewed, and rollback can restore the previous image plus schema-compatible data.

## 16. Failure behavior

- Runtime preparation failure leaves the login form disabled and provides bounded retry; it never starts an Agent.
- Authentication outage leaves the installed runtime intact and the user signed out.
- Token issue/refresh failure stops new Trace export. Existing conversation may finish only while the current authenticated runtime lease is still valid; no model/tool work is repeated.
- Gateway or Langfuse outage moves Trace batches into the bounded in-memory queue and returns the Agent result normally.
- Queue overflow drops oldest telemetry, never Agent work, and creates a non-sensitive diagnostic counter.
- Introspection outage fails closed with `503`; it does not accept cached identity beyond the explicit cache maximum.
- A `401` triggers one token refresh and one identical Trace resend, never a conversation retry.
- Logout/account switch invalidates the whole auth epoch and discards unsent old-user batches.
- Malformed OTLP, forged identity fields, wrong schema, and raw audio payloads are rejected before Langfuse.
- Langfuse migration/health failure preserves existing data and old containers for rollback; no volume deletion or `down -v` is allowed.
- DNS/certificate absence prevents the public-HTTPS completion claim but does not prevent local/internal implementation and testing.

## 17. Test strategy

All behavior changes follow RED -> observed expected failure -> minimal GREEN -> refactor while green. Tests execute behavior, not source-text patterns.

### 17.1 Client unit and integration

- fresh packaged lifecycle prepares complete runtime before enabling login;
- runtime is not reinstalled after login and remains ready after logout;
- unsigned state never starts or reuses backend/Relay;
- login starts credential broker, backend, and Relay in one auth epoch;
- no supported preference or environment input disables product Trace;
- notice text describes full semantic upload;
- public token never reaches renderer, logs, tool subprocess environment, or package files;
- refresh changes public bearer without redoing model/tool work;
- logout drains, revokes, clears, and rejects stale epoch batches;
- account switch changes trusted user while keeping installation identity;
- retry preserves bytes and Trace/span IDs;
- queue limits, backoff, expiry, overflow, and 3-second flush are deterministic;
- Voice transcript exports through the same lifecycle and raw audio does not.

### 17.2 Auth service

- existing login, CSRF, session expiry, logout, rate limiting, and disabled-account behavior remain green;
- anonymous or missing-CSRF issue requests fail;
- valid session issues a 15-minute opaque upload-only token;
- only token hashes are stored;
- rotation revokes the prior token;
- introspection requires the internal service credential;
- expiry, logout, disablement, device revocation, wrong audience, and wrong scope are inactive;
- responses/logs contain no password, cookies, raw tokens, signing/internal secret, or real user fixtures.

### 17.3 Gateway

- missing/malformed/expired/revoked token is rejected;
- valid OTLP protobuf is forwarded with server-trusted user/installation fields;
- forged user/install/resource/span fields are removed;
- session/entrypoint/run headers are validated and canonicalized;
- body/media/encoding/rate limits match the contract;
- identical retry produces one logical Trace/span set;
- upstream 401/5xx never echoes credentials;
- logs contain no authorization, cookie, password, Langfuse key, or full Trace payload;
- raw audio attributes/events are rejected or removed according to the redaction contract.

### 17.4 Deployment and permissions

- Compose publishes only intended loopback ports;
- databases, ClickHouse, Redis, MinIO, and Worker are unreachable publicly;
- anonymous Dashboard access redirects to login;
- ordinary c2sml accounts and upload tokens cannot log in to Langfuse;
- dedicated admin can log in and filter by user/installation/session/run/time;
- exactly one enabled dedicated administrator exists;
- owner-only secret permissions and secret scans pass.

### 17.5 Packaging

- macOS and Windows configurations use the independent product identity everywhere;
- package payload contains the complete source/runtime inputs and no external runtime download path;
- source commit is clean and identical for both builds;
- official root scripts call the official Desktop prepare/build/Builder hooks;
- artifact names, bundle/app ID, executable, install/uninstall identity, shortcut, protocol, userData, runtime root, and updater channel do not collide;
- macOS bundle architecture, embedded runtime, signature/notarization, install, first launch, and Gatekeeper status are recorded;
- Windows NSIS contract, internal payload, PE x64 app executable, and installer provenance pass.

### 17.6 Real E2E

1. Use two non-admin c2sml test users and two installation IDs.
2. Install the newly built DMG as the independent app, launch it from the install location, and prove existing Hermes applications/data still exist.
3. User A performs a real text conversation with one safe read-only tool call; export is automatic.
4. User A performs a Voice conversation when microphone/model prerequisites are available; the Trace contains transcript and no audio bytes.
5. Logout and sign in as user B; perform a different text/tool conversation without identity carryover.
6. Dedicated admin logs in at `https://trace.c2sml.cn`, locates all new Traces, and validates full input, response, tool args/result, Voice metadata, and canonical identity fields.
7. Verify no duplicate model/tool calls, no secrets, and persistence after controlled service restart.

## 18. Rollout

1. Commit the approved spec and exact implementation plan without staging unrelated platform files.
2. Implement and test product identity and install-first state machine in the isolated client branch.
3. Implement auth token models/endpoints against the sanitized source snapshot and run regression tests.
4. Implement and container-test Gateway plus internal Langfuse forwarding.
5. Deploy auth/Gateway/Langfuse changes on private/loopback interfaces and verify synthetic multi-user ingestion.
6. Add DNS records and issue certificates for `api.c2sml.cn` and `trace.c2sml.cn`; enable exact Nginx routes.
7. Create and browser-test the dedicated Langfuse administrator.
8. Build both artifacts from one clean client commit through official local scripts.
9. Install the Mac artifact and run the two-user packaged-app E2E.
10. Update runbook, evidence report, component locks, file index, and progress.

## 19. Rollback

- Client: uninstall only `Ansatz Voice Trace Client`; its product-specific app/runtime/userData paths leave Hermes untouched. Retain the installer hash for reproduction.
- Gateway: remove only its exact Nginx route and stop its exact Compose service; keep logs without payloads and do not prune images globally.
- Auth service: restore the previous image and pre-migration database backup. Token tables are additive; rollback ignores them.
- Langfuse: restore prior `NEXTAUTH_URL`/proxy route and image definitions without deleting volumes.
- Administrator: restore the prior enabled-state inventory from the reversible record if migration validation fails.
- DNS: remove only the two task-created records after proxy rollback; no other c2sml records change.

## 20. Acceptance criteria

Completion requires current evidence for every item:

1. Client feature branch is based on the recorded remote commit and has a clean deliverable commit.
2. Installers contain full runtime payloads and do not download source/runtime on user install or first launch.
3. Fresh packaged launch completes runtime preparation before login; unsigned state cannot converse.
4. Authenticated Desktop and Voice share one forced Relay lifecycle with bounded retry/flush and no model/tool duplication.
5. Auth service issues, refreshes, introspects, and revokes upload-only opaque tokens without secret leakage.
6. Gateway accepts valid OTLP, rejects invalid access, overwrites trusted identity, enforces limits, and forwards with server-only Langfuse keys.
7. Two users/two installations are distinguishable to the administrator and cannot read Langfuse.
8. Public HTTPS endpoints are live at the exact approved hosts with valid certificates.
9. The deployment has exactly one enabled dedicated Langfuse administrator; credential handoff is owner-only and browser-tested.
10. macOS arm64 DMG and Windows x64 EXE come from the same clean commit through official local tooling and are copied to Downloads without overwrite, with byte size and SHA-256.
11. The newly installed Mac app coexists with existing Hermes apps/data and completes a real conversation whose full Trace is opened in the public Dashboard.
12. Secret scans, public-port checks, service restart persistence, package audits, and complete requirement audit pass.

## 21. Resolved and external decisions

No implementation choice remains unspecified:

- opaque introspected tokens are selected over JWT and direct Langfuse credentials;
- token lifetime is 15 minutes;
- Gateway body limit is 8 MiB;
- queue limit is 128 batches/32 MiB/15 minutes with a 3-second flush;
- product identity and paths are fixed above;
- Mac target for this round is arm64 because the official base script requires the observed arm64 host; universal is not claimed;
- Windows target is x64 and may be cross-built on Mac with native Windows installation still explicitly unverified;
- raw audio is excluded;
- one dedicated admin is the only enabled Langfuse user at acceptance.

External infrastructure still required for final public acceptance is precisely identified: DNS A/AAAA records for `api.c2sml.cn` and `trace.c2sml.cn`, followed by certificate issuance. The absence of those records is a factual deployment dependency, not an architectural ambiguity and not permission to change the approved endpoint names.
