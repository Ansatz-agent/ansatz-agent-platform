# Unified Ansatz client Trace entrypoints

Date: **2026-09-02**
Status: **Approved by the user on 2026-09-02**
Client baseline: `origin/main@f1163847de988042e20468ff079485e3e9426605`

## 1. Classification and decision

This is an architectural change. It changes the authority and process boundary
for authenticated Trace admission across the Ansatz CLI, standalone Web
Dashboard, and Electron Desktop. Production code must not be changed until this
written design is approved.

The selected design is a single product Trace service owned by the existing
native authentication owner process. Every producer obtains a short-lived,
entrypoint-bound loopback ingress lease from that owner. The owner, not the
producer, owns Trace-token refresh, encrypted durable storage, FIFO recovery,
Gateway receipt validation, and account/session binding.

The exact public entrypoint values are:

- classic CLI, one-shot CLI, and standalone TUI: `cli`;
- `ansatz dashboard` and its browser chat/backend work: `dashboard`;
- Electron Desktop renderer chat, Voice, and its headless `ansatz serve`
  backend: `desktop` for Desktop chat and `voice` only for an explicitly
  Voice-scoped producer.

There is no default entrypoint. Missing, ambiguous, inherited, or unsupported
entrypoint identity disables Trace admission for that producer and reports a
payload-free diagnostic; it never silently becomes `desktop`.

## 2. Evidence from the current code

The design is based on CodeGraph exploration and current source at the recorded
baseline:

1. `hermes_cli/client_auth/runtime.py` is already the shared account authority.
   CLI, Dashboard, and Desktop use its cached native Session, authorization
   scope, and Trace-token operation.
2. `agent/ansatz_trace_policy.py` currently accepts product Trace only when
   `ANSATZ_TRACE_ENTRYPOINT` is `desktop` or `voice`.
3. `apps/desktop/electron/trace-forwarder.ts` is the only durable Trace broker.
   Its headerless OTLP path derives session/run correlation from protobuf but
   hard-codes `entrypoint: 'desktop'`.
4. `apps/desktop/electron/main.ts` owns token refresh, `safeStorage` key
   protection, outbox creation, retries, and Gateway receipts. CLI and
   standalone Dashboard never instantiate those facilities.
5. `config/ansatz-voice-trace/plugins.toml` sends OTLP/HTTP protobuf to a
   loopback endpoint with a static local bearer. It does not itself establish a
   trustworthy entrypoint.
6. Trace Gateway already accepts `cli`, `dashboard`, `desktop`, and `voice`.
   `internal/otlp.Canonicalize` removes forged canonical attributes at resource,
   scope, span, event, and link levels and writes `hermes.entrypoint` from the
   validated `X-Trace-Entrypoint` header.
7. Gateway canonicalization tests exercise `voice` and `desktop`, but there is
   no three-entrypoint end-to-end contract proving identity binding, offline
   recovery, retry, deduplication, and canonical entrypoint preservation.

The current implementation therefore has two independent defects: missing
durable upload for CLI/Dashboard, and an unsafe Desktop fallback that can
mislabel an unidentified producer.

## 3. Goals

- Make CLI, standalone Dashboard, and Desktop use one authentication owner,
  native Session, Trace-token provider, OTLP/HTTP protobuf contract, encrypted
  durable outbox, retry scheduler, idempotency key, and Gateway receipt parser.
- Preserve a stable `account_id`, `session_id`, and installation binding from
  local admission through Gateway introspection and canonical OTLP output.
- Bind each loopback producer credential to exactly one explicit entrypoint so
  producer headers or payload attributes cannot upgrade, downgrade, or relabel
  it.
- Keep local Agent work available during authentication, Trace-token, Gateway,
  and Langfuse outages.
- Acknowledge local OTLP only after either the local encrypted transaction or a
  matching durable Gateway receipt owns the batch.
- Recover pending work after producer exit, owner restart, machine restart, and
  network restoration without replaying model or tool work.
- Preserve FIFO, stable batch identity, 401 token-only refresh, duplicate
  receipts, bounded retention, and payload-free receipt tombstones.
- Add behavioral end-to-end contract tests for all three entrypoints and
  Gateway canonicalization.

## 4. Non-goals

- No production deployment, installer publication, push, pull request, or
  remote mutation.
- No change to Langfuse query/UI behavior or the account/native-Session server
  protocol unless a failing contract proves an additive server field is
  required.
- No redesign of Hermes SessionDB or conversation persistence.
- No browser-held Trace token, native Session secret, outbox key, or raw OTLP
  payload.
- No support for arbitrary third-party OTLP senders. The forced product path
  remains sealed and product-runtime-only.
- No preservation of the unsafe behavior where an unidentified producer is
  labeled `desktop`.
- No plaintext fallback when OS-protected key storage is unavailable.

## 5. Requirements and invariants

### 5.1 Identity

- The authentication owner is authoritative for the current cached principal,
  native Session, installation ID, epoch, and explicit revocation state.
- Outbox namespaces are keyed by immutable `account_id`, never username or a
  short-lived Trace token.
- A batch stores the owner account/session/installation tuple captured at local
  admission. Upload rechecks that tuple after every await and before accepting
  a receipt.
- Sign out or matching structured revocation stops new admission and upload but
  preserves encrypted pending data. Re-authentication by the same account may
  resume it; another account cannot enumerate, decrypt, or upload it.

### 5.2 Entrypoint

- Entrypoint is an enum, not a caller-controlled free string.
- The raw command/surface resolver must return an explicit value before opening
  an ingress lease.
- The owner issues a random local bearer whose server-side lease record contains
  the entrypoint. The OTLP request cannot override that binding.
- The broker writes `X-Trace-Entrypoint` from the lease record. It ignores and
  rejects conflicting producer metadata.
- The sealed Relay config includes `hermes.entrypoint` as producer evidence for
  diagnostics, but Gateway authority remains the broker header.
- The existing `traceMetadata()` Desktop fallback is removed. Compatibility
  code must receive an explicit entrypoint constructor argument.

### 5.3 Durability and idempotency

- A UUIDv4 `batch_id` and SHA-256 digest are assigned before the durability
  race. Every retry uses the same values and original protobuf bytes.
- Local success is returned only after one durable owner exists: committed,
  encrypted local storage or a matching Gateway `accepted|duplicate` receipt.
- Gateway success is valid only when HTTP is successful and both
  `X-Trace-Batch-ID` and `X-Trace-Receipt` match the outstanding batch.
- A missing/malformed receipt is retryable and never deletes local payload.
- A 401 invalidates only the Trace token and retries the same batch after one
  single-flight refresh. It never changes local login state.
- Retry and upload are FIFO per account. A newer batch cannot bypass an older
  sendable batch.

### 5.4 Availability

- Trace service startup and Trace-token acquisition are not prerequisites for
  an Agent turn.
- CLI exit and Dashboard/desktop backend restarts do not discard pending data.
- The auth owner remains alive while an outbox contains pending work even when
  no interactive consumer lease remains.
- Recovery triggers include durable enqueue, owner startup, retry deadline,
  network recovery, producer reconnect, token readiness/near-expiry, and upload
  401.

## 6. Architecture

```text
CLI (`cli`) ───────────────┐
Dashboard (`dashboard`) ──┼─ open_trace_ingress(entrypoint)
Desktop (`desktop`) ──────┘
                              │
                              v
native auth owner process
  ├─ cached principal + native Session
  ├─ stable product installation ID
  ├─ entrypoint-bound loopback bearer leases
  ├─ OTLP/HTTP protobuf ingress and correlation parser
  ├─ account-keyed AES-256-GCM durable outbox
  ├─ single-flight Trace-token provider
  ├─ FIFO retry/recovery scheduler
  └─ strict Gateway receipt validator
                              │
                              v
Trace Gateway durable inbox (`account_id`, `batch_id`)
  ├─ accepted/duplicate receipt
  ├─ canonical identity + `hermes.entrypoint`
  └─ asynchronous Langfuse delivery
```

The auth owner is selected because it is already the only process shared by all
three product surfaces and already owns native Session secrets, validation
health, account epochs, and Trace-token issuance. Moving the broker there
removes the Electron-only authority without exposing credentials to Python
Agent code, the renderer, or the browser.

## 7. Shared product Trace service

### 7.1 Modules

Add a focused package under `hermes_cli/client_auth/trace/`:

- `identity.py`: explicit entrypoint enum, installation identity, account
  binding, and strict wire validation;
- `otlp.py`: bounded OTLP envelope parsing, session/run correlation, and
  resource attribute validation;
- `crypto.py`: account-bound data-key generation, OS-keyring wrapping, and
  AES-256-GCM records with authenticated metadata;
- `outbox.py`: crash-safe account-local journal/database, dedupe, receipt
  tombstones, retention, capacity, quarantine, and recovery;
- `gateway.py`: Trace-token single-flight, HTTP upload, retry classification,
  exact headers, and receipt validation;
- `service.py`: loopback HTTP listener, entrypoint-bound bearer leases,
  durability race, lifecycle, and scheduler;
- `protocol.py`: bounded auth-owner request/response frames for ingress leases
  and payload-free diagnostics.

The modules remain independent of Electron, FastAPI, renderer code, and
SessionDB. The product Trace package is imported lazily only for the sealed
Ansatz runtime so upstream Hermes behavior remains unchanged.

### 7.2 Installation identity

Use one stable product installation UUID stored through the auth owner's
OS-protected credential record and exposed only as a public identifier. Existing
Desktop installations migrate their current installation UUID into the shared
record only after validating it as canonical UUIDv4. A disagreement between two
existing trusted sources is a migration error requiring explicit recovery; it
must not select one silently.

CLI and Dashboard login pass this installation/client context so they use the
same native Session upgrade path as Desktop. The server may still issue separate
native Sessions after an explicit account switch, but all product surfaces use
the same runtime owner and current account authority.

### 7.3 Entrypoint-bound ingress leases

The auth runtime protocol adds:

```json
{
  "version": 4,
  "operation": "trace_ingress_open",
  "entrypoint": "cli | dashboard | desktop | voice",
  "consumer_id": "bounded-random-id"
}
```

The owner returns only non-cloud credentials:

```json
{
  "version": 4,
  "ok": true,
  "ingress": {
    "endpoint": "http://127.0.0.1:<port>/v1/traces",
    "authorization": "Bearer <local-random-secret>",
    "installation_id": "uuid-v4",
    "plugins_toml": "<validated sealed path>",
    "entrypoint": "cli | dashboard | desktop | voice"
  }
}
```

The local secret is random, rotates on owner restart and account epoch change,
and maps to a server-side lease. It is never written to config, logs, browser
state, or outbox. A lease can post only from loopback, only to `/v1/traces`,
only protobuf with identity encoding, and only under its bound entrypoint.

### 7.4 Producer integration

- CLI bootstraps a `cli` lease after authorization and before Relay activation.
  Failure is diagnostic-only and does not prevent Agent construction.
- Standalone `ansatz dashboard` bootstraps a `dashboard` lease after final
  profile re-exec/routing and before in-process Agent creation.
- Electron requests a `desktop` lease through the existing auth bridge and
  registers it with each current/future `ansatz serve` backend using the
  existing bounded stdin control channel. Voice requests a distinct explicit
  `voice` lease.
- Headless `serve` never guesses. It receives Desktop transport registration or
  runs without product Trace.
- The Relay product config is generated from the sealed template with the
  lease endpoint, local Authorization variable, installation ID, and explicit
  `hermes.entrypoint` resource attribute.

### 7.5 Encrypted outbox

The owner stores account-isolated state under:

```text
<HERMES_HOME>/telemetry/ansatz-traces/<account-id>/
  outbox.db
  outbox.db-wal
  outbox.db-shm
```

SQLite provides transactional ordering and crash recovery with WAL and
`synchronous=FULL`. Metadata needed for scheduling and bounded diagnostics is
plaintext; OTLP bodies are Brotli-compressed and then AES-256-GCM encrypted.
Authenticated additional data binds ciphertext to account/session/installation,
batch ID, entrypoint, session/run IDs, schema version, sequence, and digest.

The random per-account data key is stored only in the OS keyring using the same
product namespace and account binding as native authentication. No raw or
reversibly encoded key is written to disk. If the keyring is unavailable, the
service rejects local Trace admission as unavailable and preserves local Agent
work; it never writes plaintext.

Limits remain 2 GiB and 30 days per account, 8 MiB per logical OTLP batch, with
the greater of 1 GiB or 5% volume free-space reserve. Oldest-pending controlled
eviction, encrypted quarantine, payload-free receipt tombstones, and counters
retain the current continuity contract. WAL checkpoint/incremental vacuum runs
only at an idle boundary and never shares SessionDB locks.

### 7.6 Desktop outbox migration

Existing Electron `safeStorage` ciphertext cannot be decrypted safely by the
new Python owner. Desktop therefore uses a bounded compatibility drain:

1. stop admitting new batches to the Electron outbox;
2. keep the existing Electron forwarder only as a legacy reader/uploader for
   already-persisted namespaces;
3. route every new OTLP producer to the shared owner service;
4. delete no legacy namespace automatically;
5. after it reaches zero pending/quarantined records, record a payload-free
   migration marker and leave destructive cleanup to an explicitly authorized
   later maintenance action.

The compatibility drainer is not a fourth active upload implementation. It is
read-only with respect to new Trace admission and exists solely to avoid losing
already-encrypted payloads during the format/authority migration.

## 8. OTLP and Gateway contract

The shared uploader sends the existing protocol exactly:

- `Content-Type: application/x-protobuf`;
- `Idempotency-Key: <uuid-v4 batch_id>`;
- `X-Trace-Payload-SHA256: <lowercase hex>`;
- `X-Hermes-Session-ID`;
- `X-Trace-Run-ID`;
- `X-Trace-Entrypoint` from the ingress lease;
- `X-Telemetry-Schema-Version: 1`;
- `Authorization: Bearer <short-lived Trace token>`.

Gateway remains authoritative for canonicalization. It must remove forged
canonical keys throughout the OTLP tree and append the introspected
account/installation identity plus the validated entrypoint header. Tests must
prove `cli`, `dashboard`, and `desktop` survive unchanged and cannot be replaced
by forged payload attributes.

## 9. Security and privacy

- Native Session and Trace tokens remain in the auth owner and outbound uploader
  only; producer processes receive only a loopback-local random bearer.
- Browser and renderer APIs expose payload-free state and diagnostics only.
- The listener binds strictly to `127.0.0.1`, rejects proxy/remote addresses,
  duplicate security headers, unsupported encoding/media, oversized bodies,
  invalid protobuf, unknown local bearer, and conflicting entrypoint evidence.
- The local bearer is entrypoint- and epoch-bound and is not accepted after
  account change, sign out, explicit revocation, or owner restart.
- Payload ciphertext uses unique nonces and authenticated metadata. Key loss,
  account mismatch, header mismatch, or authentication-tag failure quarantines
  the record and never uploads it.
- Logs contain only batch ID, status class, elapsed time, request ID, counts,
  and sizes. They never contain payload, local bearer, native Session token,
  Trace token, password, cookies, or key material.
- Existing product redaction stays before durable storage, and Gateway redacts
  and canonicalizes again before its durable inbox.

## 10. Failure behavior

| Failure | Agent capability | Trace behavior |
|---|---|---|
| Auth service/network/429/5xx malformed response | continues from cached authorization | retain encrypted head; retry |
| Trace-token issue failure | continues | local durable admission; single-flight retry |
| Ingest 401 | continues | invalidate Trace token only; resend same batch |
| Matching structured revocation | stops per auth policy | stop admission/upload; preserve encrypted data |
| Gateway/Langfuse unavailable | continues | local/Gateway durable owner retains and retries |
| Missing or mismatched Gateway receipt | continues | retain payload and retry; never acknowledge cloud ownership |
| Local keyring unavailable | continues | no plaintext admission; payload-free degraded diagnostic |
| Local database/full disk failure | continues | durability race may still succeed via Gateway; otherwise producer receives retryable unavailable |
| Producer exits | unaffected | owner keeps pending queue and retries |
| Owner/machine restarts | restores cached auth | reopen/repair WAL, validate ciphertext, resume FIFO |
| Entrypoint missing/ambiguous | continues | no Trace admission; never default to Desktop |

## 11. Testing strategy

All production changes use one-test RED/GREEN/refactor cycles. Source-reading or
snapshot tests are prohibited.

### 11.1 Unit and component contracts

- table-driven entrypoint resolver tests for CLI, TUI, one-shot, Dashboard,
  headless Desktop backend, and invalid/ambiguous invocation;
- auth-runtime protocol tests proving exact versioned request/response fields,
  entrypoint-bound bearer rotation, account epoch checks, and no cloud secret in
  the response;
- crypto/outbox tests for key wrapping, account binding, nonce uniqueness,
  durable commit-before-ack, crash/WAL recovery, FIFO, capacity/retention,
  quarantine, tombstones, and key loss;
- gateway-client tests for exact headers, timeout/network/429/5xx retry,
  `Retry-After`, 401 single-flight refresh, explicit 403 binding, receipt
  matching, and stable batch ID/digest;
- Relay policy tests proving all explicit entrypoints work and missing identity
  never becomes Desktop.

### 11.2 Three-entrypoint end-to-end contract

A real-import local harness starts one auth owner, one encrypted outbox, and one
Gateway-compatible HTTP server. It launches the actual CLI, Dashboard backend,
and Desktop backend registration seams against isolated `HERMES_HOME` and
asserts for each entrypoint:

1. the same account/session/installation binding is used;
2. local OTLP is acknowledged only after durable ownership;
3. an offline batch survives producer and owner restart;
4. recovery retries without re-running the Agent turn;
5. lost response and token rotation retain one stable batch ID;
6. Gateway `duplicate` drains the local payload;
7. canonical OTLP contains exactly the expected `hermes.entrypoint`;
8. a forged payload `hermes.entrypoint=desktop` cannot relabel CLI or Dashboard;
9. another account cannot upload the first account's backlog.

The harness uses real protobuf bytes and real filesystem/key-protector adapters;
network and clock boundaries may be controlled deterministically.

### 11.3 Gateway contracts

Parameterize server/canonicalization integration tests across `cli`,
`dashboard`, and `desktop`. For each value, assert durable accepted/duplicate
receipts, identity binding, forged canonical attribute removal at every level,
and the final resource attribute. Retain existing `voice` coverage separately.

## 12. Acceptance criteria

Completion requires fresh evidence for every item:

1. Development branch/worktree is based on the recorded latest client
   `origin/main` baseline and canonical `main` remains clean/read-only.
2. CLI, Dashboard, and Desktop all obtain product Trace ingress from the same
   auth-owner service and no active path bypasses it.
3. All three use one implementation of token refresh, encrypted outbox, FIFO
   retry, idempotency, and Gateway receipt validation.
4. `hermes.entrypoint` is exactly `cli`, `dashboard`, or `desktop` for the
   corresponding end-to-end test; missing identity never defaults to Desktop.
5. Identity binding, offline restart, same-process recovery, retry, response
   loss, token rotation, duplicate receipt, and cross-account denial are proven
   for every entrypoint.
6. Gateway canonicalization removes forged values and writes the validated
   entrypoint for every entrypoint.
7. Local Agent capability remains usable during every tested auth/token/upload
   outage.
8. Existing Desktop pending encrypted batches have a non-destructive migration
   path and new batches cannot enter the legacy outbox.
9. Focused tests, complete auth-runtime Python suite, relevant Relay/plugin
   suites, complete Electron suite, typecheck/lint, Gateway Go tests/race/vet,
   and secret scans pass with full output inspected.
10. No commit, push, PR, deployment, installer publication, destructive cleanup,
    or production mutation occurs without separate user authorization.

## 13. Rollout and compatibility

1. Land additive auth-runtime protocol support and the shared service behind the
   sealed product marker.
2. Add CLI and Dashboard producers while Desktop continues its current path.
3. Switch Desktop new admission to the shared service and enable the legacy
   read-only drainer.
4. Add Gateway three-entrypoint contracts; no production protocol change is
   expected unless TDD exposes a defect.
5. Run local cross-entrypoint and restart evidence.
6. A later explicitly authorized release must deploy server compatibility
   first, then package one reviewed client commit, and retain a known rollback
   artifact.

Older Desktop builds continue using the existing Gateway wire contract. The
Gateway already accepts all entrypoint values, so the client migration is
additive. Auth runtime protocol negotiation keeps older owners usable for local
authorization; Trace ingress fails closed as unavailable until the upgraded
owner starts.

## 14. Rollback

- Disable shared ingress activation while leaving the auth owner and encrypted
  database untouched.
- Restore Desktop admission to the existing Electron path only in a reviewed
  rollback build; never relabel CLI/Dashboard as Desktop.
- CLI/Dashboard continue local Agent work without Trace when shared ingress is
  unavailable.
- Do not delete either shared or legacy outboxes during rollback.
- Gateway needs no rollback if its wire behavior remains unchanged; test-only
  changes are harmless.

## 15. Resolved decisions and design gate

Resolved here:

- shared authority is the native auth owner, not Electron and not each Agent
  process;
- the product uses entrypoint-bound local bearer leases, not trusted producer
  headers;
- new durable storage is one Python implementation shared by all three
  entrypoints;
- Desktop's old encrypted records drain non-destructively rather than being
  decrypted or deleted by the new owner;
- Gateway remains the canonical identity/entrypoint authority;
- no entrypoint default exists.

The user approved this spec and all subsequent local plans/operations on
2026-09-02, while requiring continuous execution. The approval does not change
the task's explicit prohibition on automatic commits, pushes, pull requests, or
deployments. The implementation plan must name every file/interface, the
one-test RED/GREEN sequence, expected command outcomes, and verification gates.
