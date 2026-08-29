# Bounded Trace Query Bug-Fix Design

**Status:** Approved by user on 2026-08-29
**Date:** 2026-08-29
**Classification:** Architectural bug fix
**Affected repositories:** `agent-langfuse-server`, `ansatz-agent-platform`

## Problem

The ordinary-user `/traces` application requests every observation in a session or trace with
`io,metadata` included. One production session contains 843 observations and about 483.54 MiB of
stored input/output/metadata. Materializing that response caused ClickHouse to exceed 5 GiB RSS and
be killed by the host OOM killer; Langfuse Web then exhausted its 1.5 GiB JavaScript heap. The user
observed intermittent 502/503 responses after a successful login.

The existing session-first inspector bounds browser rendering but does not bound the upstream
Langfuse/ClickHouse query.

## Goals

1. Never fetch all session or trace payloads to render an index or navigation shell.
2. Fetch IO only for the currently selected, already owner-scoped observation.
3. Keep oversized payload failure local to the selected content panel.
4. Prevent one ClickHouse query from exhausting the 7.6 GiB production host.
5. Preserve owner isolation, escaping, existing routes, stored data, and rollback capability.

## Non-goals

- Modifying upstream Langfuse Web or Worker source code.
- Deleting, truncating, migrating, or rewriting business Trace data.
- Adding exports or downloads for payloads larger than the online-view limit.
- Fixing host reboot orchestration; that is a separate incident follow-up.
- Raising the Langfuse Node.js heap or resizing the host as a substitute for bounded queries.

## Design

### Lightweight collection queries

`LangfuseClient.list_observations` returns only core/basic/time/model/usage/metrics/trace-context
fields. Session detail, trace detail, and the step rail use this method and never request IO or full
metadata. Session rows retain identity, time, duration, model, usage, cost, errors, and step counts;
request/response previews are removed because Langfuse cannot return bounded prefixes without first
materializing the full values.

Collection requests use a 100-row page size, at most 20 pages, and at most 2,000 accumulated rows.
Exceeding the bound returns a controlled unavailable result without an unscoped fallback.

### One owner-scoped payload query

A new `get_observation` method queries Langfuse v2 with a structured filter containing all three
equalities: authenticated `userId`, `traceId`, and observation `id`. It requests IO and normally
truncated metadata for exactly one row. The view first confirms that the observation ID occurs in
the already owner-scoped lightweight trace index.

The selected-step fragment fetches one payload. Trace Overview fetches only the root observation;
it uses that root's captured input/output and does not scan the trace for another full payload.
Invalid or foreign step IDs fall back to Overview without issuing a payload lookup for the supplied
ID.

### Payload and failure bounds

The Auth service retains its 8 MiB upstream-response cap. A dedicated payload-too-large exception
renders the trace shell and an explicit selected-panel error instead of converting the whole page
to 503. Langfuse timeout or invalid response remains a generic 503 without leaking upstream detail.

### ClickHouse guardrail

The deployed default ClickHouse profile receives measured host-safe defaults and matching maximum
constraints:

- `max_memory_usage = 1 GiB`
- `max_result_bytes = 64 MiB`, overflow mode `throw`
- `max_execution_time = 35 seconds`, overflow mode `throw` (matching Langfuse's explicit setting)
- `max_threads = 2`

The maximum constraints prevent a query from raising these values above the profile guard. The
guardrail is defense in depth: normal Trace pages must already avoid the pathological query.

## Security and Privacy

- The browser never supplies an account selector.
- Structured detail filters contain authenticated user, trace, and observation identity together.
- Local owner filtering remains in place after every upstream response.
- Foreign and nonexistent IDs remain indistinguishable.
- Payloads remain template-escaped; credentials remain server-side and absent from logs/evidence.

## Failure and Recovery

- Collection bound exceeded: generic unavailable page; ClickHouse and Langfuse stay running.
- Selected payload over 8 MiB: only the selected content panel reports that it is too large.
- ClickHouse guard rejection: upstream request fails without host OOM or container restart.
- Rollback Auth by restoring the recorded prior image.
- Rollback ClickHouse by restoring the backed-up `profilers.xml` and restarting only the exact
  ClickHouse container with the existing guarded remediation procedure.

## Testing and Acceptance

1. A synthetic 843-observation session performs only lightweight collection queries.
2. Session detail never requests IO and renders no request/response payload previews.
3. Trace detail fetches at most one payload for its selected panel.
4. Invalid/foreign step selection does not issue a detail query for that ID.
5. A response above 8 MiB leaves the trace shell usable and does not return a whole-page 503.
6. ClickHouse effective settings equal the four guard values and reject higher overrides.
7. The production pathological session and a 3.9 MiB observation open without increasing
   ClickHouse or Langfuse Web restart counts.
8. Login, Dashboard, Session index/detail, Trace detail, Langfuse admin health, and Trace ingest
   health all pass after deployment.

## Rollout

1. Approve this design and its companion task book.
2. Implement both repositories test-first in isolated worktrees.
3. Merge reviewed PRs to their respective `main` branches.
4. Apply the merged ClickHouse guard first and verify normal queries.
5. Build Auth only from the exact merged service `origin/main`, deploy it, and run production
   acceptance.

## Decision

Approve removal of Session request/response previews, one-observation lazy IO with an 8 MiB online
view cap, and the ClickHouse 1 GiB / 64 MiB / 35 s / 2-thread guardrail. The approved 30-second
draft was corrected to 35 seconds after production query-log evidence proved that Langfuse
explicitly sets 35 seconds; a lower maximum constraint would reject otherwise valid queries.
