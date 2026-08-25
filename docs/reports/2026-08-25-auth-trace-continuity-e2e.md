# Authentication and Trace Continuity — Local Verification Report

Date: **2026-08-25**

## Result and delivery state

Authentication continuity and durable Trace upload continuity are implemented
and verified on three local development branches. This is a local automated
verification report: none of these branches has been pushed, used to open a
pull request, packaged for release, or deployed.

| Repository | Local branch | Verified ref |
|---|---|---|
| `agent-hermes-client` | `feature/auth-trace-continuity` | `c179d6659fbbd3d353938b81fc8123d10c5b7cf6` |
| `agent-langfuse-server` | `feature/auth-continuity-protocol` | `b1c920dc190d7ed0ef5ef09ac8a69e29f8986c91` |
| `ansatz-agent-platform` / Trace Gateway | `feature/trace-ingest-continuity` | runtime evidence through `02491c4af89390e093166a5f98eb3110bbebb1fc`; subsequent commits are documentation-only |

The work was based only on the respective `main` baselines selected in the
approved design.

## Implemented behavior

### Authentication continuity

- A locally cached authenticated session restores from the OS-protected
  credential cache before remote validation, including after client restart.
- Timeout, offline/DNS/VPN/proxy failures, 429, 5xx, and malformed or otherwise
  transient authentication responses leave the authorization scope, local
  Hermes backend, and mounted conversation intact.
- Background validation silently recovers when the service becomes available.
- Local capability stops only for user Sign out or trusted, structured,
  identity-matching account/session revocation. Untrusted or mismatched
  terminal-looking responses fail closed as transient service failures.
- Sign out clears credentials and application access but preserves SessionDB,
  attachments, local conversations, and the Trace outbox.
- Trace token acquisition is asynchronous and is not a prerequisite for local
  Hermes startup or local conversation use.

### Trace upload continuity

The client uses a write-ahead encrypted outbox and races its local durability
barrier against a Gateway durable receipt. Relay success is returned only after
one durable owner exists. A Gateway-first `accepted` or `duplicate` receipt
cancels the local payload append; a local-first receipt makes the encrypted
payload reclaimable. Successful Trace/span payload bytes are therefore not
retained locally—only bounded payload-free receipt tombstones remain.

The local storage contract is:

- 2 GiB and 30 days per account;
- Brotli compression followed by AES-256-GCM encryption;
- 64 MiB maximum encrypted record (not a segment-rotation size);
- one account-local `active.segment`, with bounded per-record compaction and a
  free-volume reserve of the greater of 1 GiB or 5%;
- account-bound keys protected by Electron `safeStorage`, with key loss and
  owner mismatch quarantined/failing closed.

Backlog delivery is FIFO. Trace token acquisition is single-flight. New Trace,
startup, network/renderer recovery, system resume, window focus, timers,
token readiness/near-expiry, and upload 401 all trigger recovery. A 401
refreshes only the Trace token and resends the same batch; it does not change
the user's login state. Explicit matching structured revocation pauses upload.

The Gateway durably accepts idempotent `(account_id, batch_id)` batches in a
bbolt inbox before returning a receipt, preserves accepted-but-undelivered
data across restart, wakes delivery for both accepted and duplicate receipts,
and drains FIFO with persisted retry state. Storage/durability unavailability
returns retryable HTTP 503 with `Retry-After: 60` and an OTLP protobuf status;
it never claims durable ownership.

## Verification evidence

All counts below are captured results from the implementation and independent
review/fix cycles. The documentation-only closeout did not rerun expensive
client suites merely to reproduce the same evidence.

| Scope | Evidence | Result |
|---|---|---|
| Client final integration, focused Electron | Auth coordinator/bridge/gate, Trace outbox/forwarder/recovery/runtime, and continuity integration suites | **123/123 passed** |
| Client migration review fixes | Durable legacy predecessor recovery, receipt-only dedupe rebinding, cross-namespace FIFO, and background bounded migration | **22/22 Electron and 1/1 Python passed** |
| Client final migration gate | Exact legacy-bootstrap continuity proof, cross-account fail-closed behavior, and cloud-send barrier while local capture remains available | **43/43 Electron and 1/1 Python passed** |
| Client Python | Authentication policy, Relay projection, and continuity-focused tests | **152 passed, 6 skipped** |
| Client static/format gates | Desktop typecheck, lint, and format checks | **clean** |
| Client full Electron regression | One necessary full-suite run made earlier in the integration cycle | **1606 passed, 3 skipped** |
| Client local E2E | Playwright authentication-continuity scenarios | **5/5 passed** |
| Auth server | Django test suite | **222 tests passed** |
| Auth server schema/style | `makemigrations --check --dry-run` and Ruff | **no migration drift; Ruff clean** |
| Trace Gateway | Go test suite and `go vet` | **118 tests passed; vet clean** |
| Platform contracts | Python/shell Compose, route, deploy-safety, and secret-bootstrap contracts | **25/25 passed** |

The Playwright scenarios cover local cached restart, transient authentication
failure without logout, silent recovery, matching explicit revocation, and
Sign out data preservation. Gateway integration tests cover lost client
response, restart, durable duplicate receipt, FIFO recovery, and retryable
storage failure. These are local deterministic harnesses; they are not a claim
that the new branches are running in production.

During the final main-thread verification, the first parallel focused run had
one listener-close socket timing failure (42/43). The exact failing test passed
when isolated, and the complete three-file focused group then passed 43/43.
This timing observation is retained here rather than silently discarded.

## Requirement evidence summary

| Requirement | Evidence status |
|---|---|
| Authentication outages do not log out an established user or stop local conversation capability | Covered by focused Electron tests and Playwright |
| Restart restores cached authorization before network validation | Covered by bridge/coordinator and Playwright restart scenarios |
| Only Sign out or trusted matching structured revocation stops capability | Covered across server protocol, Electron coordinator, Gateway 403 propagation, and Playwright |
| Trace token failure never blocks the local Hermes backend | Covered by runtime-startup and integration tests |
| Unsent Trace data survives process/restart failure | Covered by encrypted outbox crash/reopen tests and integration tests |
| FIFO, single-flight token acquisition, retry/backoff, and 401 Trace-only refresh | Covered by forwarder, credential-provider, recovery-controller, and integration tests |
| Gateway acknowledges only durable accepted/duplicate ownership and survives response loss/restart | Covered by bbolt/server/continuity Go tests |
| Successful Trace payload is not retained in the client outbox | Covered by Gateway-first cancellation, receipt compaction, and payload-free tombstone tests |
| Sign out preserves SessionDB, attachments, conversations, and outbox | Covered by Electron/Playwright preservation assertions |

## Boundaries and remaining delivery actions

- No branch in this report has been pushed, submitted as a PR, merged, built as
  an installer, or deployed. Those are separate explicitly authorized actions.
- No live Windows runtime run was performed. Windows path and platform logic
  are covered by deterministic tests/type checking, not by a packaged Windows
  application acceptance run.
- The existing production Voice Trace deployment described by older reports
  predates these local continuity branches. This report must not be cited as
  evidence that production has the new protocol or client behavior.
- Playwright and Gateway tests use controlled local services. A future release
  can add packaged macOS/Windows and staged service acceptance without changing
  the local correctness result recorded here.

Operations and recovery procedures are in
[`../runbooks/trace-upload-continuity.md`](../runbooks/trace-upload-continuity.md).
The approved architecture and task plan are
[`../superpowers/specs/2026-08-24-auth-trace-continuity-design.md`](../superpowers/specs/2026-08-24-auth-trace-continuity-design.md),
[`../superpowers/plans/2026-08-24-authentication-continuity.md`](../superpowers/plans/2026-08-24-authentication-continuity.md),
and
[`../superpowers/plans/2026-08-24-trace-upload-continuity.md`](../superpowers/plans/2026-08-24-trace-upload-continuity.md).
