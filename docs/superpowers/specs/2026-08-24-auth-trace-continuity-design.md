# Authentication and Trace Continuity Design

Date: **2026-08-24**
Status: **Approved for implementation by the user's standing instruction on 2026-08-24**

## 1. Decision record

This is an architectural change across three repositories:

- `agent-hermes-client`, based only on `main@80bc34f5f`;
- `agent-langfuse-server`, based only on `main@31a22bf1bf`;
- `ansatz-agent-platform`, based only on `origin/main@0b1b5b04c2c7` because the existing local `main` checkout is stale and dirty.

The existing client branch `fix/relay-token-cost` and all of its staged changes are explicitly out of scope. No file, commit, patch, stash, or dependency decision from that branch may be copied into this work.

The user approved all subsequent plans, recommendations, implementation strategies, and review gates without requiring further questions. This approval authorizes local branches, worktrees, commits, implementation, tests, and local review. It does not authorize push, pull request creation, deployment, destructive cleanup, production mutation, or secret disclosure.

The selected design is:

1. Separate durable local authorization from online authentication-service health.
2. Add an immutable account identifier and a persistent, explicitly revocable native client Session protocol.
3. Make Trace credential acquisition and upload entirely asynchronous relative to Hermes backend startup and conversation execution.
4. Replace the in-memory Trace queue with an account-isolated, encrypted, crash-safe disk outbox.
5. Replace Gateway pass-through delivery and token-scoped in-memory deduplication with a durable inbox and token-independent batch receipts.
6. Develop authentication continuity and Trace continuity in parallel worktrees, then integrate their client changes on a dedicated branch.

## 2. Goals

### 2.1 Authentication continuity

- After a successful first login, local Hermes remains usable until the user signs out or the server explicitly reports `account_disabled`, `account_revoked`, or `session_revoked` for the current native Session.
- Timeout, offline state, DNS/TLS/VPN/proxy errors, 429, 5xx, malformed responses, bridge failures, and ordinary Web Session expiry never revoke local authorization, stop a backend, close a conversation, or navigate to login.
- On restart, the client restores local authorization from the OS credential store before making a network request and starts local capability while validation runs in the background.
- Server recovery triggers silent revalidation. It never requires login merely because an outage occurred.
- User sign-out clears authentication credentials and application access but preserves SessionDB, attachments, projects, profiles, and local conversations.
- Trace-token readiness is not a prerequisite for local backend startup.

### 2.2 Trace continuity

- A local OTLP batch is acknowledged to Relay only after one durable owner exists: either the Gateway has returned a matching durable receipt or an encrypted outbox record has completed segment+journal fsync.
- Pending batches survive normal quit, crash, process termination, operating-system restart, authentication outage, Trace-token failure, Gateway failure, and Langfuse failure.
- Upload is FIFO per account. A failed head batch remains durable and is retried without repeating Agent/model/tool work.
- A successful or duplicate durable Gateway receipt means payload bytes are not retained locally: a not-yet-committed append is cancelled, or an already committed outbox record is tombstoned and reclaimed.
- Recovery is triggered by a new batch, retry timer, renderer online transition, system resume, window focus, Trace-token refresh completion, token-near-expiry timer, and upload 401.
- Trace credential acquisition is single-flight. Upload 401 refreshes only the Trace credential and resends the same batch.
- Short-lived token rotation, Gateway restart, concurrent retries, and lost upstream responses do not create a second logical Gateway batch.

### 2.3 Server contracts

- Every account has an immutable UUID `account_id` independent of username and Django integer primary key reuse.
- Native client Sessions are persistent domain records with immutable `session_id`, installation binding, secret digest, explicit lifecycle, and structured revocation reason.
- Trace-token introspection distinguishes refreshable token failure from explicit account or Session revocation.
- Gateway batch idempotency is based on trusted account identity plus client `batch_id`, never short-lived Trace token ID.

## 3. Non-goals

- No CLI or standalone Dashboard authentication/upload unification in this round.
- No redesign of Hermes SessionDB or local conversation persistence.
- No deletion of local conversations, attachments, or SessionDB on sign-out or revocation.
- No multi-region or multi-replica Gateway high availability. The deployed topology remains one Gateway instance with durable storage.
- No exactly-once guarantee for arbitrary third-party OTLP senders that do not provide the new batch protocol.
- No user-facing telemetry opt-out or raw-audio upload change.
- No push, pull request, deployment, production migration, or installer release without a later explicit request.

## 4. Current root causes

The current implementation deliberately encodes the opposite behavior:

- Python authentication refresh converts every `AuthServiceError` into `locked`, increments epoch, and removes the refresh schedule.
- Electron removes the runtime scope and calls full capability cleanup for every non-authenticated status or bridge error.
- The renderer mounts the protected conversation tree only for `authenticated && runtime_ready`.
- Local backend preparation waits for `ensureDesktopTraceForwarder()`, which waits for a current Trace token.
- The Trace queue is a process-memory array limited to 128 batches, 32 MiB, and 15 minutes; stop and epoch change clear it.
- The auth service identifies users externally with `str(User.pk)`, has no native Session model, and returns no structured revocation reason.
- Trace introspection collapses expired, rotated, revoked, invalid, and disabled-account tokens into `{active:false}`.
- Gateway deduplication includes short-lived token ID, lasts 15 minutes in memory, and disappears on restart.
- Gateway forwards synchronously to Langfuse before responding, leaving a commit-ambiguity window when Langfuse accepts but the response is lost.

Several existing tests pin these old contracts and must be inverted through TDD rather than retained.

## 5. Alternatives considered

### 5.1 Selected: durable local authorization plus durable client/server queues

This design treats local conversation capability, account validation, Trace credential availability, and remote Trace delivery as four distinct state machines. It requires the most explicit migration work, but it is the only option that satisfies offline restart, explicit-only revocation, crash-safe Trace retention, and cross-token/restart idempotency together.

### 5.2 Rejected: extend the current authentication lease or add a grace period

A longer lease merely delays the same teardown. Any finite grace period eventually violates the requirement during a longer outage, and startup still cannot restore capability after the cached lease expires.

### 5.3 Rejected: client-only persistent retry

A disk outbox alone prevents local loss but cannot prevent duplicates after Trace-token rotation, Gateway restart, concurrent retries, or an upstream response loss. It also cannot provide structured account/Session revocation.

### 5.4 Rejected: Gateway deduplication without durable acceptance

A durable success ledger written only after synchronous Langfuse forwarding still has a commit-ambiguity window. Durable inbox acceptance before response is required.

## 6. Architecture

```text
OS credential store
  └─ native Session credential + cached account principal
       ├─ local authorization state ───────────────> Hermes backend / conversation
       └─ background validation health
              ├─ active / transient failure ──────> keep local capability
              └─ explicit revoke ─────────────────> stop capability, keep local data

Hermes / NeMo Relay
  └─ loopback OTLP
       └─ Electron durable encrypted outbox
            └─ Trace-token provider (single-flight, asynchronous)
                 └─ HTTPS Trace Gateway
                      └─ durable inbox + receipt ledger
                           └─ background FIFO delivery
                                └─ Langfuse OTLP
```

Authorities are explicit:

- The client credential store is authoritative for whether a previously authenticated principal may start local capability while no explicit revocation has been observed.
- The auth service is authoritative for immutable account identity and explicit account/Session revocation.
- Electron main is authoritative for machine-local outbox persistence, encryption, runtime lifecycle, and retry triggers.
- Gateway durable storage is authoritative for cloud-side batch acceptance and deduplication.
- Langfuse remains authoritative for final Trace query and visualization.

## 7. Account and native Session protocol

### 7.1 Data model

Add an account identity model associated one-to-one with Django `User`:

```text
AccountIdentity
  account_id UUID unique immutable
  user FK unique
  state active | revoked
  revoked_at nullable
  revocation_reason nullable
  created_at
```

Backfill one UUID for every existing user in a deterministic migration transaction. The UUID is generated once and never derived from username or integer primary key.

Add a persistent native Session model:

```text
ClientSession
  session_id UUID unique immutable
  account FK
  installation_id UUID
  credential_digest unique
  created_at
  last_seen_at
  revoked_at nullable
  revocation_reason nullable
```

Allowed explicit revocation reasons are:

- `signed_out`
- `session_revoked`
- `account_disabled`
- `account_revoked`

`expired`, `unknown_credential`, transport failure, rate limiting, and malformed service responses are not explicit local-capability revocations.

The native Session credential is a high-entropy opaque value. Only its digest is stored server-side. It is bound to `installation_id` and remains valid until explicit revocation. The existing Django Web Session remains a login/bootstrap mechanism and may expire without affecting the native Session.

### 7.2 Issue endpoint

`POST /auth/api/client-session/`

Authentication: current Django login Session plus CSRF. Request:

```json
{
  "installation_id": "uuid-v4",
  "client_version": "string"
}
```

Response `201`, `Cache-Control: no-store`:

```json
{
  "account_id": "uuid-v4",
  "session_id": "uuid-v4",
  "session_token": "opaque-base64url",
  "username": "display-only",
  "issued_at": "RFC3339"
}
```

### 7.3 Status endpoint

`GET /auth/api/client-session/`

Authentication: `Authorization: Bearer <native-session-token>` and installation binding.

Active response `200`:

```json
{
  "state": "active",
  "account_id": "uuid-v4",
  "session_id": "uuid-v4",
  "installation_id": "uuid-v4",
  "username": "display-only",
  "server_time": "RFC3339"
}
```

Explicit revocation response `403`:

```json
{
  "state": "revoked",
  "code": "account_disabled | account_revoked | session_revoked",
  "account_id": "uuid-v4",
  "session_id": "uuid-v4",
  "revoked_at": "RFC3339",
  "retryable": false
}
```

An unknown/malformed credential returns `401` with `code=invalid_session_credential`. The client treats this as cloud authorization unavailable and preserves local capability until it receives a structured explicit revocation for the cached `account_id/session_id` or the user signs out. Administrators must revoke rather than delete Session records so explicit evidence remains available.

### 7.4 Sign-out and administrative controls

- Client sign-out immediately clears local authentication credentials and stops local capability.
- It best-effort calls `DELETE /auth/api/client-session/current/` before clearing the token.
- Remote sign-out persists `signed_out` on the ClientSession and revokes its Trace tokens.
- Admin account disable/revoke marks every active ClientSession with the corresponding explicit reason.
- Admin Session revoke affects only the selected Session.
- No path deletes local conversations or attachments.

## 8. Client authentication state machines

### 8.1 Durable cached principal

The OS credential record contains:

- schema version;
- `account_id`;
- `session_id`;
- native Session token;
- installation ID;
- display username;
- last successful validation time.

No password is stored. A revocation tombstone contains only non-secret account/session IDs, reason, and timestamp so a revoked cached credential cannot be resurrected offline.

### 8.2 Local authorization

Local authorization states:

```text
signed_out
  -> active_cached
  -> explicitly_revoked
```

Only user sign-out or an explicit structured revocation moves away from `active_cached`. Online failures do not mutate this state, account identity, or runtime generation.

### 8.3 Validation health

Validation is a parallel state:

```text
unknown -> validating -> online
                    \-> degraded(retryable reason)
```

Health affects cloud upload diagnostics only. It never controls AuthGate mounting or backend cleanup.

### 8.4 Legacy cache migration

Existing installations may have only a Django cookie record. If the service is unavailable on the first upgraded launch:

- the presence of an unsign-out legacy credential restores local capability;
- a local legacy principal key is derived from a digest of the credential record, never username;
- Trace upload remains paused because no trusted `account_id` exists;
- after the first successful validation/login, the client obtains a native Session, atomically replaces the legacy record, and migrates any legacy-keyed outbox directory to the trusted account namespace;
- explicit sign-out clears the legacy record and prevents later offline restoration.

### 8.5 Runtime and renderer behavior

- Scope/runtime generation changes only on sign-out, explicit revocation, or account switch.
- Cached local authorization can mint/refresh local runtime scope tokens without contacting the service.
- Backend startup never awaits account validation or Trace token acquisition.
- AuthGate mounts the protected app for `active_cached` plus runtime readiness. Validation degradation is non-terminal status, not navigation.
- Existing conversation components and backend remain mounted across authentication outages.

## 9. Trace-token protocol

Trace tokens are issued using the native Session bearer rather than requiring a live Django Web Session.

Active introspection adds trusted fields:

```json
{
  "active": true,
  "token_id": "server-id",
  "account_id": "uuid-v4",
  "session_id": "uuid-v4",
  "installation_id": "uuid-v4",
  "expires_at": "RFC3339",
  "scope": "trace:write",
  "audience": "ansatz-trace-gateway"
}
```

Inactive internal responses include one reason:

- `token_expired`
- `token_rotated`
- `token_revoked`
- `session_revoked`
- `account_disabled`
- `account_revoked`
- `invalid_token`

Gateway external responses:

- `401 code=trace_token_refresh_required` for expired, rotated, invalid, or ordinary token revocation;
- `403 code=session_revoked | account_disabled | account_revoked` only after authenticated introspection provides explicit revocation;
- `503 code=authentication_unavailable` for timeout, DNS, 429, 5xx, malformed, or unknown introspection responses.

The client always handles ingest 401 by invalidating only the Trace credential, performing one single-flight refresh, and resending the same batch. It never signs the user out because of ingest 401. An explicit ingest 403 may be passed to the authentication owner as confirmed revocation evidence for the same account/session.

Gateway must first deploy an introspection parser that tolerates additive fields. The existing strict unknown-field rejection makes this rollout order mandatory.

## 10. Trace payload pressure and durable client outbox

### 10.1 Storage-pressure policy

SessionDB and Trace are not equivalent copies. SessionDB appends each conversation message and tool result once for recovery. Full Trace additionally records model/tool timing, attempts, hierarchy, and per-call usage, but the current producer also repeats the complete conversation history in every model request and again in turn start/end marks. Without mitigation, long sessions can make Trace growth approach quadratic in turns and model/tool steps, while SessionDB remains approximately linear. Inline base64 media makes the amplification worse.

This design preserves complete per-model-call request/response observability while removing only data that is redundant inside the same Trace or binary content outside the intended semantic Trace boundary:

- turn start/end marks no longer embed the complete `conversation_history`; they retain turn input, output, result, correlation, timing, and outcome;
- every model span continues to carry the complete request actually sent to the provider, so each model call remains independently inspectable in Langfuse;
- inline image/audio/binary payloads are represented by content hash, media type, and byte size rather than raw/base64 bytes; text transcripts and textual multimodal prompts remain;
- system prompt and request repetition remain unchanged in the wire schema for this round and are handled by local compression.

An LLM-history-delta wire protocol was considered and rejected for this round. It would reduce bytes further but make individual model observations non-self-contained and require new server-side reconstruction semantics. Continuity must not trade away debuggability merely to reduce storage.

SessionDB is not reused as the outbox. It is owned by the Python backend, has its own WAL/full-sync behavior, indexes, compaction, and conversation lifecycle. Sharing it with Electron would create cross-process locking, couple telemetry deletion to conversation retention, and mix unredacted conversation truth with redacted upload payloads.

### 10.2 Storage layout and encryption

Electron main owns:

```text
<product-user-data>/trace-outbox/<account-key>/
  key.json
  segments/
  index.journal
  diagnostics.json
```

- Generate a random 256-bit per-account data key.
- Wrap the data key with Electron `safeStorage`; never store the raw key.
- Append batches to bounded segment files, targeting 64 MiB per segment, so the 2 GiB ceiling uses tens of files rather than tens of thousands of per-batch files.
- Compress each record with built-in asynchronous Brotli before encryption, then encrypt it independently with AES-256-GCM using a random nonce and authenticated metadata. Compression never runs after encryption.
- Keep a checksummed append-only metadata journal mapping `batch_id` to segment/offset/state. The index is reconstructable by scanning valid segment records after a crash; it is not a second blob store.
- Group commits for at most 50 ms, fsync the segment and journal, then acknowledge every batch in that committed group. Acknowledgement never precedes both durability barriers.
- Segment and journal compaction run only while conversation streaming is idle. They never acquire or share SessionDB locks.
- If secure OS encryption is unavailable, local conversation remains usable but Trace capture reports degraded and does not persist plaintext Trace payloads.

### 10.3 Batch schema

Each durable envelope includes:

- `batch_id` UUIDv4;
- trusted local account key and, when known, `account_id/session_id`;
- installation ID;
- session ID, run ID, entrypoint, telemetry schema;
- OTLP payload digest;
- encrypted original protobuf bytes;
- creation sequence/time;
- attempt count, last error class, and next retry time.

### 10.4 Local acknowledgement and state

```text
received
  -> racing_gateway_and_local_commit
  -> gateway_owned (matching durable receipt wins)
  -> durable_pending (local fsync wins or cloud is unavailable)
  -> sending
  -> durable_pending (retryable failure)
  -> quarantined (terminal payload error)
  -> accepted | duplicate
  -> deleted payload + retained receipt tombstone
```

The normal online path races two durable boundaries using the same preassigned `batch_id` and payload digest: Gateway acceptance and the local compressed/encrypted append. The loopback endpoint returns success when the first boundary completes. If the Gateway receipt wins before the journal commit, the append is cancelled; any unjournaled tail is ignored/truncated during recovery and only a small payload-free receipt tombstone may remain for deduplication. If local fsync wins, the record becomes `durable_pending`, Relay receives success, and the in-flight upload continues; a later matching receipt tombstones the record and makes its segment bytes reclaimable. Successful Trace/span payload bytes are therefore never retained locally beyond this transient race/reclamation window.

The direct Gateway race is allowed only when the account has no older pending/quarantined sendable batch and the caller owns the per-account admission/send slot. If backlog exists or another batch owns the slot, the new batch commits locally and joins FIFO; newer data cannot bypass older durable data. Authentication/token unavailability skips the cloud contender and proceeds directly to local durability. If both durable paths fail, return a local 507-style failure and a non-sensitive diagnostic; never claim persistence.

FIFO is per account. Retryable failure retains the head. Terminal `400/409-digest-mismatch/413/415` moves the payload to encrypted quarantine rather than silently deleting it. Quarantine counts toward the same capacity and retention limits.

### 10.5 Limits and controlled degradation

- Hard capacity: 2 GiB per account namespace.
- Hard retention: 30 days for unsent/quarantined payloads and receipt tombstones.
- Free-disk reserve: before append or segment rotation, preserve at least the greater of 1 GiB or 5% of the containing volume. If the reserve would be crossed, apply the same oldest-unsent controlled eviction before writing.
- Individual logical batch limit remains 8 MiB after redaction and before compression. The producer splits on span/resource boundaries where possible; a single oversize span is quarantined with diagnostics rather than silently discarded.
- On limit exhaustion, delete the oldest unsent/quarantined batch, increment durable counters, and expose diagnostics. Never delete SessionDB or block local conversation.
- The policy intentionally accepts bounded telemetry loss only after these explicit limits; ordinary outages below the limits do not lose data.

### 10.6 Retry and triggers

Backoff is full-jitter exponential: base 1 second, multiplier 2, cap 5 minutes. Server `Retry-After` is honored when longer. Successful delivery resets the next batch to immediate eligibility.

Triggers:

- durable enqueue;
- startup recovery;
- periodic timer;
- renderer `online` transition through a narrow IPC event;
- `powerMonitor` resume;
- application/window focus;
- Trace token becomes available or nears expiry;
- upload 401 forced refresh.

Only one pump per account and one Trace credential request may run at a time. A forced refresh after 401 cannot coalesce into a stale non-forced request; it waits for that request, invalidates its result, then starts one new request.

### 10.7 Segment reclamation and local deduplication

The outbox maintains 30-day receipt tombstones keyed by account, correlation fields, and payload digest. Repeated local submission of identical bytes while pending or recently accepted returns the existing local receipt instead of creating another batch. Tombstones are size-bounded independently from payload storage.

Accepted payloads are marked reclaimable in the journal. A segment is physically removed only after every record in it is accepted, expired, evicted, or moved into a compacted quarantine segment. Gateway-first cancellation leaves no committed payload record; if append bytes reached an unjournaled tail, startup recovery truncates them. This avoids retaining successful payloads while also avoiding rewriting or deleting large blobs synchronously on every acknowledgement.

## 11. Durable Gateway inbox and idempotency

### 11.1 Wire contract

Client adds:

- `Idempotency-Key: <batch_id>`;
- `X-Trace-Payload-SHA256: <hex digest>`.

Gateway responses include:

- `X-Trace-Batch-ID`;
- `X-Trace-Receipt: accepted | duplicate`;
- an OTLP-compatible success body.

`accepted` means the canonical batch is durably owned by the Gateway, not necessarily already visible in Langfuse. This is sufficient for the client to delete its payload because responsibility has crossed a persistent boundary.

### 11.2 Storage

Use bbolt as the single-instance durable embedded transactional store on a persistent Gateway volume, pinned through `go.mod`/`go.sum` and included in dependency/license review. Records are keyed by `account_id + batch_id` and contain:

- installation/session identity;
- payload digest;
- canonical protobuf bytes and correlation headers;
- accepted time and monotonic sequence;
- delivery state, attempts, last error, next retry;
- stored receipt outcome.

The unique transaction is the concurrency arbiter:

- absent key: insert and fsync, return `accepted`;
- same key and digest: return stored `duplicate` outcome;
- same key and different digest: return `409 idempotency_conflict` without altering the original.

The key never contains Trace token ID, so rotation and refresh do not change identity.

### 11.3 Delivery worker

The Gateway worker sends accepted batches to Langfuse FIFO. Retryable network/429/5xx failures retain the batch and honor `Retry-After`. Permanent upstream rejection moves the batch to server quarantine and raises diagnostics.

After upstream success, remove canonical payload bytes but retain the receipt for at least 30 days. Accepted but undelivered payloads are never evicted automatically. When the Gateway volume reaches its configured safety threshold, reject new acceptance with `507 storage_unavailable`; clients retain their local batches.

This durable inbox closes the upstream commit-ambiguity window from the client's perspective. Stable OTLP Trace/span IDs remain unchanged on every retry and provide downstream convergence defense in depth.

## 12. Security and privacy

- Passwords never persist.
- Native Session and Trace tokens are opaque, high entropy, OS-keychain protected client-side, digest-only server-side, and excluded from logs.
- `account_id` is authorization identity; username is display metadata only.
- Outbox content is encrypted at rest and separated by account namespace. Another signed-in account cannot enumerate, decrypt, or upload it.
- Sign-out preserves outbox payloads but removes the ability to upload them until the same account authenticates again.
- Explicit account/Session revocation quarantines that account's pending payloads and stops upload. It does not delete conversations.
- Gateway validates body size, media type, protobuf, schema, correlation headers, digest, and idempotency key before durable acceptance.
- Gateway uses trusted proxy configuration for client-source headers and never trusts arbitrary forwarded identity.
- Existing credential and raw-audio redaction remains mandatory before durable storage and again before Langfuse forwarding.
- Logs and diagnostics contain only batch IDs, counts, sizes, status classes, and non-secret identity IDs; never payloads or credentials.

## 13. Failure behavior

| Failure | Local capability | Local outbox | Cloud action |
|---|---|---|---|
| timeout/DNS/proxy/offline | continues | retains | retry |
| auth 429/5xx/malformed | continues | retains | retry validation/token |
| ordinary Web Session expiry | continues | retains | native Session remains authoritative |
| unknown native credential | continues degraded | retains | pause and revalidate; no implicit revoke |
| explicit session/account revoke | stops | quarantines, preserves | stop upload |
| Trace token issue failure | continues | retains | retry single-flight |
| ingest 401 | continues | retains current batch | refresh Trace token and resend |
| Gateway auth unavailable 503 | continues | retains | retry |
| Gateway full 507 | continues | retains | retry/operator action |
| Gateway accepted, Langfuse down | continues | client may delete after durable receipt | Gateway retries |
| local disk write failure/full | continues | controlled oldest eviction at hard limit or producer failure | diagnostics |
| crash/restart | restores from cache | scans and repairs atomic records | resume FIFO |
| sign-out | stops | preserves account-isolated data | best-effort revoke |

## 14. Migration and compatibility

### 14.1 Server identity migration

- Backfill AccountIdentity UUIDs for existing users.
- Continue returning legacy `platform_user_id` during a compatibility window while adding `account_id`.
- New traces use UUID account identity. Personal dashboard queries temporarily match both legacy integer ID and UUID until historical re-attribution or the compatibility window ends.
- Existing Trace tokens remain valid until their normal 15-minute expiry; new introspection fields are additive after Gateway tolerance deployment.

### 14.2 Client migration

- Legacy cookie-only records restore local capability offline but cannot upload until upgraded online.
- Online upgrade issues a native Session and atomically writes the new credential record before removing the legacy record.
- Existing in-memory Trace batches cannot be recovered across upgrade; only batches produced after durable-outbox deployment receive the new guarantee.

### 14.3 Gateway rollout order

1. Deploy parser tolerance and response compatibility.
2. Deploy auth-service account/native-Session models and structured introspection.
3. Deploy durable Gateway inbox/idempotency using `account_id` with legacy identity fallback during migration.
4. Deploy client authentication continuity and durable outbox.
5. Remove legacy protocol paths only after packaged-client adoption evidence.

Rollback keeps every additive database field/table and stops using new routes; it never deletes identity, Session, receipt, inbox, outbox, or conversation data.

## 15. Parallel development and integration topology

Two implementation streams run independently from clean main refs:

### Stream A: authentication continuity

- Client worktree/branch: `feature/auth-continuity` from `agent-hermes-client/main`.
- Server worktree/branch: `feature/auth-continuity-protocol` from `agent-langfuse-server/main`.
- Owns account/native Session protocol, cached local authorization, revocation classification, backend/AuthGate lifecycle, and sign-out invariants.

### Stream B: Trace continuity

- Client worktree/branch: `feature/trace-outbox` from `agent-hermes-client/main`.
- Platform worktree/branch: `feature/trace-ingest-continuity` from `ansatz-agent-platform/origin/main` plus this design/plan.
- Owns durable encrypted outbox, retry triggers, token-only 401 recovery, durable Gateway inbox, idempotent receipts, and Langfuse delivery worker.

The client streams must avoid unrelated overlap. Stream B implements modules and narrow integration seams; final lifecycle wiring occurs on `feature/auth-trace-continuity`, based on client `main`, by applying both reviewed streams and resolving only contract-level overlap.

No worktree may use or inspect `fix/relay-token-cost` changes as implementation input.

## 16. Test strategy

All behavior changes follow one-test RED, observed expected failure, minimal GREEN, refactor while green.

### 16.1 Client authentication

- cached restart starts backend while service is unreachable;
- timeout, DNS/network, 429, 5xx, malformed response, bridge crash, and Web Session expiry retain scope/backend/conversation tree;
- server recovery silently returns validation health to online;
- structured account/session revoke tears down capability once;
- ordinary Trace-token failure never changes local authorization;
- sign-out clears credentials and access while SessionDB, attachments, and conversations remain byte-for-byte present;
- legacy cache offline migration and online upgrade preserve identity isolation.

### 16.2 Client outbox

- local loopback success happens only after either a matching Gateway durable receipt or an fsync-backed local write;
- with no older backlog, Gateway acceptance and local commit race on the same batch ID; the winner is crash-safe and successful payload bytes are not retained locally;
- with older backlog, new batches persist locally and cannot bypass FIFO;
- crash during temp write, rename, manifest update, send, and ack recovers deterministically;
- quit/restart/device restart replays FIFO;
- account namespaces cannot decrypt or send one another's batches;
- capacity, retention, eviction, quarantine, and diagnostic counters are deterministic;
- retry triggers and backoff honor server `Retry-After`;
- concurrent token requests are single-flight; forced 401 refresh cannot return stale credentials;
- accepted/duplicate deletes exactly one payload; all failures retain it;
- identical local submission is deduplicated by pending/receipt tombstones.

### 16.3 Auth service

- AccountIdentity backfill is unique and immutable;
- Django PK delete/recreate cannot inherit the prior account UUID;
- native Session issue stores only token digest and binds installation;
- active, sign-out, per-Session revoke, account disable, and account revoke return exact structured states;
- Session revoke does not affect another Session for the same account;
- Web Session expiry does not expire native Session;
- Trace issuance and introspection classify refreshable versus explicit-revocation failures;
- legacy routes remain compatible during rollout.

### 16.4 Gateway

- introspection ignores additive fields and still rejects malformed required fields;
- auth outage maps to 503, refreshable token failure to 401, explicit revoke to 403;
- same account/batch/digest across token rotation, concurrent requests, and process restart returns duplicate without a second durable record;
- same account/batch with a different digest returns 409;
- accepted response is returned only after durable transaction sync;
- Gateway restart resumes pending FIFO delivery;
- Langfuse response loss does not require a second client batch;
- storage-full rejects before acceptance so the client retains its payload;
- logs contain no credentials or payload content.

### 16.5 Cross-repository contract and E2E

- one login produces account/session identity, starts local Hermes, stores a Trace, obtains a token asynchronously, receives a durable Gateway receipt, and eventually appears in Langfuse;
- auth service and Gateway are stopped while the client restarts and continues a local conversation;
- a Trace whose Gateway race does not win remains on disk through restart and uploads automatically after recovery; Gateway-accepted payloads are absent or promptly reclaimed locally;
- ingest 401 rotates only Trace token;
- explicit Session revoke stops local capability without deleting conversation data;
- retry produces no duplicate logical Gateway batch or model/tool execution.

## 17. Acceptance criteria

Completion requires fresh evidence for every item:

1. All work is based on the pinned main refs and no `fix/relay-token-cost` content appears in diffs.
2. Client restores cached authorization and starts Hermes offline.
3. Every listed transient failure preserves backend, scope, conversation, and AuthGate mounting.
4. Only sign-out or structured account/current-Session revocation stops local capability.
5. Sign-out and revocation preserve SessionDB, attachments, and local conversations.
6. Trace-token acquisition is asynchronous and never blocks backend start.
7. Every locally acknowledged Trace batch has one proven durable owner first: matching Gateway receipt or encrypted local fsync; Gateway-accepted payload bytes are not retained locally.
8. Pending batches survive quit/crash/restart and automatically resume from every required trigger.
9. FIFO, single-flight, 401 refresh, retry, limits, retention, quarantine, and controlled degradation match this spec.
10. Server exposes immutable account UUID and durable, structured native Session revocation.
11. Gateway accepts idempotent batches independently of short-lived token, persists before receipt, survives restart, and retries Langfuse.
12. Cross-token/concurrent/restart/lost-response retries create one logical Gateway batch.
13. Targeted client, auth-service, Gateway, contract, and cross-repository E2E tests pass with full output and exit status inspected.
14. Secret scan and diff audit show no credentials, payload fixtures containing real user data, unrelated refactors, or prohibited branch content.

## 18. Operational handoff

Implementation updates:

- auth/native-Session API contract and administrative revoke procedure;
- Gateway durable-volume sizing, backup, diagnostics, compaction, and recovery runbook;
- client outbox location, encryption/key-loss behavior, retention, capacity, and diagnostic meanings;
- rollout order, compatibility window, rollback, and E2E report;
- project progress and file index only after evidence exists.

## 19. Resolved decisions

No implementation decision remains open at the design gate:

- natural Session/Web Session expiry does not stop local capability;
- only explicit sign-out/account disable/account revoke/current-Session revoke is terminal;
- local outbox limit is 2 GiB and 30 days per account, with a 1 GiB-or-5% free-volume reserve;
- hard-limit degradation drops oldest unsent data with durable visible diagnostics while preserving conversation;
- account identity is an immutable UUID;
- native Session is explicit-revocation based and OS-keychain stored;
- outbox uses Brotli-compressed, per-account AES-256-GCM append-only segments with an OS-wrapped key and a reconstructable checksummed journal;
- Gateway uses durable inbox acceptance and token-independent `batch_id` receipts;
- accepted Gateway ownership is the client deletion boundary;
- online admission races Gateway durable ownership against local fsync only when no older FIFO backlog exists; successful payload bytes are cancelled or reclaimed locally;
- the three repositories and two parallel streams are fixed above.
