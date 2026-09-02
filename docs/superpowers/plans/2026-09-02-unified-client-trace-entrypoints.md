# Unified Ansatz client Trace entrypoints implementation plan

Date: **2026-09-02**
Status: **Approved for local implementation by the user's standing approval on 2026-09-02**

**Goal:** Make Ansatz CLI, standalone Web Dashboard, and Electron Desktop use
one auth-owner Trace service for identity, Trace tokens, OTLP/HTTP protobuf,
encrypted persistent outbox, FIFO idempotent recovery, and Gateway receipts,
while preserving exact `hermes.entrypoint` values and never defaulting to
Desktop.

**Baselines:** client `origin/main@f1163847de988042e20468ff079485e3e9426605`;
platform `origin/main@72aa64f88c71c49a65f281f2e172db864f937ad7`.

**Architecture:** Extend the existing native authentication owner with a lazy,
sealed-product Trace service. Producers receive only an entrypoint-bound
loopback lease. The owner stores encrypted batches, refreshes Trace tokens,
retries FIFO with stable batch IDs, and validates durable Gateway receipts.

**Constraints:** TDD one failing behavior at a time. Use `bash
scripts/run_tests.sh`, never direct pytest. Use the shared Conda `dl` interpreter
when invoking Python outside repository test wrappers. Do not commit, push,
create a PR, deploy, delete legacy outboxes, or mutate production.

## Task 1: Establish explicit entrypoint and installation identity contracts

**Files**

- Create `hermes_cli/client_auth/trace/__init__.py`
- Create `hermes_cli/client_auth/trace/identity.py`
- Create `tests/hermes_cli/client_auth/trace/test_identity.py`
- Modify `agent/ansatz_trace_policy.py`
- Modify `tests/agent/test_ansatz_trace_policy.py`

**RED**

1. Add a table-driven test for `TraceEntrypoint.parse()` accepting exactly
   `cli`, `dashboard`, `desktop`, and `voice`, with no default for `None`, empty,
   inherited garbage, or unknown values.
2. Add tests for a canonical UUIDv4 `TraceInstallationIdentity` and rejection of
   missing/non-v4/non-canonical values.
3. Add a product policy test proving `cli` and `dashboard` activate the sealed
   config and missing entrypoint does not activate or become Desktop.
4. Run:

   `bash scripts/run_tests.sh tests/hermes_cli/client_auth/trace/test_identity.py tests/agent/test_ansatz_trace_policy.py -q`

   Expected RED: module/import or new behavior assertions fail.

**GREEN**

Implement immutable enums/value objects, extend product policy validation to
the explicit enum, and inject `hermes.entrypoint` into the copied sealed Relay
resource attributes. Update the sealed config hash only after the config file
changes in Task 7.

Expected GREEN: focused files pass; no existing Desktop/Voice policy regression.

## Task 2: Implement account-bound encryption without plaintext fallback

**Files**

- Create `hermes_cli/client_auth/trace/crypto.py`
- Create `tests/hermes_cli/client_auth/trace/test_crypto.py`
- Modify `hermes_cli/client_auth/runtime.py` only to expose a narrow injected
  secure-secret adapter; do not expose native credentials.

**Interfaces**

- `TraceKeyProtector.available() -> bool`
- `TraceKeyProtector.wrap(account_id, key) -> bytes`
- `TraceKeyProtector.unwrap(account_id, wrapped) -> bytes`
- `encrypt_record(key, metadata, plaintext) -> EncryptedRecord`
- `decrypt_record(key, metadata, record) -> bytes`

**RED**

Test random 256-bit keys, unique 96-bit nonces, AES-256-GCM round trip,
authenticated-metadata tamper failure, account mismatch, key loss, and secure
backend unavailable behavior. Assert no plaintext bytes occur in encoded
records.

Run `bash scripts/run_tests.sh tests/hermes_cli/client_auth/trace/test_crypto.py -q`.
Expected RED: missing implementation.

**GREEN**

Use lazy `cryptography.hazmat.primitives.ciphers.aead.AESGCM` imports. Store the
per-account data key only via an injected keyring-backed adapter. Never fall
back to a file or base64-only key.

## Task 3: Build the crash-safe encrypted FIFO outbox

**Files**

- Create `hermes_cli/client_auth/trace/outbox.py`
- Create `tests/hermes_cli/client_auth/trace/test_outbox.py`

**Interfaces**

- `TraceEnvelope` and `DurableTraceBatch`
- `TraceOutbox.open(root, owner, key_protector, clock, disk_usage)`
- `begin_enqueue()`, `commit()`, `peek_eligible()`, `mark_retry()`,
  `acknowledge()`, `quarantine()`, `diagnostics()`, `compact_if_idle()`,
  `close()`

**RED sequence**

1. Commit-before-local-ack and recovery after abrupt reopen.
2. AES ciphertext/account metadata binding and wrong-account denial.
3. Stable UUIDv4 batch ID and dedupe key across repeated identical admission.
4. Strict FIFO and persisted retry eligibility.
5. Accepted/duplicate payload removal plus payload-free tombstone.
6. 2 GiB/30-day/8 MiB/free-reserve limits and encrypted quarantine.
7. Key loss and corrupt/torn database fail closed without plaintext recovery.

After each test, run the focused file and confirm the expected failure before
minimal implementation. Use SQLite WAL, `synchronous=FULL`, explicit
transactions, and Brotli-before-AES. If `brotlicffi` is not importable in the
declared client runtime, stop and report the exact missing import before any
dependency mutation.

Expected final command:

`bash scripts/run_tests.sh tests/hermes_cli/client_auth/trace/test_outbox.py -q`

Expected GREEN: all outbox contracts pass and reopen proves persisted state.

## Task 4: Implement strict Gateway upload and receipt handling

**Files**

- Create `hermes_cli/client_auth/trace/gateway.py`
- Create `tests/hermes_cli/client_auth/trace/test_gateway.py`

**Interfaces**

- `TraceCredentialProvider.current(force=False)` with single-flight semantics
- `GatewayUploader.send(batch) -> Accepted | Duplicate | Retry | Quarantine`
- `next_retry(attempt, now, Retry-After, random)`

**RED sequence**

1. Exact Trace headers, content type, original protobuf, installation binding,
   and entrypoint from the batch.
2. Matching `accepted` and `duplicate` receipts only.
3. Missing/mismatched receipt retains payload.
4. Network, timeout, 429, 5xx, and auth-unavailable retry with full jitter and
   bounded `Retry-After`.
5. 401 waits for the current token flight, invalidates it, starts exactly one
   forced refresh, and resends the same batch ID/body.
6. Structured 403 is terminal only when account/session identity matches.

Run `bash scripts/run_tests.sh tests/hermes_cli/client_auth/trace/test_gateway.py -q`.
Expected GREEN: retry classifications and receipt ownership are exact.

## Task 5: Implement OTLP ingress and the durability race

**Files**

- Create `hermes_cli/client_auth/trace/otlp.py`
- Create `hermes_cli/client_auth/trace/service.py`
- Create `tests/hermes_cli/client_auth/trace/test_otlp.py`
- Create `tests/hermes_cli/client_auth/trace/test_service.py`

**Interfaces**

- `derive_correlation(export_request) -> session_id, run_id`
- `TraceIngressLease(endpoint, local_authorization, installation_id,
  entrypoint, plugins_toml)`
- `TraceService.open_ingress(entrypoint, consumer_id)`
- `TraceService.pump()`, `next_retry_at()`, `diagnostics()`, `close()`

**RED sequence**

1. Parse real OTLP protobuf and derive bounded session/run IDs.
2. Reject remote source, wrong route/method/media/encoding, duplicate headers,
   oversize body, invalid protobuf, and unknown bearer.
3. Prove one bearer is bound to exactly one explicit entrypoint; conflicting
   payload/resource evidence cannot relabel it.
4. Prove there is no headerless/Desktop fallback.
5. Race local durable commit against matching Gateway receipt and respond only
   after one succeeds; both failures return unavailable.
6. Backlog disables the cloud fast path and preserves FIFO.
7. Scheduler resumes after service/owner restart and does not require producer
   restart.

Use loopback `ThreadingHTTPServer` or an equally bounded stdlib server with
explicit shutdown and request-size limits. Do not add FastAPI or browser
dependencies to the auth owner.

Expected focused GREEN:

`bash scripts/run_tests.sh tests/hermes_cli/client_auth/trace/test_otlp.py tests/hermes_cli/client_auth/trace/test_service.py -q`

## Task 6: Extend the native auth-owner protocol

**Files**

- Modify `hermes_cli/client_auth/runtime.py`
- Modify `hermes_cli/client_auth/bridge.py`
- Modify `tests/hermes_cli/client_auth/test_runtime.py`
- Modify `tests/hermes_cli/client_auth/test_bridge.py`
- Create `tests/hermes_cli/client_auth/trace/test_runtime_integration.py`

**Protocol**

- Bump auth-owner protocol to v4 with backward negotiation.
- Add `trace_ingress_open(entrypoint, consumer_id)` returning only the local
  ingress lease.
- Bind service lifecycle to the authenticated owner and keep the owner alive
  while pending batches exist.
- Stop/rotate admission on sign out, account switch, matching revocation, epoch
  change, and owner restart; preserve encrypted payload.

**RED sequence**

1. Exact request/response schema and rejection of extra fields.
2. Response contains no native Session or cloud Trace token.
3. Same account/same entrypoint returns a valid current lease; epoch change
   invalidates the old bearer.
4. CLI/Dashboard/Desktop leases share owner/account/outbox yet retain distinct
   entrypoints.
5. Pending outbox prevents owner idle exit and restarts recovery after crash.

Run `bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py tests/hermes_cli/client_auth/test_bridge.py tests/hermes_cli/client_auth/trace/test_runtime_integration.py -q`.

## Task 7: Wire the sealed Relay config and explicit backend transport

**Files**

- Modify `config/ansatz-voice-trace/plugins.toml`
- Modify `agent/ansatz_trace_policy.py`
- Modify `agent/relay_runtime.py`
- Modify `hermes_cli/client_auth/runtime.py`
- Modify `tests/plugins/test_nemo_relay_plugin.py`
- Modify `tests/agent/test_ansatz_trace_policy.py`
- Modify `tests/hermes_cli/client_auth/test_runtime.py`

**RED**

Add behavior tests proving copied Relay config contains the lease-bound
`hermes.entrypoint` for each entrypoint, the sealed hash matches, transport
registration accepts the explicit enum, and missing identity does not activate.

Run focused tests; expected RED on current Desktop-only validators.

**GREEN**

Add the resource attribute placeholder to the sealed config, update its exact
SHA-256 constant, and replace it only after validating the lease. Keep backend
stdin registration exact and backward compatible. No environment-based default.

## Task 8: Wire CLI and standalone Dashboard entrypoints

**Files**

- Create `hermes_cli/client_auth/trace/bootstrap.py`
- Modify `hermes_cli/main.py`
- Modify `cli.py`
- Modify `hermes_cli/oneshot.py` only if the common CLI hook cannot cover it
- Modify `tests/hermes_cli/client_auth/test_entrypoints.py`
- Create `tests/hermes_cli/client_auth/trace/test_bootstrap.py`
- Modify relevant Dashboard command tests under `tests/hermes_cli/`

**RED**

1. Classic CLI, TUI, and one-shot open `cli` leases.
2. Final standalone Dashboard process after profile routing opens `dashboard`.
3. `serve` does not guess Dashboard/Desktop; it requires explicit registered
   transport.
4. Trace bootstrap failure does not block an authorized Agent turn.

**GREEN**

Install one lazy bootstrap hook after auth authorization and before Relay
activation. Carry explicit process surface as data, not a mutable global default.

Run focused entrypoint/bootstrap/Dashboard tests through `bash scripts/run_tests.sh`.

## Task 9: Switch Desktop new admission to the shared service

**Files**

- Modify `apps/desktop/electron/auth-bridge.ts`
- Modify `apps/desktop/electron/auth-bridge.test.ts`
- Create or modify a narrow Desktop Trace lease adapter under
  `apps/desktop/electron/`
- Modify `apps/desktop/electron/main.ts`
- Modify `apps/desktop/electron/backend-env.ts`
- Modify `apps/desktop/electron/backend-env.test.ts`
- Modify `apps/desktop/electron/trace-durability-runtime.ts` and tests only for
  the legacy read-only drain boundary
- Modify `apps/desktop/electron/trace-forwarder.ts` and tests to remove the
  headerless Desktop default and prevent new legacy admission

**RED sequence**

1. Auth bridge parses an exact local ingress lease and rejects secrets/extra
   fields.
2. Desktop backend registration carries explicit `desktop` from the shared
   owner.
3. New OTLP cannot enter the Electron outbox.
4. Existing Electron ciphertext remains readable/drainable, is never deleted
   automatically, and another account cannot drain it.
5. Missing shared lease degrades Trace only and never starts a fallback that
   labels unknown traffic Desktop.

Use the existing Desktop Vitest commands from `apps/desktop/package.json` and
run focused files first. Expected GREEN includes type-safe bridge and backend
registration behavior.

## Task 10: Add three-entrypoint end-to-end contracts

**Files**

- Create `tests/hermes_cli/client_auth/trace/test_three_entrypoint_e2e.py`
- Modify/add Desktop integration harness only where required to execute the real
  registration seam
- Add fixtures under the test package, never under system temporary paths

**Contract for each of `cli`, `dashboard`, `desktop`**

- same authenticated account/session/installation binding;
- correct entrypoint-bound local bearer and Gateway header;
- durable local acknowledgement while Gateway is offline;
- same-process recovery and owner/machine-style restart recovery;
- retry without re-running Agent/model/tool work;
- response loss and token rotation keep one batch ID/body;
- duplicate receipt removes local payload;
- forged `hermes.entrypoint=desktop` cannot relabel CLI/Dashboard;
- another account cannot upload the backlog.

Run the exact E2E file through `bash scripts/run_tests.sh`. Expected final GREEN:
all three parameter rows pass with real imports, protobuf, filesystem, crypto,
and HTTP boundaries.

## Task 11: Expand Gateway canonicalization contracts

**Files**

- Modify `ansatz-agent-platform/services/trace-gateway/internal/otlp/canonicalize_test.go`
- Modify `ansatz-agent-platform/services/trace-gateway/internal/server/server_test.go`
- Modify `ansatz-agent-platform/services/trace-gateway/internal/server/continuity_integration_test.go` only if needed for full receipt/restart proof

**RED**

Parameterize `cli`, `dashboard`, and `desktop`. Assert forged canonical keys are
removed at resource/scope/span/event/link levels and final
`hermes.entrypoint` equals the validated request header. Assert durable
accepted/duplicate receipts and immutable account binding for every entrypoint.

Run `go test ./internal/otlp ./internal/server` from `services/trace-gateway`.
Expected RED: absent three-entrypoint contract or exposed mismatch.

**GREEN**

Prefer test-only expansion if current production behavior is correct. If a test
exposes a defect, make the smallest canonicalization/server fix and rerun.

## Task 12: Verification and completion audit

**Client focused/full proof**

- `bash scripts/run_tests.sh tests/hermes_cli/client_auth/trace/ -q`
- `bash scripts/run_tests.sh tests/hermes_cli/client_auth/ tests/agent/test_ansatz_trace_policy.py tests/plugins/test_nemo_relay_plugin.py -q`
- relevant complete Desktop Vitest project using its declared package script;
- Desktop typecheck and changed-file lint;
- `rtk git diff --check` and secret-pattern scan over changed files.

**Gateway proof**

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`

**Audit**

Map every design acceptance criterion and user requirement to current source
and fresh test output. Confirm both canonical main worktrees remain clean and no
commit/push/PR/deploy occurred. Update platform progress/file index only with
evidence-backed local status; do not claim release or production deployment.
