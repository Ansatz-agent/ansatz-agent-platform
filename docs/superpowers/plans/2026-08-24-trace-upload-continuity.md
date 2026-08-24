# Trace Upload Continuity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve every locally acknowledged Desktop Trace batch in an encrypted crash-safe outbox, resume FIFO upload without blocking local Hermes, and transfer durable ownership to an idempotent Gateway inbox before deleting the client payload.

**Architecture:** Python removes only redundant turn-mark history and inline binary payloads before export, while retaining each model span's complete provider request. Electron main accepts loopback OTLP into a per-account Brotli/AES-GCM segment store with a checksummed journal, then a single-flight credential provider and one pump per account send stable batches to a bbolt-backed Gateway inbox. The Gateway acknowledges after a synced transaction and delivers accepted canonical OTLP to Langfuse asynchronously with persistent receipts and FIFO retry state.

**Tech Stack:** Python 3.11+, pytest, Electron 40 / Node 22, TypeScript 6, Vitest 4, Node `crypto`/`zlib`/`fs`, Go 1.24, bbolt, OTLP protobuf, Docker Compose, Python unittest/httpx.

**Spec:** `docs/superpowers/specs/2026-08-24-auth-trace-continuity-design.md`

## Global Constraints

- Client implementation starts only from `agent-hermes-client/main@80bc34f5f18d2d58d1866b3140f8a1c6bc953928` on `feature/trace-outbox`.
- Gateway implementation starts only from `ansatz-agent-platform/origin/main@0b1b5b04c2c7ec132d666e7183bc042729d7d9c5` on `feature/trace-ingest-continuity`.
- Never inspect, copy, cherry-pick, diff against, or otherwise use `fix/relay-token-cost` or its staged work as implementation input.
- The Stream B client branch owns focused Trace modules and narrow seams. Final auth/lifecycle wiring is integrated with Stream A on `feature/auth-trace-continuity`.
- Do not modify `RelaySessionCoordinator.begin_turn()` or `end_turn()` and do not remove the complete request from any model span.
- Remove `conversation_history` only from `hermes.turn.start` and `hermes.turn.end` mark payloads; other plugins continue receiving the original hook kwargs.
- Client hard limits are exactly 2 GiB and 30 days per account, 64 MiB target segments, an 8 MiB logical batch limit before compression, a 50 ms maximum group-commit window, and a free-volume reserve of `max(1 GiB, 5%)`.
- Retry is full-jitter exponential with base 1 second, multiplier 2, cap 5 minutes; a longer valid `Retry-After` wins.
- Compression occurs before AES-256-GCM encryption. No plaintext Trace payload or raw data key is persisted.
- Local Relay success is sent only after both the segment and journal durability barriers complete.
- Client payload deletion occurs only after an `accepted` or `duplicate` durable Gateway receipt is journaled and synced.
- Gateway acceptance keys are trusted `account_id + batch_id`; Trace token ID is never part of idempotency identity.
- Gateway accepted-but-undelivered payloads are never evicted automatically. A storage safety failure rejects new acceptance with HTTP 507.
- Use the Conda environment `dl` for Python verification and never create a project-local virtual environment.
- Never execute a `.sh` file directly; invoke it as `bash path/to/script.sh`.
- No push, PR, deployment, destructive cleanup, production mutation, or installer release is authorized by this plan.

## File Structure

### Client files

- Modify `plugins/observability/nemo_relay/__init__.py`: project hook kwargs into compact turn-mark payloads.
- Modify `tests/plugins/test_nemo_relay_plugin.py`: prove marks omit redundant history while hook callers and model spans retain it.
- Modify `agent/ansatz_trace_policy.py`: replace inline binary/media content with stable descriptors.
- Modify `tests/agent/test_ansatz_trace_policy.py`: media privacy and non-mutation behavior.
- Create `apps/desktop/electron/otlp-split.ts`: split oversized OTLP requests at resource/scope/span boundaries.
- Create `apps/desktop/electron/otlp-split.test.ts`: protobuf split behavior.
- Create `apps/desktop/electron/trace-outbox-types.ts`: stable Stream B types and auth integration seam.
- Create `apps/desktop/electron/trace-outbox-crypto.ts`: Brotli, AES-GCM, wrapped key adapter, and record authentication.
- Create `apps/desktop/electron/trace-outbox-crypto.test.ts`: encryption and account isolation.
- Create `apps/desktop/electron/trace-outbox-record.ts`: bounded segment record codec.
- Create `apps/desktop/electron/trace-outbox-record.test.ts`: partial/corrupt record handling.
- Create `apps/desktop/electron/trace-outbox-journal.ts`: checksummed append-only metadata journal.
- Create `apps/desktop/electron/trace-outbox-journal.test.ts`: replay and torn-tail recovery.
- Create `apps/desktop/electron/trace-outbox-store.ts`: group commit, recovery, limits, dedupe, quarantine, receipts, and reclamation.
- Create `apps/desktop/electron/trace-outbox-store.test.ts`: real-disk durability and recovery tests.
- Create `apps/desktop/electron/trace-retry-policy.ts`: retry classification and full-jitter scheduling.
- Create `apps/desktop/electron/trace-retry-policy.test.ts`: deterministic backoff and `Retry-After` tests.
- Create `apps/desktop/electron/trace-credential-provider.ts`: normal and forced single-flight Trace token state.
- Create `apps/desktop/electron/trace-credential-provider.test.ts`: stale-flight 401 race tests.
- Modify `apps/desktop/electron/trace-forwarder.ts`: durable loopback admission, receipt protocol, and same-batch resend.
- Replace `apps/desktop/electron/trace-forwarder-queue.ts`: remove the process-memory queue after all consumers move to the outbox.
- Modify `apps/desktop/electron/trace-forwarder.test.ts`: durable boundary and upload behavior.
- Delete `apps/desktop/electron/trace-forwarder-queue.test.ts`: obsolete memory-queue contract after equivalent disk tests are green.
- Create `apps/desktop/electron/trace-recovery-controller.ts`: coalesced triggers and one pump per account.
- Create `apps/desktop/electron/trace-recovery-controller.test.ts`: trigger and pump exclusivity behavior.
- Modify `apps/desktop/electron/main.ts`: asynchronous startup and OS/renderer recovery triggers.
- Modify `apps/desktop/electron/preload.ts`: narrow renderer-online notification.
- Modify `apps/desktop/src/global.d.ts`: typed online notification bridge.
- Create `apps/desktop/electron/trace-continuity.integration.test.ts`: quit/reopen/account isolation integration.

### Gateway and platform files

- Modify `services/trace-gateway/internal/auth/introspector.go` and `_test.go`: additive parsing and structured error classes.
- Create `services/trace-gateway/internal/inbox/types.go`: durable envelope, receipt, retry, and error types.
- Create `services/trace-gateway/internal/inbox/store.go`: bbolt schema and synced acceptance.
- Create `services/trace-gateway/internal/inbox/store_test.go`: concurrency, restart, conflict, and storage guard tests.
- Create `services/trace-gateway/internal/delivery/worker.go` and `_test.go`: persistent FIFO Langfuse delivery.
- Modify `services/trace-gateway/internal/server/server.go` and `_test.go`: idempotency/digest/receipt wire contract.
- Modify `services/trace-gateway/internal/otlp/canonicalize.go` and `_test.go`: stable batch identity and UUID account ownership.
- Modify `services/trace-gateway/internal/redact/redact.go` and `_test.go`: defense-in-depth media descriptors.
- Modify `services/trace-gateway/cmd/gateway/main.go`: store/worker lifecycle and validated configuration.
- Modify `services/trace-gateway/go.mod` and `go.sum`: pin bbolt.
- Modify `deploy/voice-trace/docker-compose.yml` and `.env.example`: persistent Gateway volume and exact safety values.
- Modify `tests/test_voice_trace_compose_contract.py`: deployment contract.
- Modify `scripts/send_gateway_trace.py` and `tests/test_send_gateway_trace.py`: durable receipt protocol fixture.
- Create `services/trace-gateway/internal/server/continuity_integration_test.go`: lost-response/restart integration.
- Create `docs/runbooks/trace-upload-continuity.md`: outbox/inbox operations and recovery.
- Create `docs/reports/2026-08-24-trace-upload-continuity-e2e.md`: fresh verification evidence after execution.
- Modify `docs/02-progress.md` and `docs/03-file-index.md` only after all proof is available.

---

### Task 1: Remove Redundant Conversation History From Turn Marks

**Files:**
- Modify: `plugins/observability/nemo_relay/__init__.py`
- Test: `tests/plugins/test_nemo_relay_plugin.py`

**Interfaces:**
- Consumes: `on_pre_llm_call(**kwargs)` and `on_post_llm_call(**kwargs)` hook kwargs, including `conversation_history` supplied by `agent/turn_context.py` and `agent/turn_finalizer.py`.
- Produces: `_turn_mark_payload(kwargs: Mapping[str, Any]) -> dict[str, Any]`, which returns a shallow copy without `conversation_history`; `runtime.mark(name, payload)` retains every other field.
- Invariant: `_invoke_hook()` callers and other plugins still receive complete `conversation_history`; `relay_llm._relay_request_body()` remains unchanged.

- [ ] **Step 1: Write the failing behavior test**

```python
def test_turn_marks_drop_only_redundant_conversation_history(monkeypatch):
    from types import SimpleNamespace

    plugin = _fresh_plugin(monkeypatch, _FakeNemoRelay())
    observed = []
    runtime = SimpleNamespace(mark=lambda name, payload: observed.append((name, payload)))
    monkeypatch.setattr(plugin, "_get_runtime", lambda: runtime)
    history = [{"role": "user", "content": "old"}]
    common = {
        "session_id": "s1",
        "turn_id": "t1",
        "task_id": "task1",
        "conversation_history": history,
        "model": "gpt-test",
        "platform": "desktop",
    }

    plugin.on_pre_llm_call(user_message="new", **common)
    plugin.on_post_llm_call(user_message="new", assistant_response="answer", **common)

    assert [name for name, _ in observed] == ["hermes.turn.start", "hermes.turn.end"]
    assert all("conversation_history" not in payload for _, payload in observed)
    assert observed[0][1]["user_message"] == "new"
    assert observed[1][1]["assistant_response"] == "answer"
    assert common["conversation_history"] is history
```

- [ ] **Step 2: Run the test and verify RED**

Workdir: client worktree.

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/plugins/test_nemo_relay_plugin.py -k turn_marks_drop_only -v
```

Expected: FAIL because both recorded payloads still contain `conversation_history`.

- [ ] **Step 3: Implement the minimal projection**

```python
def _turn_mark_payload(kwargs: Mapping[str, Any]) -> dict[str, Any]:
    return {
        str(key): value
        for key, value in kwargs.items()
        if key != "conversation_history"
    }


def on_pre_llm_call(**kwargs: Any) -> None:
    runtime = _get_runtime()
    if runtime is not None:
        _safe(lambda: runtime.mark("hermes.turn.start", _turn_mark_payload(kwargs)))


def on_post_llm_call(**kwargs: Any) -> None:
    runtime = _get_runtime()
    if runtime is not None:
        _safe(lambda: runtime.mark("hermes.turn.end", _turn_mark_payload(kwargs)))
```

- [ ] **Step 4: Run focused and model-span regression tests GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/plugins/test_nemo_relay_plugin.py tests/agent/test_relay_llm.py -v
```

Expected: PASS; the new turn-mark test passes and Relay model request tests still prove complete request propagation.

- [ ] **Step 5: Commit the isolated behavior change**

```bash
rtk git add plugins/observability/nemo_relay/__init__.py tests/plugins/test_nemo_relay_plugin.py
rtk git commit -m "fix(trace): remove redundant history from turn marks"
```

### Task 2: Replace Inline Media With Stable Descriptors

**Files:**
- Modify: `agent/ansatz_trace_policy.py`
- Test: `tests/agent/test_ansatz_trace_policy.py`

**Interfaces:**
- Produces: `media_descriptor(payload: bytes, media_type: str) -> dict[str, Any]` with exact keys `redacted`, `media_type`, `byte_size`, `sha256`.
- Produces: `_data_url_descriptor(value: str) -> dict[str, Any] | None` for valid base64 data URLs.
- Preserves: text transcripts, textual multimodal prompts, model messages, tool arguments/results, and the caller's original object.

- [ ] **Step 1: Write failing descriptor tests**

```python
def test_inline_media_becomes_hash_type_and_size_without_mutation():
    import base64
    import hashlib
    from agent.ansatz_trace_policy import redact_trace_value

    raw = b"\x00\x01image-bytes"
    data_url = "data:image/png;base64," + base64.b64encode(raw).decode()
    source = {
        "messages": [{"role": "user", "content": [
            {"type": "text", "text": "inspect this"},
            {"type": "image_url", "image_url": {"url": data_url}},
        ]}],
        "audio_bytes": raw,
        "transcript": "spoken text",
    }

    clean = redact_trace_value(source)
    expected = {
        "redacted": "media",
        "media_type": "image/png",
        "byte_size": len(raw),
        "sha256": hashlib.sha256(raw).hexdigest(),
    }
    assert clean["messages"][0]["content"][1]["image_url"]["url"] == expected
    assert clean["audio_bytes"]["media_type"] == "application/octet-stream"
    assert clean["transcript"] == "spoken text"
    assert source["messages"][0]["content"][1]["image_url"]["url"] == data_url
    assert data_url not in repr(clean)
```

- [ ] **Step 2: Run the test and verify RED**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/agent/test_ansatz_trace_policy.py -k inline_media -v
```

Expected: FAIL because bytes and data URLs currently become `[REDACTED]` or remain inline instead of descriptors.

- [ ] **Step 3: Implement descriptor parsing before generic string redaction**

```python
def media_descriptor(payload: bytes, media_type: str) -> dict[str, Any]:
    return {
        "redacted": "media",
        "media_type": media_type,
        "byte_size": len(payload),
        "sha256": hashlib.sha256(payload).hexdigest(),
    }


def _data_url_descriptor(value: str) -> dict[str, Any] | None:
    match = re.fullmatch(r"data:([^;,]+);base64,([A-Za-z0-9+/=\r\n]+)", value)
    if not match:
        return None
    try:
        payload = base64.b64decode(match.group(2), validate=True)
    except (ValueError, binascii.Error):
        return {"redacted": "invalid_media"}
    return media_descriptor(payload, match.group(1).lower())
```

Call `_data_url_descriptor()` for strings before `_redact_string()`, and use `media_descriptor(bytes(value), "application/octet-stream")` for byte-like values. Decode recognized raw-audio base64 keys before descriptor creation; malformed raw media becomes `{"redacted":"invalid_media"}`.

- [ ] **Step 4: Run privacy and Relay serializer tests GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/agent/test_ansatz_trace_policy.py tests/agent/test_relay_llm.py tests/agent/test_relay_tools.py -v
```

Expected: PASS; no inline media survives, semantic model/tool text remains, original values are not mutated.

- [ ] **Step 5: Commit**

```bash
rtk git add agent/ansatz_trace_policy.py tests/agent/test_ansatz_trace_policy.py
rtk git commit -m "feat(trace): summarize inline media before export"
```

### Task 3: Split Oversized OTLP Requests at Semantic Boundaries

**Files:**
- Create: `apps/desktop/electron/otlp-split.ts`
- Create: `apps/desktop/electron/otlp-split.test.ts`
- Modify: `apps/desktop/electron/otlp-correlation.ts`

**Interfaces:**
- Produces: `splitOtlpExportTraceRequest(body: Buffer, maxBytes: number): { batches: Buffer[]; oversizedSpan: Buffer | null }`.
- Produces internal reusable protobuf helpers `readFields()`, `encodeLengthDelimited()`, and `encodeMessage()` from the existing correlation parser.
- Guarantees each returned batch is a valid `ExportTraceServiceRequest`, preserves resource and instrumentation-scope messages, and keeps span order.

- [ ] **Step 1: Write the failing split test**

```ts
test('splits by span while preserving resource and scope and quarantines one oversize span', () => {
  const body = exportRequest({ resource: 'service-a', scope: 'relay', spans: [span(1, 70), span(2, 70)] })
  const result = splitOtlpExportTraceRequest(body, 150)

  assert.equal(result.batches.length, 2)
  assert.deepEqual(result.batches.flatMap(readSpanIds), [1, 2])
  assert.ok(result.batches.every(batch => readResource(batch) === 'service-a'))
  assert.ok(result.batches.every(batch => readScope(batch) === 'relay'))

  const oversize = splitOtlpExportTraceRequest(
    exportRequest({ resource: 'service-a', scope: 'relay', spans: [span(3, 512)] }),
    150
  )
  assert.equal(oversize.batches.length, 0)
  assert.equal(readSpanIds(oversize.oversizedSpan!), 3)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/otlp-split.test.ts
```

Expected: FAIL because `otlp-split.ts` and `splitOtlpExportTraceRequest` do not exist.

- [ ] **Step 3: Implement the minimal ordered packer**

```ts
export function splitOtlpExportTraceRequest(body: Buffer, maxBytes: number): SplitOtlpResult {
  const decoded = decodeExportRequest(body)
  if (!decoded || maxBytes <= 0) return { batches: [], oversizedSpan: body }
  const batches: Buffer[] = []
  for (const resource of decoded.resources) {
    let current: Buffer[] = []
    for (const scopedSpan of resource.scopedSpans) {
      const candidate = encodeExport(resource, [...current, scopedSpan])
      if (candidate.length <= maxBytes) { current.push(scopedSpan); continue }
      if (current.length) batches.push(encodeExport(resource, current))
      current = []
      const single = encodeExport(resource, [scopedSpan])
      if (single.length > maxBytes) return { batches, oversizedSpan: single }
      current.push(scopedSpan)
    }
    if (current.length) batches.push(encodeExport(resource, current))
  }
  return { batches, oversizedSpan: null }
}
```

The concrete decoder must retain unknown protobuf fields verbatim and only split repeated span field `ScopeSpans.spans=2`; it must not JSON-round-trip OTLP.

- [ ] **Step 4: Run split and correlation tests GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/otlp-split.test.ts electron/trace-forwarder.test.ts
```

Expected: PASS; all split pieces remain correlation-readable and malformed protobuf is rejected deterministically.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/otlp-split.ts apps/desktop/electron/otlp-split.test.ts apps/desktop/electron/otlp-correlation.ts
rtk git commit -m "feat(trace): split oversized OTLP batches"
```

### Task 4: Define Durable Outbox Types and Auth Integration Seam

**Files:**
- Create: `apps/desktop/electron/trace-outbox-types.ts`
- Create: `apps/desktop/electron/trace-outbox-types.test.ts`

**Interfaces:**
- Produces the exact types `TraceOwner`, `TraceEnvelopeInput`, `DurableTraceBatch`, `DurableReceipt`, `TraceOutboxDiagnostics`, and `OutboxSendResult` used by all later client tasks.
- `TraceOwner.accountKey` is the directory namespace; trusted upload requires non-null UUID `accountId` and `sessionId`.

- [ ] **Step 1: Write failing validation tests**

```ts
test('trusted Trace owners require matching UUID identities while legacy owners remain local-only', () => {
  assert.deepEqual(validateTraceOwner({
    accountKey: 'account-11111111-1111-4111-8111-111111111111',
    accountId: '11111111-1111-4111-8111-111111111111',
    sessionId: '22222222-2222-4222-8222-222222222222',
    installationId: '33333333-3333-4333-8333-333333333333'
  }).uploadable, true)
  assert.equal(validateTraceOwner({
    accountKey: 'legacy-' + 'a'.repeat(64),
    accountId: null,
    sessionId: null,
    installationId: '33333333-3333-4333-8333-333333333333'
  }).uploadable, false)
  assert.throws(() => validateTraceOwner({ ...validOwner(), accountKey: '../escape' }), /invalid_account_key/)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-types.test.ts
```

Expected: FAIL because the module does not exist.

- [ ] **Step 3: Implement exact validation**

```ts
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const ACCOUNT_KEY = /^(?:account-[0-9a-f-]{36}|legacy-[0-9a-f]{64})$/

export function validateTraceOwner(owner: TraceOwner): TraceOwnerState {
  if (!ACCOUNT_KEY.test(owner.accountKey) || !UUID_V4.test(owner.installationId)) {
    throw new TypeError('invalid_account_key')
  }
  const uploadable = owner.accountId !== null && owner.sessionId !== null
  if (uploadable && (!UUID_V4.test(owner.accountId!) || !UUID_V4.test(owner.sessionId!))) {
    throw new TypeError('invalid_trace_owner')
  }
  return { owner: { ...owner }, uploadable }
}
```

- [ ] **Step 4: Run GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-types.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-outbox-types.ts apps/desktop/electron/trace-outbox-types.test.ts
rtk git commit -m "feat(trace): define durable outbox contracts"
```

### Task 5: Implement Brotli and AES-GCM Record Protection

**Files:**
- Create: `apps/desktop/electron/trace-outbox-crypto.ts`
- Create: `apps/desktop/electron/trace-outbox-crypto.test.ts`
- Create: `apps/desktop/electron/trace-outbox-record.ts`
- Create: `apps/desktop/electron/trace-outbox-record.test.ts`

**Interfaces:**
- Produces: `TraceKeyProtector { available(); wrap(); unwrap() }` for Electron `safeStorage` injection.
- Produces: `encryptTraceRecord(plaintext, key, aad): Promise<EncryptedTraceRecord>` and `decryptTraceRecord(record, key, aad): Promise<Buffer>`.
- Produces: `encodeSegmentRecord()` / `decodeSegmentRecord()` with magic `ATOB`, version 1, bounded lengths, and SHA-256 record checksum.

- [ ] **Step 1: Write failing crypto/codec tests**

```ts
test('compresses before encryption and rejects another account AAD or a torn record', async () => {
  const body = Buffer.from('repeated history '.repeat(10_000))
  const key = Buffer.alloc(32, 7)
  const encrypted = await encryptTraceRecord(body, key, Buffer.from('account-a/batch-1'))
  assert.ok(encrypted.ciphertext.length < body.length)
  assert.deepEqual(await decryptTraceRecord(encrypted, key, Buffer.from('account-a/batch-1')), body)
  await assert.rejects(decryptTraceRecord(encrypted, key, Buffer.from('account-b/batch-1')))

  const encoded = encodeSegmentRecord({ header: validHeader(), encrypted })
  assert.equal(decodeSegmentRecord(encoded, 0)?.nextOffset, encoded.length)
  assert.equal(decodeSegmentRecord(encoded.subarray(0, encoded.length - 1), 0), null)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-crypto.test.ts electron/trace-outbox-record.test.ts
```

Expected: FAIL because both modules are absent.

- [ ] **Step 3: Implement the codec**

```ts
export async function encryptTraceRecord(plaintext: Buffer, key: Buffer, aad: Buffer) {
  const compressed = await brotliCompressAsync(plaintext)
  const nonce = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', key, nonce)
  cipher.setAAD(aad)
  const ciphertext = Buffer.concat([cipher.update(compressed), cipher.final()])
  return { nonce, ciphertext, tag: cipher.getAuthTag(), originalBytes: plaintext.length }
}

export async function decryptTraceRecord(record: EncryptedTraceRecord, key: Buffer, aad: Buffer) {
  const decipher = createDecipheriv('aes-256-gcm', key, record.nonce)
  decipher.setAAD(aad)
  decipher.setAuthTag(record.tag)
  return brotliDecompressAsync(Buffer.concat([decipher.update(record.ciphertext), decipher.final()]))
}
```

`encodeSegmentRecord()` must include fixed-width unsigned lengths and checksum all preceding record bytes. Reject nonce lengths other than 12, tag lengths other than 16, header lengths above 64 KiB, ciphertext above 64 MiB, unknown version, invalid checksum, and incomplete tails.

- [ ] **Step 4: Run GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-crypto.test.ts electron/trace-outbox-record.test.ts
```

Expected: PASS, including tamper, corrupt checksum, wrong key, and compression-order assertions.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-outbox-crypto.ts apps/desktop/electron/trace-outbox-crypto.test.ts apps/desktop/electron/trace-outbox-record.ts apps/desktop/electron/trace-outbox-record.test.ts
rtk git commit -m "feat(trace): encrypt compressed outbox records"
```

### Task 6: Add the Checksummed Journal and Fsync-Before-Ack Group Commit

**Files:**
- Create: `apps/desktop/electron/trace-outbox-journal.ts`
- Create: `apps/desktop/electron/trace-outbox-journal.test.ts`
- Create: `apps/desktop/electron/trace-outbox-store.ts`
- Create: `apps/desktop/electron/trace-outbox-store.test.ts`

**Interfaces:**
- Produces: `TraceJournal.append(operations)` and `recover()`.
- Produces: `TraceOutboxStore.open(options)` and `enqueue(input)`.
- Durability order is observable through injected `TraceFileSystem`; enqueue resolves only after segment sync and journal sync.

- [ ] **Step 1: Write the failing durability-order test**

```ts
test('resolves every grouped enqueue only after segment sync then journal sync', async () => {
  const events: string[] = []
  const gate = deferred<void>()
  const fs = fakeTraceFileSystem({
    onSegmentWrite: () => events.push('segment.write'),
    onSegmentSync: async () => { events.push('segment.sync'); await gate.promise },
    onJournalWrite: () => events.push('journal.write'),
    onJournalSync: () => events.push('journal.sync')
  })
  const store = await TraceOutboxStore.open(options({ fs, groupCommitMs: 50 }))
  let acknowledged = false
  const first = store.enqueue(envelope('one')).then(() => { acknowledged = true })
  const second = store.enqueue(envelope('two'))
  await fs.waitFor('segment.sync')
  assert.equal(acknowledged, false)
  gate.resolve()
  await Promise.all([first, second])
  assert.deepEqual(events, ['segment.write', 'segment.write', 'segment.sync', 'journal.write', 'journal.sync'])
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-journal.test.ts electron/trace-outbox-store.test.ts
```

Expected: FAIL because journal/store modules do not exist.

- [ ] **Step 3: Implement the group commit queue**

```ts
async flushGroup(): Promise<void> {
  const group = this.pending.splice(0)
  try {
    const records = await Promise.all(group.map(item => this.prepareRecord(item.input)))
    await this.segment.append(records.map(item => item.encoded))
    await this.segment.sync()
    await this.journal.append(records.map(item => ({ op: 'pending', ...item.index })))
    await this.journal.sync()
    records.forEach((record, index) => group[index].resolve(record.batch))
  } catch (error) {
    group.forEach(item => item.reject(error))
  }
}
```

Schedule the first queued write for at most 50 ms; reaching 8 MiB of uncommitted input flushes immediately. Journal lines use canonical JSON and a SHA-256 checksum. `key.json` is written to a same-directory temporary regular file, synced, atomically renamed, then parent directory synced before the first record is accepted.

- [ ] **Step 4: Run GREEN against real and fake filesystems**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-journal.test.ts electron/trace-outbox-store.test.ts
```

Expected: PASS; no promise resolves before both sync barriers, and a failed segment or journal sync rejects every member of that group.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-outbox-journal.ts apps/desktop/electron/trace-outbox-journal.test.ts apps/desktop/electron/trace-outbox-store.ts apps/desktop/electron/trace-outbox-store.test.ts
rtk git commit -m "feat(trace): persist outbox before local acknowledgement"
```

### Task 7: Recover Torn Writes and Preserve FIFO Across Restart

**Files:**
- Modify: `apps/desktop/electron/trace-outbox-journal.ts`
- Modify: `apps/desktop/electron/trace-outbox-store.ts`
- Modify: `apps/desktop/electron/trace-outbox-store.test.ts`

**Interfaces:**
- Produces: `TraceOutboxStore.peekEligible(now)` in monotonic sequence order.
- Recovery scans valid segment records, truncates only an invalid final tail, replays valid checksummed journal entries, and reconstructs missing pending entries from segment headers.

- [ ] **Step 1: Write failing real-disk crash tests**

```ts
test.each(['segment-tail', 'journal-tail', 'after-segment-sync', 'during-send', 'after-receipt'])
('recovers deterministically from %s', async crashPoint => {
  const root = await temporaryOutboxDirectory()
  await buildCrashFixture(root, crashPoint, [envelope('one'), envelope('two')])
  const recovered = await TraceOutboxStore.open(options({ root }))
  const ids = await drainBatchIds(recovered)
  assert.deepEqual(ids, expectedPendingIds(crashPoint))
  assert.equal(new Set(ids).size, ids.length)
  assert.equal(recovered.diagnostics().recoveredCorruptTail, crashPoint.endsWith('tail') ? 1 : 0)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-store.test.ts
```

Expected: FAIL because `open()` does not yet rebuild missing journal state or truncate torn tails.

- [ ] **Step 3: Implement deterministic recovery**

```ts
private async recover(): Promise<void> {
  const scanned = await scanSegments(this.segmentDirectory, this.dataKey)
  const journal = await this.journal.recover()
  this.index = reconcile(scanned.records, journal.operations)
  for (const missing of scanned.records.filter(record => !this.index.has(record.batchId))) {
    this.index.set(missing.batchId, pendingIndexFromRecord(missing))
    await this.journal.append([{ op: 'pending', ...pendingIndexFromRecord(missing), recovered: true }])
  }
  await this.journal.sync()
}
```

If a non-tail record is corrupt, leave its segment untouched, quarantine the segment index entry, and record a diagnostic; never scan past untrusted lengths. Receipt journal state wins over an older pending record after restart.

- [ ] **Step 4: Run restart tests GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-store.test.ts
```

Expected: PASS for normal close/reopen and every injected crash point with original FIFO order.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-outbox-journal.ts apps/desktop/electron/trace-outbox-store.ts apps/desktop/electron/trace-outbox-store.test.ts
rtk git commit -m "feat(trace): recover durable outbox after crashes"
```

### Task 8: Enforce Limits, Quarantine, Local Dedupe, and Segment Reclamation

**Files:**
- Modify: `apps/desktop/electron/trace-outbox-types.ts`
- Modify: `apps/desktop/electron/trace-outbox-store.ts`
- Modify: `apps/desktop/electron/trace-outbox-store.test.ts`

**Interfaces:**
- Produces: `quarantine(batchId, errorClass)`, `acknowledge(batchId, receipt)`, `compactIfIdle()`, and `diagnostics()`.
- Dedupe key is SHA-256 of `accountKey + entrypoint + hermesSessionId + runId + payloadSha256`.
- Receipt tombstones retain 30 days and are independently capped at 64 MiB or 100,000 entries, whichever is reached first.

- [ ] **Step 1: Write failing deterministic policy tests**

```ts
test('evicts oldest telemetry at hard limits, dedupes locally, and never touches SessionDB', async () => {
  const root = await temporaryOutboxDirectory()
  const sessionDb = await sentinelFile(root, '../hermes/state.db', 'conversation-truth')
  const store = await TraceOutboxStore.open(options({
    root,
    maxBytes: 12,
    retentionMs: 30 * DAY,
    freeSpace: () => ({ available: 20, total: 100 })
  }))
  const one = await store.enqueue(envelope('one', 8))
  await store.enqueue(envelope('two', 8))
  assert.equal(store.diagnostics().evictedCapacity, 1)
  const duplicate = await store.enqueue(envelope('two', 8))
  assert.equal(duplicate.batchId, (await store.peekEligible(Date.now()))!.batchId)
  await store.quarantine(duplicate.batchId, 'payload_too_large')
  assert.equal(store.diagnostics().quarantined, 1)
  assert.equal(await readFile(sessionDb, 'utf8'), 'conversation-truth')
  assert.notEqual(one.batchId, duplicate.batchId)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-store.test.ts
```

Expected: FAIL because eviction, quarantine, tombstones, and dedupe are not implemented.

- [ ] **Step 3: Implement exact controlled degradation**

```ts
private async ensureCapacity(incomingBytes: number, now: number): Promise<void> {
  await this.expireBefore(now - THIRTY_DAYS_MS)
  const disk = await this.freeSpace()
  const reserve = Math.max(ONE_GIB, Math.ceil(disk.total * 0.05))
  while (this.payloadBytes + incomingBytes > this.maxBytes || disk.available - incomingBytes < reserve) {
    const oldest = this.oldestUnsentOrQuarantined()
    if (!oldest) throw new TraceOutboxError('storage_unavailable')
    await this.recordTerminal(oldest.batchId, 'evicted')
    this.diagnosticsState.evictedCapacity += 1
  }
}
```

Quarantine is a journal state over encrypted bytes and counts toward 2 GiB. Accepted records become reclaimable only after receipt journal sync. Delete a segment only when every record is terminal/reclaimable; otherwise compact to a new synced segment only while `isConversationStreaming()` is false.

- [ ] **Step 4: Run policy tests GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-outbox-store.test.ts
```

Expected: PASS for capacity, retention, free reserve, key loss, dedupe, tombstone GC, quarantine, and full-segment reclamation.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-outbox-types.ts apps/desktop/electron/trace-outbox-store.ts apps/desktop/electron/trace-outbox-store.test.ts
rtk git commit -m "feat(trace): bound and reclaim durable outbox storage"
```

### Task 9: Implement Retry Policy and Race-Free Trace Credential Single-Flight

**Files:**
- Create: `apps/desktop/electron/trace-retry-policy.ts`
- Create: `apps/desktop/electron/trace-retry-policy.test.ts`
- Create: `apps/desktop/electron/trace-credential-provider.ts`
- Create: `apps/desktop/electron/trace-credential-provider.test.ts`
- Modify: `apps/desktop/electron/trace-forwarder.ts`

**Interfaces:**
- Produces: `nextTraceRetry({attempt, now, retryAfterMs, random})`.
- Produces: `RefreshingTraceCredentialProvider.current({forceRefresh})`, `invalidate()`, `clear()`, and `expiresAt()`.
- Forced refresh waits for a normal flight, invalidates its result, and creates exactly one new forced flight.

- [ ] **Step 1: Write failing backoff and stale-flight race tests**

```ts
test('a 401 cannot coalesce forced refresh into a stale normal flight', async () => {
  const normal = deferred<TraceCredential>()
  const calls: boolean[] = []
  const provider = new RefreshingTraceCredentialProvider({ load: force => {
    calls.push(force)
    return force ? Promise.resolve(credential('fresh')) : normal.promise
  }})
  const first = provider.current()
  const forced = provider.current({ forceRefresh: true })
  normal.resolve(credential('stale'))
  assert.equal((await first).access_token, 'stale')
  assert.equal((await forced).access_token, 'fresh')
  assert.deepEqual(calls, [false, true])
})

test('full jitter caps at five minutes and honors a longer Retry-After', () => {
  assert.equal(nextTraceRetry({ attempt: 0, now: 10, random: () => 0.5 }), 510)
  assert.equal(nextTraceRetry({ attempt: 20, now: 10, random: () => 1 }), 300_010)
  assert.equal(nextTraceRetry({ attempt: 1, now: 10, random: () => 0, retryAfterMs: 600_000 }), 600_010)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-retry-policy.test.ts electron/trace-credential-provider.test.ts
```

Expected: FAIL because the modules do not exist and the old provider coalesces forced refresh into `pending`.

- [ ] **Step 3: Implement separate normal and forced flights**

```ts
async current({ forceRefresh = false } = {}): Promise<TraceCredential> {
  if (!forceRefresh) return this.currentNormal()
  if (this.forcedFlight) return this.forcedFlight
  this.forcedFlight = (async () => {
    if (this.normalFlight) await this.normalFlight.catch(() => undefined)
    this.credential = null
    return this.loadValidated(true)
  })().finally(() => { this.forcedFlight = null })
  return this.forcedFlight
}
```

Parse `Retry-After` as either non-negative seconds or RFC 7231 date. Invalid or shorter values do not replace jitter.

- [ ] **Step 4: Run GREEN including old forwarder token tests**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-retry-policy.test.ts electron/trace-credential-provider.test.ts electron/trace-forwarder.test.ts
```

Expected: PASS; one normal flight, one post-401 forced flight, and identical current batch bytes on resend.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-retry-policy.ts apps/desktop/electron/trace-retry-policy.test.ts apps/desktop/electron/trace-credential-provider.ts apps/desktop/electron/trace-credential-provider.test.ts apps/desktop/electron/trace-forwarder.ts
rtk git commit -m "fix(trace): serialize token refresh and retry scheduling"
```

### Task 10: Make the Loopback Forwarder Acknowledge Only Durable Batches

**Files:**
- Modify: `apps/desktop/electron/trace-forwarder.ts`
- Modify: `apps/desktop/electron/trace-forwarder.test.ts`
- Delete: `apps/desktop/electron/trace-forwarder-queue.ts`
- Delete: `apps/desktop/electron/trace-forwarder-queue.test.ts`

**Interfaces:**
- `TraceForwarder` consumes `TraceOutboxStore`, `TraceOwner`, and `RefreshingTraceCredentialProvider`.
- `start(owner)` opens loopback admission without acquiring a Trace token.
- Upstream headers add `Idempotency-Key` and `X-Trace-Payload-SHA256`.
- HTTP 200/2xx is accepted only with matching `X-Trace-Batch-ID` and `X-Trace-Receipt: accepted|duplicate`.

- [ ] **Step 1: Write failing durable-boundary and receipt tests**

```ts
test('local 200 waits for outbox durability and Gateway receipt controls deletion', async () => {
  const durable = deferred<DurableTraceBatch>()
  const store = fakeOutbox({ enqueue: () => durable.promise })
  const forwarder = new TraceForwarder({ store, credentialProvider, installationId })
  const started = await forwarder.start(validOwner())
  const posted = post(started.endpoint, started.localBearer)
  assert.equal(await settlesWithin(posted, 25), false)
  durable.resolve(batch('batch-1'))
  assert.equal((await posted).status, 200)
  await waitFor(() => upstreamCalls.length === 1)
  assert.equal(upstreamCalls[0].headers.get('idempotency-key'), 'batch-1')
  assert.equal(store.acknowledge.mock.calls.length, 0)
  gatewayResponse.resolve(receiptResponse('batch-1', 'accepted'))
  await waitFor(() => store.acknowledge.mock.calls.length === 1)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-forwarder.test.ts
```

Expected: FAIL because current `handle()` returns 200 immediately after an in-memory enqueue and accepts any 2xx as deletion authority.

- [ ] **Step 3: Replace the memory queue path**

```ts
const durable = await this.store.enqueue({ ...metadata, body })
respond(response, 200, true)
this.recovery.trigger('durable-enqueue')

const receipt = parseGatewayReceipt(response, batch.batchId)
if (receipt) {
  await this.store.acknowledge(batch.batchId, receipt)
  return { kind: receipt.outcome }
}
```

Loopback body read is capped at 64 MiB; `splitOtlpExportTraceRequest(..., 8 * 1024 * 1024)` runs before enqueue. A single oversize span is encrypted into quarantine and Relay receives a non-success diagnostic response. Disk/key/reserve failures return HTTP 507 without claiming persistence. Gateway 400, 409, 413, and 415 quarantine; 401 refreshes only Trace credential and resends the same batch; 403 is forwarded as explicit revocation evidence; 429/5xx/network retain the head.

- [ ] **Step 4: Run forwarder/outbox tests GREEN, then remove old queue files**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-forwarder.test.ts electron/trace-outbox-store.test.ts
rtk npm --prefix apps/desktop run typecheck
```

Expected: PASS; no import references `TraceForwarderQueue`, local success follows durable commit, and failures retain or quarantine exactly once.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-forwarder.ts apps/desktop/electron/trace-forwarder.test.ts apps/desktop/electron/trace-forwarder-queue.ts apps/desktop/electron/trace-forwarder-queue.test.ts
rtk git commit -m "feat(trace): forward from a durable encrypted outbox"
```

### Task 11: Add Recovery Triggers and Asynchronous Main-Process Wiring

**Files:**
- Create: `apps/desktop/electron/trace-recovery-controller.ts`
- Create: `apps/desktop/electron/trace-recovery-controller.test.ts`
- Modify: `apps/desktop/electron/main.ts`
- Modify: `apps/desktop/electron/preload.ts`
- Modify: `apps/desktop/src/global.d.ts`
- Modify: `apps/desktop/electron/trace-forwarder.test.ts`

**Interfaces:**
- Produces `TraceRecoveryController.trigger(reason)` for `enqueue`, `startup`, `timer`, `renderer-online`, `resume`, `focus`, `token-ready`, `token-near-expiry`, and `upload-401`.
- One pump per `TraceOwner.accountKey`; repeated triggers coalesce through a single pending promise.
- Preload exposes only `window.hermes.traceOnline(): void`; renderer cannot pass account IDs, tokens, paths, or payloads.

- [ ] **Step 1: Write failing trigger and non-blocking startup tests**

```ts
test('coalesces every recovery signal into one account pump', async () => {
  const pump = vi.fn(() => deferredPump.promise)
  const controller = new TraceRecoveryController({ pump, accountKey: 'account-' + UUID })
  for (const reason of ALL_TRACE_RECOVERY_TRIGGERS) controller.trigger(reason)
  assert.equal(pump.mock.calls.length, 1)
  deferredPump.resolve()
  await controller.idleForTest()
})

test('local backend preparation does not wait for Trace token acquisition', async () => {
  const token = deferred<TraceCredential>()
  const harness = createAuthenticatedRuntimeHarness({ traceToken: () => token.promise })
  const backend = await harness.prepareLocalBackend()
  assert.equal(backend.kind, 'local')
  assert.equal(harness.forwarderStarted, true)
  assert.equal(harness.tokenResolved, false)
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-recovery-controller.test.ts electron/trace-forwarder.test.ts
```

Expected: FAIL because no trigger controller exists and `ensureDesktopTraceForwarder()` awaits `provider.current()`.

- [ ] **Step 3: Wire all triggers without widening renderer authority**

```ts
const forwarder = await TraceForwarder.create({
  owner: traceOwnerFromScope(scope),
  root: path.join(app.getPath('userData'), 'trace-outbox'),
  keyProtector: electronSafeStorageProtector(safeStorage),
  credentialProvider: provider
})
const started = await forwarder.start(owner)
desktopTraceForwarder = forwarder
forwarder.trigger('startup')
void provider.current().then(() => forwarder.trigger('token-ready')).catch(() => undefined)
```

Register `powerMonitor.on('resume', ...)`, `mainWindow.on('focus', ...)`, periodic due timer, token-near-expiry timer, and guarded IPC `hermes:trace:online`. Stop only the pump/admission on sign-out or owner switch; never clear the account directory. Replace the source-reading test in `trace-forwarder.test.ts` with the injected runtime harness behavior above.

- [ ] **Step 4: Run Electron tests, typecheck, and lint GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-recovery-controller.test.ts electron/trace-forwarder.test.ts electron/backend-env.test.ts
rtk npm --prefix apps/desktop run typecheck
rtk npm --prefix apps/desktop run lint
```

Expected: PASS; startup is token-independent and each required trigger schedules recovery without concurrent pumps.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-recovery-controller.ts apps/desktop/electron/trace-recovery-controller.test.ts apps/desktop/electron/main.ts apps/desktop/electron/preload.ts apps/desktop/src/global.d.ts apps/desktop/electron/trace-forwarder.test.ts
rtk git commit -m "feat(trace): resume outbox upload from lifecycle signals"
```

### Task 12: Prove Client Quit, Restart, and Account Isolation End to End

**Files:**
- Create: `apps/desktop/electron/trace-continuity.integration.test.ts`
- Modify: `apps/desktop/electron/trace-outbox-store.ts`
- Modify: `apps/desktop/electron/trace-forwarder.ts`

**Interfaces:**
- Uses real filesystem, real HTTP loopback listener, real encryption codec, and mock public Gateway responses.
- Proves another account cannot enumerate, decrypt, or upload an existing account namespace.

- [ ] **Step 1: Write the failing integration scenario**

```ts
test('offline accepted Trace survives quit and uploads FIFO after same-account restart', async () => {
  const userData = await temporaryOutboxDirectory()
  const gateway = await controllableGateway({ online: false })
  const first = await launchTraceHarness({ userData, owner: ownerA, gateway })
  assert.equal((await first.post(payload(1))).status, 200)
  assert.equal((await first.post(payload(2))).status, 200)
  await first.quit()

  const wrong = await launchTraceHarness({ userData, owner: ownerB, gateway: await controllableGateway({ online: true }) })
  await wrong.trigger('startup')
  assert.deepEqual(wrong.gatewayBodies, [])
  await wrong.quit()

  gateway.setOnline(true)
  const resumed = await launchTraceHarness({ userData, owner: ownerA, gateway })
  await resumed.trigger('startup')
  await waitFor(() => resumed.outboxDiagnostics().pending === 0)
  assert.deepEqual(gateway.payloadOrder, [sha(payload(1)), sha(payload(2))])
  assert.deepEqual(gateway.receipts, ['accepted', 'accepted'])
})
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-continuity.integration.test.ts
```

Expected: FAIL until restart ownership, close semantics, and account-key directory routing are fully connected.

- [ ] **Step 3: Apply only the minimal integration fixes revealed by the test**

```ts
async stop({ closeAdmission = true } = {}): Promise<TraceForwarderSummary> {
  if (closeAdmission) await this.closeLoopbackServer()
  this.recovery.stop()
  await this.store.close()
  return this.store.diagnostics()
}
```

Opening a namespace must require its wrapped account key; key unwrap failure reports `encryption_unavailable` and never falls through to another namespace or plaintext.

- [ ] **Step 4: Run the complete client Stream B proof GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/agent/test_ansatz_trace_policy.py tests/plugins/test_nemo_relay_plugin.py -v
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/otlp-split.test.ts electron/trace-outbox-crypto.test.ts electron/trace-outbox-record.test.ts electron/trace-outbox-journal.test.ts electron/trace-outbox-store.test.ts electron/trace-retry-policy.test.ts electron/trace-credential-provider.test.ts electron/trace-forwarder.test.ts electron/trace-recovery-controller.test.ts electron/trace-continuity.integration.test.ts
rtk npm --prefix apps/desktop run typecheck
rtk npm --prefix apps/desktop run lint
```

Expected: all commands exit 0 with no flaky retry report.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/trace-continuity.integration.test.ts apps/desktop/electron/trace-outbox-store.ts apps/desktop/electron/trace-forwarder.ts
rtk git commit -m "test(trace): prove offline restart continuity"
```

### Task 13: Make Gateway Introspection Additive and Structured

**Files:**
- Modify: `services/trace-gateway/internal/auth/introspector.go`
- Modify: `services/trace-gateway/internal/auth/introspector_test.go`

**Interfaces:**
- Extends `auth.Principal` with `AccountID`, `SessionID`, and `InstallationID` UUIDs.
- Produces typed errors `ErrRefreshRequired`, `ErrExplicitRevocation` carrying a safe code, and `ErrUnavailable`.
- Unknown additive JSON fields are ignored; malformed required fields remain unavailable.

- [ ] **Step 1: Write failing table tests**

```go
func TestIntrospectorClassifiesAdditiveInactiveReasons(t *testing.T) {
    tests := []struct{ body string; want error; code string }{
        {`{"active":false,"reason":"token_expired","extra":"ok"}`, ErrRefreshRequired, "trace_token_refresh_required"},
        {`{"active":false,"reason":"session_revoked","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222"}`, ErrExplicitRevocation, "session_revoked"},
        {`{"active":false,"reason":"account_disabled","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222"}`, ErrExplicitRevocation, "account_disabled"},
        {`{"active":false,"reason":"unknown_new_reason"}`, ErrUnavailable, ""},
    }
    for _, test := range tests { assertIntrospectionError(t, test.body, test.want, test.code) }
}
```

- [ ] **Step 2: Run and verify RED**

Workdir: `services/trace-gateway` in the platform worktree.

```bash
rtk go test ./internal/auth -run 'TestIntrospectorClassifies|TestIntrospectorReturnsExact' -v
```

Expected: FAIL because unknown fields are rejected and inactive reasons collapse into `ErrInactive`.

- [ ] **Step 3: Implement tolerant parsing and exact reason mapping**

```go
type InactiveError struct { Code string; Explicit bool }
func (e *InactiveError) Error() string { return "trace credential inactive" }
func (e *InactiveError) Is(target error) bool {
    if e.Explicit { return target == ErrExplicitRevocation }
    return target == ErrRefreshRequired
}
```

Remove `decoder.DisallowUnknownFields()`. Active responses require UUID account/session/installation, scope, audience, and future expiry. Map expired/rotated/revoked/invalid token to refresh-required; map only session/account explicit reasons to explicit revocation; map timeout, non-200, malformed, and unknown reason to unavailable. Never include bearer/internal secret in errors.

- [ ] **Step 4: Run auth package GREEN**

```bash
rtk go test ./internal/auth -v
```

Expected: PASS for additive active fields, exact inactive classifications, timeout, malformed body, and secret non-disclosure.

- [ ] **Step 5: Commit**

```bash
rtk git add services/trace-gateway/internal/auth/introspector.go services/trace-gateway/internal/auth/introspector_test.go
rtk git commit -m "feat(gateway): classify Trace introspection failures"
```

### Task 14: Add the Synced bbolt Inbox and Durable Receipt Ledger

**Files:**
- Create: `services/trace-gateway/internal/inbox/types.go`
- Create: `services/trace-gateway/internal/inbox/store.go`
- Create: `services/trace-gateway/internal/inbox/store_test.go`
- Modify: `services/trace-gateway/go.mod`
- Modify: `services/trace-gateway/go.sum`

**Interfaces:**
- Produces `Open(path string, options Options) (*BoltStore, error)`.
- Produces `Accept(ctx, Envelope) (AcceptResult, error)`, `PeekEligible`, `MarkRetry`, `MarkDelivered`, `MarkQuarantined`, `CollectReceipts`, `Sync`, and `Close`.
- bbolt keys are `account_id + NUL + batch_id`; FIFO keys are unsigned big-endian sequence numbers.

- [ ] **Step 1: Write failing acceptance/concurrency/restart tests**

```go
func TestBoltStoreAcceptIsDurableIdempotentAndTokenIndependent(t *testing.T) {
    path := filepath.Join(t.TempDir(), "inbox.db")
    store := openTestStore(t, path)
    envelope := validEnvelope("account-a", "batch-a", []byte("protobuf"))
    first, err := store.Accept(context.Background(), envelope)
    if err != nil { t.Fatal(err) }
    if first.Outcome != ReceiptAccepted { t.Fatalf("outcome = %q", first.Outcome) }
    store.Close()

    store = openTestStore(t, path)
    duplicate, err := store.Accept(context.Background(), envelope)
    if err != nil { t.Fatal(err) }
    if duplicate.Outcome != ReceiptDuplicate { t.Fatalf("outcome = %q", duplicate.Outcome) }
    conflict := envelope
    conflict.Payload = []byte("different")
    conflict.PayloadSHA256 = sha256Hex(conflict.Payload)
    _, err = store.Accept(context.Background(), conflict)
    if !errors.Is(err, ErrIdempotencyConflict) { t.Fatalf("error = %v", err) }
    if got := countCanonicalBatches(t, store); got != 1 { t.Fatalf("batches = %d", got) }
}
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk go test ./internal/inbox -run TestBoltStore -v
```

Expected: FAIL because the package and bbolt dependency do not exist.

- [ ] **Step 3: Implement one-transaction arbitration and explicit sync**

```go
func (s *BoltStore) Accept(ctx context.Context, env Envelope) (AcceptResult, error) {
    if err := s.storageGuard.Check(int64(len(env.Payload))); err != nil { return AcceptResult{}, err }
    var result AcceptResult
    err := s.db.Update(func(tx *bolt.Tx) error {
        key := receiptKey(env.AccountID, env.BatchID)
        if existing := tx.Bucket(receiptsBucket).Get(key); existing != nil {
            return decodeDuplicateOrConflict(existing, env.PayloadSHA256, &result)
        }
        sequence, err := tx.Bucket(metaBucket).NextSequence()
        if err != nil { return err }
        return insertEnvelope(tx, sequence, env, &result)
    })
    if err != nil { return AcceptResult{}, err }
    if err := s.db.Sync(); err != nil { return AcceptResult{}, err }
    return result, nil
}
```

Buckets are `meta`, `batches`, `fifo`, `receipts`, and `quarantine`. Receipts retain digest/outcome/time after canonical bytes are removed. `PeekEligible` returns only the oldest FIFO record; a delayed head prevents later delivery.

- [ ] **Step 4: Run store tests GREEN and dependency audit**

```bash
rtk go test ./internal/inbox -v
rtk go list -m all
```

Expected: PASS; concurrent same-key calls create one record, reopen preserves pending FIFO/receipts, sync failure prevents accepted outcome, and dependency list includes the pinned bbolt module once.

- [ ] **Step 5: Commit**

```bash
rtk git add services/trace-gateway/internal/inbox/types.go services/trace-gateway/internal/inbox/store.go services/trace-gateway/internal/inbox/store_test.go services/trace-gateway/go.mod services/trace-gateway/go.sum
rtk git commit -m "feat(gateway): persist Trace batches in bbolt"
```

### Task 15: Add the Persistent FIFO Langfuse Delivery Worker

**Files:**
- Create: `services/trace-gateway/internal/delivery/worker.go`
- Create: `services/trace-gateway/internal/delivery/worker_test.go`
- Modify: `services/trace-gateway/internal/inbox/store.go`

**Interfaces:**
- Produces `New(store inbox.Store, upstream Upstream, options Options) *Worker`, `Run(ctx) error`, and `Trigger()`.
- `Upstream.Send(ctx, payload) (Response, error)` returns status and `Retry-After`.
- Success removes canonical bytes and retains receipt; retryable failure persists attempts/error/next retry; permanent rejection persists encrypted/server-side quarantine metadata.

- [ ] **Step 1: Write failing FIFO/restart/Retry-After tests**

```go
func TestWorkerRetainsHeadAndResumesFIFOAfterRestart(t *testing.T) {
    store := durableFixture(t, envelopes("one", "two"))
    upstream := &fakeUpstream{responses: []response{
        {status: 503, retryAfter: "600"},
        {status: 200},
        {status: 200},
    }}
    worker := New(store, upstream, deterministicOptions())
    runOne(t, worker)
    if !slices.Equal(upstream.seen, []string{"one"}) { t.Fatalf("seen = %v", upstream.seen) }
    if got := pending(t, store, "one").NextRetry.Sub(testNow); got != 600*time.Second { t.Fatalf("retry = %s", got) }
    reopenStore(t, store)
    advance(600 * time.Second)
    runUntilIdle(t, New(store, upstream, deterministicOptions()))
    if !slices.Equal(upstream.seen, []string{"one", "one", "two"}) { t.Fatalf("seen = %v", upstream.seen) }
}
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk go test ./internal/delivery -v
```

Expected: FAIL because the delivery package does not exist.

- [ ] **Step 3: Implement one coalesced worker loop**

```go
func (w *Worker) deliverHead(ctx context.Context, now time.Time) error {
    batch, err := w.store.PeekEligible(ctx, now)
    if err != nil || batch == nil { return err }
    response, err := w.upstream.Send(ctx, batch.Payload)
    switch classify(response.Status, err) {
    case success:
        return w.store.MarkDelivered(ctx, batch.BatchID, now)
    case permanent:
        return w.store.MarkQuarantined(ctx, batch.BatchID, safeErrorClass(response.Status), now)
    default:
        retry := fullJitter(batch.Attempts, now, response.RetryAfter, w.random)
        return w.store.MarkRetry(ctx, batch.BatchID, retry)
    }
}
```

One buffered trigger channel coalesces wakeups. Network, 429, and 5xx are retryable. Other upstream 4xx are permanent quarantine. No log includes canonical bytes, authorization, public/secret keys, or upstream body.

- [ ] **Step 4: Run worker and inbox tests GREEN**

```bash
rtk go test ./internal/delivery ./internal/inbox -v
```

Expected: PASS for FIFO, restart, full jitter, long Retry-After, permanent quarantine, response loss retry, and clean logs.

- [ ] **Step 5: Commit**

```bash
rtk git add services/trace-gateway/internal/delivery/worker.go services/trace-gateway/internal/delivery/worker_test.go services/trace-gateway/internal/inbox/store.go
rtk git commit -m "feat(gateway): deliver durable inbox in the background"
```

### Task 16: Implement Gateway Batch/Digest/Receipt Protocol and Defense-in-Depth Redaction

**Files:**
- Modify: `services/trace-gateway/internal/server/server.go`
- Modify: `services/trace-gateway/internal/server/server_test.go`
- Modify: `services/trace-gateway/internal/otlp/canonicalize.go`
- Modify: `services/trace-gateway/internal/otlp/canonicalize_test.go`
- Modify: `services/trace-gateway/internal/redact/redact.go`
- Modify: `services/trace-gateway/internal/redact/redact_test.go`

**Interfaces:**
- `server.Config` consumes `inbox.Store` and a worker trigger instead of synchronous Langfuse credentials/client.
- Request requires one UUIDv4 `Idempotency-Key` and one lowercase 64-hex `X-Trace-Payload-SHA256`.
- Success returns HTTP 200, OTLP success body, matching `X-Trace-Batch-ID`, and `X-Trace-Receipt: accepted|duplicate`.
- `Canonicalize()` consumes stable batch ID for `trace.gateway.batch.id`; request ID remains log-only.

- [ ] **Step 1: Write failing wire tests**

```go
func TestHandlerAcknowledgesOnlySyncedInboxOwnership(t *testing.T) {
    store := &blockingStore{accepted: make(chan struct{})}
    handler := mustHandler(t, Config{Introspector: activeAccount(), Store: store})
    request := validRequest(validBody(t))
    request.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
    request.Header.Set("X-Trace-Payload-SHA256", sha256Hex(validBody(t)))
    response := httptest.NewRecorder()
    done := make(chan struct{})
    go func() { handler.ServeHTTP(response, request); close(done) }()
    assertNotClosed(t, done)
    close(store.accepted)
    <-done
    if response.Code != http.StatusOK { t.Fatalf("status = %d", response.Code) }
    if got := response.Header().Get("X-Trace-Batch-ID"); got != "11111111-1111-4111-8111-111111111111" { t.Fatalf("batch = %q", got) }
    if got := response.Header().Get("X-Trace-Receipt"); got != "accepted" { t.Fatalf("receipt = %q", got) }
}
```

Add table rows for missing/duplicate/invalid idempotency headers, digest mismatch, same key/different digest 409, store-full 507, refresh-required 401 code, explicit revoke 403 code, auth unavailable 503 code, and duplicate durable receipt.

- [ ] **Step 2: Run and verify RED**

```bash
rtk go test ./internal/server ./internal/otlp ./internal/redact -v
```

Expected: FAIL because current server has memory dedupe, synchronously forwards upstream, lacks receipt headers, and redaction does not emit media descriptors.

- [ ] **Step 3: Replace pass-through delivery with durable acceptance**

```go
digest := sha256.Sum256(body)
if hex.EncodeToString(digest[:]) != headers.PayloadSHA256 {
    writeError(w, http.StatusConflict, "payload_digest_mismatch")
    return http.StatusConflict, len(body)
}
if err := otlp.Canonicalize(&export, principal, headers.Trace, headers.BatchID); err != nil { ... }
canonicalBody, err := proto.Marshal(&export)
result, err := s.store.Accept(request.Context(), inbox.Envelope{
    AccountID: principal.AccountID,
    SessionID: principal.SessionID,
    InstallationID: principal.InstallationID,
    BatchID: headers.BatchID,
    PayloadSHA256: headers.PayloadSHA256,
    Payload: canonicalBody,
    Headers: headers.Trace,
})
```

Delete `responseCache`, `requestDigest(tokenID, body)`, and synchronous `forward()`. Map typed auth/store errors exactly. Redaction converts OTLP bytes values and base64 data URLs to `{redacted,media_type,byte_size,sha256}` JSON strings before inbox persistence. Canonical identity uses immutable `account_id`; username remains display metadata.

- [ ] **Step 4: Run all Gateway packages GREEN**

```bash
rtk go test ./... -v
```

Expected: PASS; accepted follows store sync, duplicate survives new token identity, conflict retains original, errors are fixed no-store JSON, and logs contain no token/payload/credentials.

- [ ] **Step 5: Commit**

```bash
rtk git add services/trace-gateway/internal/server/server.go services/trace-gateway/internal/server/server_test.go services/trace-gateway/internal/otlp/canonicalize.go services/trace-gateway/internal/otlp/canonicalize_test.go services/trace-gateway/internal/redact/redact.go services/trace-gateway/internal/redact/redact_test.go
rtk git commit -m "feat(gateway): acknowledge durable idempotent Trace batches"
```

### Task 17: Wire Gateway Store/Worker Lifecycle and Persistent Deployment Volume

**Files:**
- Modify: `services/trace-gateway/cmd/gateway/main.go`
- Modify: `deploy/voice-trace/docker-compose.yml`
- Modify: `deploy/voice-trace/.env.example`
- Modify: `tests/test_voice_trace_compose_contract.py`

**Interfaces:**
- Gateway config values are exact deployment inputs:
  - `TRACE_GATEWAY_INBOX_PATH=/data/inbox.db`
  - `TRACE_GATEWAY_RECEIPT_RETENTION=720h`
  - `TRACE_GATEWAY_MAX_DB_BYTES=68719476736` (64 GiB)
  - `TRACE_GATEWAY_MIN_FREE_BYTES=10737418240` (10 GiB)
- Compose mounts `${DEPLOY_ROOT}/data/trace-gateway:/data` only into `trace-gateway`.
- Main closes HTTP admission, cancels worker, waits for worker exit, then closes bbolt on SIGINT/SIGTERM.

- [ ] **Step 1: Write failing deployment/lifecycle tests**

```python
def test_trace_gateway_has_private_durable_inbox_and_exact_safety_limits(self) -> None:
    _, compose = self.load_compose()
    gateway = compose["services"]["trace-gateway"]
    self.assertEqual(gateway["volumes"], [
        "/data/ansatz-agent/voice-trace/data/trace-gateway:/data"
    ])
    self.assertEqual(gateway["environment"]["TRACE_GATEWAY_INBOX_PATH"], "/data/inbox.db")
    self.assertEqual(gateway["environment"]["TRACE_GATEWAY_RECEIPT_RETENTION"], "720h")
    self.assertEqual(gateway["environment"]["TRACE_GATEWAY_MAX_DB_BYTES"], "68719476736")
    self.assertEqual(gateway["environment"]["TRACE_GATEWAY_MIN_FREE_BYTES"], "10737418240")
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract -v
```

Expected: FAIL because Gateway has no volume or inbox environment.

- [ ] **Step 3: Implement validated startup and Compose wiring**

```go
store, err := inbox.Open(required("TRACE_GATEWAY_INBOX_PATH", logger), inbox.Options{
    ReceiptRetention: requiredDuration("TRACE_GATEWAY_RECEIPT_RETENTION", logger),
    MaxDBBytes: requiredPositiveInt64("TRACE_GATEWAY_MAX_DB_BYTES", logger),
    MinFreeBytes: requiredPositiveInt64("TRACE_GATEWAY_MIN_FREE_BYTES", logger),
})
if err != nil { logger.Error("trace inbox unavailable"); os.Exit(1) }
ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()
worker := delivery.New(store, langfuseUpstream, delivery.Options{})
go worker.Run(ctx)
```

Do not log config secrets or bbolt contents. `/healthz` reports unhealthy if the store is closed/unwritable; Langfuse delivery backlog does not make admission health fail while the store can still accept.

- [ ] **Step 4: Run deployment and Go tests GREEN**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract -v
rtk go test ./...
```

Expected: PASS; Compose has one persistent Gateway mount and invalid/missing limits fail startup without secret output.

- [ ] **Step 5: Commit**

```bash
rtk git add services/trace-gateway/cmd/gateway/main.go deploy/voice-trace/docker-compose.yml deploy/voice-trace/.env.example tests/test_voice_trace_compose_contract.py
rtk git commit -m "feat(gateway): persist inbox on a bounded volume"
```

### Task 18: Update Protocol Fixture and Prove Lost-Response/Restart Idempotency

**Files:**
- Modify: `scripts/send_gateway_trace.py`
- Modify: `tests/test_send_gateway_trace.py`
- Create: `services/trace-gateway/internal/server/continuity_integration_test.go`

**Interfaces:**
- `upload_user_trace()` generates one UUIDv4 `batch_id`, hashes the exact protobuf body, sends both headers, and verifies receipt headers.
- Integration test uses a real temporary bbolt file and recreated server/worker instances.

- [ ] **Step 1: Write failing protocol and restart tests**

```python
def test_retry_reuses_batch_and_digest_and_requires_durable_receipt(self) -> None:
    result = module.upload_user_trace(credentials, "A", transport=httpx.MockTransport(handler),
                                      trace_id=bytes.fromhex("33" * 16),
                                      root_span_id=bytes.fromhex("44" * 8),
                                      start_ns=2_000_000_000)
    self.assertEqual(len({request.headers["idempotency-key"] for request in uploads}), 1)
    self.assertEqual(len({request.headers["x-trace-payload-sha256"] for request in uploads}), 1)
    self.assertEqual(result["first_receipt"], "accepted")
    self.assertEqual(result["retry_receipt"], "duplicate")
    self.assertNotIn("access_token", json.dumps(result))
```

```go
func TestLostClientResponseAndGatewayRestartKeepOneLogicalBatch(t *testing.T) {
    path := filepath.Join(t.TempDir(), "inbox.db")
    first := newContinuityServer(t, path, langfuseInitiallyDown())
    discardResponse(t, first.Handler(), validIdempotentRequest(t, "batch-id"))
    first.Close()
    second := newContinuityServer(t, path, langfuseNowHealthy())
    response := serve(t, second.Handler(), validIdempotentRequest(t, "batch-id"))
    if got := response.Header().Get("X-Trace-Receipt"); got != "duplicate" { t.Fatalf("receipt = %q", got) }
    eventually(t, func() bool { return second.LangfuseCalls() == 1 })
    if got := second.StoredReceiptCount(); got != 1 { t.Fatalf("receipts = %d", got) }
}
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_send_gateway_trace -v
rtk go test ./internal/server -run TestLostClientResponseAndGatewayRestartKeepOneLogicalBatch -v
```

Expected: FAIL because the fixture lacks batch/digest headers and no restart integration harness exists.

- [ ] **Step 3: Implement stable fixture headers and process-equivalent reopen**

```python
batch_id = str(uuid.uuid4())
payload_sha256 = hashlib.sha256(payload).hexdigest()
headers.update({
    "Idempotency-Key": batch_id,
    "X-Trace-Payload-SHA256": payload_sha256,
})
first_receipt = _require_receipt(upload, batch_id)
retry_receipt = _require_receipt(retry, batch_id)
```

The Go integration must close the first HTTP server, worker context, and bbolt handle before opening the second. The fake Langfuse records stable OTLP trace/span IDs and rejects any second logical identity.

- [ ] **Step 4: Run protocol, restart, and complete Gateway tests GREEN**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_send_gateway_trace -v
rtk go test ./... -v
```

Expected: PASS; first receipt is accepted, retries across restart/token replacement are duplicate, conflict is 409, and one logical upstream batch remains.

- [ ] **Step 5: Commit**

```bash
rtk git add scripts/send_gateway_trace.py tests/test_send_gateway_trace.py services/trace-gateway/internal/server/continuity_integration_test.go
rtk git commit -m "test(trace): prove durable receipt idempotency across restart"
```

### Task 19: Integrate Stream A Identity and Run Cross-Repository Continuity E2E

**Files:**
- Modify: `apps/desktop/electron/main.ts`
- Modify: `apps/desktop/electron/auth-bridge.ts`
- Modify: `apps/desktop/electron/trace-outbox-types.ts`
- Modify: `apps/desktop/electron/trace-continuity.integration.test.ts`
- Modify: `services/trace-gateway/internal/auth/introspector.go`
- Modify: `services/trace-gateway/internal/server/server_test.go`
- Modify: `scripts/send_gateway_trace.py`

**Interfaces:**
- Consumes Stream A cached scope fields `account_id`, `session_id`, `installation_id`, and native Session Trace-token issuance.
- Produces `traceOwnerFromScope(scope): TraceOwner`; legacy cached scope produces `accountId/sessionId=null`, persists locally, and never uploads until upgraded.
- Gateway explicit 403 evidence is forwarded to the auth owner only when returned account/session IDs match the active cached scope.

- [ ] **Step 1: Write the failing integrated scenario**

```ts
test('auth and Gateway outage never blocks local Trace capture and recovery uploads once', async () => {
  const system = await continuitySystem({ auth: 'online', gateway: 'online' })
  await system.login(accountA)
  await system.stopAuthAndGateway()
  await system.restartClient()
  assert.equal(system.localBackendReady(), true)
  assert.equal((await system.emitTrace('offline-turn')).status, 200)
  assert.equal(system.outbox().pending, 1)
  await system.restoreAuthAndGateway()
  await system.focusWindow()
  await system.waitForUpload()
  assert.equal(system.outbox().pending, 0)
  assert.equal(system.gateway().logicalBatchCount, 1)
  assert.equal(system.agentModelExecutions('offline-turn'), 1)
})
```

- [ ] **Step 2: Run and verify RED on the integration branch**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-continuity.integration.test.ts
rtk go test ./internal/auth ./internal/server -v
```

Expected: FAIL until Stream A account/session fields are mapped into `TraceOwner` and structured Gateway 403 evidence is connected.

- [ ] **Step 3: Implement the narrow integration mapping**

```ts
export function traceOwnerFromScope(scope: CachedAuthorizationScope): TraceOwner {
  return validateTraceOwner({
    accountKey: scope.account_id ? `account-${scope.account_id}` : `legacy-${scope.legacy_principal_digest}`,
    accountId: scope.account_id,
    sessionId: scope.session_id,
    installationId: scope.installation_id
  }).owner
}
```

When a legacy owner upgrades online, close both stores, atomically rename the legacy namespace to the trusted account namespace only after the trusted wrapped key/account record is synced, reopen, and journal a migration record. Never merge two different account namespaces.

- [ ] **Step 4: Run complete cross-repository proof GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/agent/test_ansatz_trace_policy.py tests/plugins/test_nemo_relay_plugin.py -v
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/trace-continuity.integration.test.ts electron/trace-forwarder.test.ts electron/trace-outbox-store.test.ts electron/trace-credential-provider.test.ts
rtk npm --prefix apps/desktop run typecheck
rtk npm --prefix apps/desktop run lint
rtk go test ./... -v
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract tests.test_send_gateway_trace -v
bash tests/run.sh
```

Expected: every command exits 0; offline local conversation and durable capture continue, recovery uploads FIFO exactly once, 401 refreshes only Trace credential, explicit matching revocation stops upload/capability through Stream A, and SessionDB/attachments remain unchanged.

- [ ] **Step 5: Commit the integration resolution**

```bash
rtk git add apps/desktop/electron/main.ts apps/desktop/electron/auth-bridge.ts apps/desktop/electron/trace-outbox-types.ts apps/desktop/electron/trace-continuity.integration.test.ts services/trace-gateway/internal/auth/introspector.go services/trace-gateway/internal/server/server_test.go scripts/send_gateway_trace.py
rtk git commit -m "feat(trace): integrate durable upload with native sessions"
```

### Task 20: Write Operations Handoff and Record Fresh Completion Evidence

**Files:**
- Create: `docs/runbooks/trace-upload-continuity.md`
- Create: `docs/reports/2026-08-24-trace-upload-continuity-e2e.md`
- Modify: `docs/02-progress.md`
- Modify: `docs/03-file-index.md`

**Interfaces:**
- Runbook defines client outbox paths, key-loss behavior, 2 GiB/30-day policy, diagnostics, Gateway 64 GiB/10 GiB thresholds, receipt retention, backup, safe compaction, recovery, rollout, and rollback.
- Report contains exact refs, commands, exit statuses, observed batch IDs/counts without credentials or payloads, and requirement-by-requirement evidence.

- [ ] **Step 1: Write a failing documentation contract test**

Add to `tests/test_voice_trace_compose_contract.py`:

```python
def test_trace_continuity_runbook_documents_exact_storage_and_recovery_contract(self) -> None:
    text = (ROOT / "docs" / "runbooks" / "trace-upload-continuity.md").read_text()
    for required in (
        "2 GiB per account", "30 days", "64 MiB segment", "AES-256-GCM",
        "Brotli", "1 GiB or 5%", "64 GiB", "10 GiB", "720h",
        "accepted", "duplicate", "507 storage_unavailable", "rollback",
    ):
        self.assertIn(required, text)
```

- [ ] **Step 2: Run and verify RED**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract.VoiceTraceComposeContractTest.test_trace_continuity_runbook_documents_exact_storage_and_recovery_contract -v
```

Expected: FAIL because the runbook does not exist.

- [ ] **Step 3: Write the runbook and evidence report with actual outputs**

The runbook must give exact read-only diagnostic commands, bbolt backup/restore prerequisites, the rule that accepted-undelivered data is never auto-evicted, safe offline compaction steps, client key-loss quarantine behavior, and additive rollback that never deletes inbox/outbox/receipts/conversations. Start it with this exact contract block, then add the concrete commands and observed paths from the implemented build:

```markdown
# Trace Upload Continuity Runbook

## Storage contracts

- Client: 2 GiB per account, 30 days, 64 MiB segment, Brotli then AES-256-GCM.
- Client reserve: the greater of 1 GiB or 5% of the containing volume.
- Gateway: 64 GiB bbolt ceiling, 10 GiB minimum free space, 720h receipts.
- `accepted` and `duplicate` transfer durable ownership; `507 storage_unavailable` does not.

## Rollback invariant

Rollback stops using additive routes and fields; it never deletes the outbox,
inbox, receipts, SessionDB, attachments, or local conversations.
```

The report must copy actual command output summaries and exit statuses from Task 19; do not write a passing claim before those commands run.

- [ ] **Step 4: Run fresh final verification and audit GREEN**

```bash
rtk git status --short
rtk git diff --check
rtk git diff --name-only origin/main...HEAD
rtk rg -n 'fix/relay-token-cost|BEGIN .*PRIVATE KEY|Bearer [A-Za-z0-9._~+/=-]{8,}|sk-[A-Za-z0-9_-]{12,}' . --glob '!docs/superpowers/specs/**' --glob '!docs/superpowers/plans/**'
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract tests.test_send_gateway_trace -v
rtk go test ./... -v
```

Run the corresponding client Task 19 commands again in the client integration worktree. Expected: all tests exit 0; `git diff --check` is empty; changed files are limited to the authorized file list in this plan; secret-scan matches are reviewed as synthetic fixtures or rejected as real credentials; no prohibited-branch provenance appears; report evidence matches fresh output.

- [ ] **Step 5: Update routing docs and commit documentation**

Only after Step 4 evidence exists, update `docs/02-progress.md` with the achieved status and `docs/03-file-index.md` with the new runbook/report paths.

```bash
rtk git add docs/runbooks/trace-upload-continuity.md docs/reports/2026-08-24-trace-upload-continuity-e2e.md docs/02-progress.md docs/03-file-index.md tests/test_voice_trace_compose_contract.py
rtk git commit -m "docs(trace): record continuity operations and evidence"
```

## Dependency Order and Parallel Execution

```text
Client: 1 ─┐
        2 ─┼─> 3 -> 4 -> 5 -> 6 -> 7 -> 8 -> 9 -> 10 -> 11 -> 12 ─┐
                                                                      ├─> 19 -> 20
Gateway: 13 -> 14 -> 15 -> 16 -> 17 -> 18 ──────────────────────────┘
```

- Tasks 1 and 2 may run in parallel before Electron work.
- Gateway Tasks 13–18 may run in parallel with client Tasks 1–12, but keep their internal order.
- Task 19 runs only after Stream A native Session/account identity contracts and both Stream B branches are reviewed.
- Task 20 runs only after Task 19 fresh evidence; it may not convert planned behavior into progress claims early.

## Plan Self-Review

- Spec coverage: redundant history, media pressure, Brotli/AES-GCM, 64 MiB segments, checksummed journal, 50 ms group commit, fsync-before-ack, recovery, FIFO, limits, retention, free reserve, quarantine, dedupe, tombstones, compaction, retry, every trigger, token race, 401 behavior, durable Gateway acceptance, token-independent bbolt receipts, worker retry, volume configuration, restart/lost-response E2E, security, operations, and final evidence all map to explicit tasks.
- Type consistency: client tasks consistently use `TraceOwner`, `TraceEnvelopeInput`, `DurableTraceBatch`, `DurableReceipt`, and `OutboxSendResult`; Gateway tasks consistently use `inbox.Envelope`, `AcceptResult`, `ReceiptAccepted`, and `ReceiptDuplicate`.
- Scope consistency: turn mark history is removed only in the NeMo Relay plugin projection; `RelaySessionCoordinator` and model request serialization remain unchanged.
- Placeholder scan: no deferred fields, copy-by-reference steps, or unspecified implementation actions remain. Gateway deployment thresholds are fixed at 64 GiB maximum DB bytes and 10 GiB minimum free bytes; receipt tombstones are fixed at 64 MiB/100,000 entries.
- Provenance: both pinned refs and the prohibition on `fix/relay-token-cost` are explicit global constraints and final audit inputs.
