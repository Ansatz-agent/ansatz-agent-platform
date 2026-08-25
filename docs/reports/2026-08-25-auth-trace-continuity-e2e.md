# Authentication and Trace Continuity — Local Verification Report

Date: **2026-08-25**

## Result and delivery state

Authentication continuity and durable Trace upload continuity are implemented
and verified on three local development branches. This is a local automated
verification report: none of these branches has been pushed, used to open a
pull request, packaged for release, or deployed.

| Repository | Local branch | Verified ref |
|---|---|---|
| `agent-hermes-client` | `feature/auth-trace-continuity` | `4c0aff42d41979ea2dec08df33709410b832a621` |
| `agent-langfuse-server` | `feature/auth-continuity-protocol` | `36612920ccd9e2e4951e0fa07faf3f42f0e3d56e` |
| `ansatz-agent-platform` / Trace Gateway | `feature/trace-ingest-continuity` | `8e8c3d137a25ad18c91ff96780a042d4ae4e0567` |

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
- Legacy cookie-only caches restore local capability without minting a random
  installation. The first successful foreground status check uses the real
  desktop installation/client context to atomically upgrade the cache to a
  native Session; an outage leaves the legacy cache authorized and retryable.

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

Final stability review additionally bounds journal and segment compaction to
one deterministic scratch file per authoritative target, removes crash
orphans on reopen, rotates the loopback ingress bearer on every non-reused
forwarder startup, and keeps OTLP splitting linear in span count. The auth
service serializes SQLite write transactions with `BEGIN IMMEDIATE`, enables
WAL, throttles `last_seen_at`, supersedes duplicate active Sessions for one
installation, invalidates Sessions after password-state change, and rate-limits
Session issuance. The Gateway now supervises worker death, runs receipt and
quarantine GC, clamps `Retry-After`, and supports safe quarantine replay.

## Verification evidence

All counts below are captured results from the implementation and independent
review/fix cycles. The documentation-only closeout did not rerun expensive
client suites merely to reproduce the same evidence.

| Scope | Evidence | Result |
|---|---|---|
| Client full Electron regression | Entire Electron Vitest project after final fixes | **1639 passed, 3 skipped** |
| Client migration review fixes | Durable legacy predecessor recovery, receipt-only dedupe rebinding, cross-namespace FIFO, and background bounded migration | **22/22 Electron and 1/1 Python passed** |
| Client final migration gate | Exact legacy-bootstrap continuity proof, cross-account fail-closed behavior, and cloud-send barrier while local capture remains available | **43/43 Electron and 1/1 Python passed** |
| Client Python auth/runtime | Complete `tests/hermes_cli/client_auth/` suite after final fixes | **313 passed, 9 skipped** |
| Client static gates | Full desktop typecheck plus changed-file lint/diff checks | **clean; no new warnings** |
| Client local E2E | Playwright authentication-continuity scenarios | **5/5 passed** |
| Auth server | Django `history` test suite | **237 tests passed** |
| Auth server schema/style | `makemigrations --check --dry-run`, system check, and changed-file Ruff comparison | **no migration drift; no new Ruff findings (7 pre-existing findings remain)** |
| Trace Gateway | Go test suite, race detector, and `go vet` | **156 tests passed; race/vet clean** |
| Platform contracts | Python/shell Compose, route, deploy-safety, and secret-bootstrap contracts | **25/25 passed** |

The Playwright scenarios cover local cached restart, transient authentication
failure without logout, silent recovery, matching explicit revocation, and
Sign out data preservation. Gateway integration tests cover lost client
response, restart, durable duplicate receipt, FIFO recovery, and retryable
storage failure. These are local deterministic harnesses; they are not a claim
that the new branches are running in production.

The first Playwright attempt was invalid because it reused a stale `dist/`
build and never reached the fixed authentication server. After a fresh build,
the next run exposed accumulating `index.journal.compact-<uuid>` crash
orphans. That production storage leak was fixed with bounded deterministic
scratch paths and reopen cleanup; a fresh rebuild then passed all 5/5
scenarios. This failure-to-fix chain is retained as verification evidence
rather than being silently discarded.

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
