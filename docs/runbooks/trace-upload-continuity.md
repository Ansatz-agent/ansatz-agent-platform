# Trace Upload Continuity Runbook

This runbook covers the encrypted Desktop Trace outbox and the durable Trace
Gateway inbox. It is an operations handoff, not completion evidence. Do not
print Trace payloads, wrapped keys, authentication caches, token responses, or
service environment files while following it.

## Storage contracts

- Client: 2 GiB per account, 30 days, 64 MiB segment, Brotli then AES-256-GCM.
- Client reserve: the greater of 1 GiB or 5% of the containing volume.
- Client receipt tombstones are payload-free and bounded to 64 MiB, 100,000
  entries, and the same 30-day retention window.
- Gateway: 64 GiB bbolt ceiling, 10 GiB minimum free space, 720h receipts.
- Gateway accepted-undelivered data is never auto-evicted. Capacity or
  durability failure rejects new admission and leaves every already accepted
  batch intact.
- `accepted` and `duplicate` transfer durable ownership to the Gateway. After
  either receipt is durably journaled, the client releases the corresponding
  payload; only the bounded receipt tombstone remains. A Gateway-first receipt
  cancels the competing local append, so a successful Trace/span payload is
  not committed to the outbox.
- `503 storage_unavailable` is retryable and does not transfer ownership. The
  Gateway supplies `Retry-After` and an OTLP `application/x-protobuf`
  response. The former insufficient-storage status in the implementation plan
  is obsolete; operators must use this 503 contract.

The outbox capacity and retention limits apply independently to each account
namespace. Quarantined payloads still consume that account's capacity. When
the client must reclaim space, it expires data older than 30 days and then
evicts the oldest unsent or quarantined record until both the 2 GiB limit and
the free-space reserve are satisfied. These loss counters must be treated as
an operational incident, not as successful delivery.

## Authoritative paths and configuration

The Desktop derives its root from Electron's `app.getPath('userData')`; there
is no supported environment-variable override:

```text
<Electron userData>/trace-outbox/<account-key>/
  key.json
  index.journal
  segments/active.segment
```

For the packaged `Ansatz` application the usual user-data roots are
`~/Library/Application Support/Ansatz` on macOS, `%APPDATA%\Ansatz` on
Windows, and `${XDG_CONFIG_HOME:-~/.config}/Ansatz` on Linux. Confirm the
actual Electron user-data root for the installed build before operating on
it. An authenticated namespace is `account-<stable-account-uuid>`; an
offline-upgraded legacy namespace is `legacy-<sha256>`. Never rename, merge,
or copy records between account namespaces by hand.

The production Gateway mount and file are:

```text
host:      /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.db
container: /data/inbox.db
```

The deployed Compose contract uses these exact environment names:

```text
TRACE_GATEWAY_INBOX_PATH=/data/inbox.db
TRACE_GATEWAY_RECEIPT_RETENTION=720h
TRACE_GATEWAY_MAX_DB_BYTES=68719476736
TRACE_GATEWAY_MIN_FREE_BYTES=10737418240
```

`DEPLOY_ROOT=/data/ansatz-agent/voice-trace` determines the host mount. Do not
print `server.env`; it also contains authentication and Langfuse credentials.

## Read-only diagnostics

Use only the following read-only diagnostics before changing state. Start with
the repository health check. Shell scripts must always be invoked
through `bash`:

```bash
bash scripts/check-voice-trace.sh
```

The following remote checks report only process state, file metadata, and free
space. They do not open the database or reveal payloads:

```bash
rtk proxy ssh hermes 'podman inspect ansatz-voice-trace-20260823_trace-gateway_1 --format "state={{.State.Status}} image={{.ImageName}}"'
rtk proxy ssh hermes 'stat -c "inbox_bytes=%s mode=%a owner=%U:%G" /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.db'
rtk proxy ssh hermes 'df -B1 --output=size,used,avail,pcent /data/ansatz-agent/voice-trace/data/trace-gateway | tail -n 1'
```

On macOS, this read-only client check reports aggregate size and file counts
without printing account directory names:

```bash
OUTBOX_ROOT="$HOME/Library/Application Support/Ansatz/trace-outbox"
test ! -d "$OUTBOX_ROOT" || du -sk "$OUTBOX_ROOT"
test ! -d "$OUTBOX_ROOT" || find "$OUTBOX_ROOT" -type f -exec stat -f '%z' {} \; | awk '{bytes += $1; files += 1} END {print "files=" files, "bytes=" bytes}'
```

Use the equivalent confirmed Electron user-data path on Windows or Linux.
Do not run `cat`, `strings`, a hex dump, JSON pretty-printers, or recursive file
listing against `key.json`, `index.journal`, `active.segment`, or `inbox.db`.
Do not attach those files to tickets. The client diagnostic fields intended
for support are counts only: pending/pendingBytes, quarantined, expired,
evictedCapacity, recoveredCorruptTail, keyLost, tombstones/tombstoneBytes,
accepted, and duplicate/deduplicated.

## Client failure and recovery behavior

Each record is Brotli-compressed before AES-256-GCM encryption. The random
data key is account-bound and wrapped with Electron `safeStorage`; the
plaintext key and payload never belong in logs or diagnostics.

- On key loss, existing encrypted records enter `key_loss` quarantine and are
  not uploaded or overwritten. Restoring access to the original OS key store
  permits a later restart to decrypt and resume them. Do not delete or replace
  `key.json` as a repair attempt.
- An owner mismatch fails closed with `trace_outbox_account_mismatch`. The
  client neither reads nor uploads another account's records. Do not move an
  outbox into the currently signed-in account directory.
- Unknown legacy ownership fails closed until a trusted owner can be bound.
- A corrupt non-tail record or unsafe path is quarantined/fails closed. Only a
  verified torn tail is truncated during automatic crash recovery.
- Compaction is automatic and runs only while conversation streaming is idle.
  It rewrites live encrypted records to a synced replacement, atomically
  replaces the segment, and then compacts the journal. Operators must not
  manually compact a live client outbox.

To perform safe offline compaction at the client level, quit Ansatz, verify no
Ansatz process owns the selected account directory, make an owner-only backup
of the complete directory on the same protected volume, and restart Ansatz.
The application performs the supported compaction. If it cannot reopen the
namespace, preserve both the original and backup and escalate; do not edit the
journal or encrypted segment.

## Network, authentication, and token recovery

Timeouts, offline state, DNS/VPN/proxy failures, malformed responses, 429, and
5xx responses pause cloud upload only. They do not revoke authentication,
stop the local Hermes backend, unmount the current conversation, or discard
the FIFO head. The client retries with bounded exponential backoff and honors
valid `Retry-After` values.

Trace token acquisition is asynchronous and single-flight. Recovery is
triggered by a new Trace, startup, the periodic/retry timer, renderer online,
system resume, window focus, token readiness, token-near-expiry, and upload
401. A 401 invalidates only the Trace credential, refreshes it once, and
resends the same batch ID and bytes. It never signs the user out.

A trusted, structured account/session revocation that exactly matches the
current owner stops local capability according to the authentication contract
and structured revocation pauses upload. Transient or malformed revocation
responses do not. Sign out preserves data: it clears authentication
credentials and application access, but preserves the Trace outbox,
SessionDB, attachments, and local conversations.

## Gateway backup prerequisites and procedure

The bbolt file is single-writer state. A filesystem copy is valid only after
the Gateway has fully stopped and closed the database. Before backup:

1. Confirm the exact Compose project/container and inbox path above.
2. Confirm the backup volume has room for at least one full inbox copy while
   leaving the configured 10 GiB reserve.
3. Stop only `trace-gateway`; never stop or prune unrelated services.
4. Verify the container is stopped, then use an audited `bbolt` CLI pinned to
   the repository's `go.etcd.io/bbolt v1.4.3` to run `bbolt check`.
5. Keep the backup owner-only and record its size and SHA-256 without opening
   its contents.

Example closed-database backup sequence (replace the timestamp with one fixed
UTC value for the whole operation):

```bash
rtk proxy ssh hermes 'cd /data/ansatz-agent/voice-trace && /usr/bin/podman-compose --env-file secrets/server.env -f deploy/docker-compose.yml -p ansatz-voice-trace-20260823 stop trace-gateway'
rtk proxy ssh hermes 'test "$(podman inspect ansatz-voice-trace-20260823_trace-gateway_1 --format "{{.State.Running}}")" = false'
rtk proxy ssh hermes 'bbolt check /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.db'
rtk proxy ssh hermes 'install -d -m 0700 /data/ansatz-agent/voice-trace/backups/trace-gateway'
rtk proxy ssh hermes 'install -m 0600 /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.db /data/ansatz-agent/voice-trace/backups/trace-gateway/inbox-YYYYMMDDTHHMMSSZ.db && sync'
rtk proxy ssh hermes 'sha256sum /data/ansatz-agent/voice-trace/backups/trace-gateway/inbox-YYYYMMDDTHHMMSSZ.db; stat -c "bytes=%s mode=%a owner=%U:%G" /data/ansatz-agent/voice-trace/backups/trace-gateway/inbox-YYYYMMDDTHHMMSSZ.db'
rtk proxy ssh hermes 'cd /data/ansatz-agent/voice-trace && /usr/bin/podman-compose --env-file secrets/server.env -f deploy/docker-compose.yml -p ansatz-voice-trace-20260823 up -d trace-gateway'
bash scripts/check-voice-trace.sh
```

If any validation fails, restart the unchanged original database and retain
the failed artifact for offline investigation. Never claim a backup is usable
until a restore rehearsal opens it successfully with the pinned bbolt build.

## Restore and safe offline compaction

Restore requires a separately approved maintenance window, an exact backup
hash, a clean `bbolt check`, matching service UID/GID and mode 0600, enough
space for the backup plus the current database plus the 10 GiB reserve, and a
fresh rescue copy of the current closed database. Restore into a new file,
validate it, then atomically exchange names on the same filesystem. Never copy
over an open `inbox.db`; never delete the rescue copy during the maintenance
window.

For safe offline compaction, use the same prerequisites and keep the original
as the rollback artifact:

```bash
rtk proxy ssh hermes 'bbolt check /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.db'
rtk proxy ssh hermes 'bbolt compact -o /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.compacted.db /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.db'
rtk proxy ssh hermes 'bbolt check /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.compacted.db && stat -c "bytes=%s mode=%a owner=%U:%G" /data/ansatz-agent/voice-trace/data/trace-gateway/inbox.compacted.db'
```

Then set the compacted file to the verified inbox ownership and mode 0600,
move (do not delete) the original into the owner-only backup directory, rename
the compacted file to `inbox.db` on the same filesystem, start the Gateway,
and run the full health check. If startup or health fails, stop the Gateway,
move the failed compacted file aside, restore the untouched original name,
and restart. Accepted-undelivered batches and pending receipts must have the
same counts before and after; compaction is not a retention or eviction tool.

## Rollout and rollback invariant

Roll out the Gateway durable inbox and response fields before requiring them
from new clients. Back up the closed inbox before changing the Gateway image,
preserve the additive route and fields during mixed-version operation, verify
503 retry behavior without payload disclosure, and then roll out the Desktop
build gradually. Monitor only aggregate backlog, receipt, retry, quarantine,
capacity, and age metrics.

Rollback stops using additive routes and fields; it never deletes the outbox,
inbox, receipts, SessionDB, attachments, or local conversations. Reverting a
client or Gateway binary is not authorization to remove new data. Keep both
stores untouched, restore the last compatible binary/configuration, and allow
the newer component to resume processing after forward rollout. User Sign out
also preserves every data store named above.
