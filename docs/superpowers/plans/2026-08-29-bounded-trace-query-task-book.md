# Bounded Trace Query Bug-Fix Task Book

**Date:** 2026-08-29
**Design:** `docs/superpowers/specs/2026-08-29-bounded-trace-query-design.md`
**Delivery:** Two reviewed PRs, merged `main` commits, production deployment, and verified rollback

## Task 1: Prove production provenance and preserve rollback

**Evidence:** production container/image identity, deployed configuration hashes, Git ancestry.

1. Require Auth production commit `01c73ca1ad` to be an ancestor of service `origin/main`.
2. Require deployed Compose and ClickHouse profiler hashes to match files reachable from platform
   `origin/main`.
3. Record the current Auth image ID, ClickHouse container ID/restart count, profile file, and Voice
   Trace business row counts before deployment.

**Expected:** all provenance checks succeed before implementation or production mutation.

## Task 2: Add failing bounded-query service tests

**Repository:** `agent-langfuse-server`

**Modify:**

- `auth-service/history/tests/test_trace_dashboard.py`

**Tests first:**

1. Change the collection contract from `limit=1000` to `limit=100`, with no `io` field.
2. Add an 843-lightweight-observation session test whose fake transport rejects any collection
   request containing IO.
3. Require session detail to call collection mode and omit payload previews.
4. Require trace detail/fragment to fetch only the selected owner-scoped observation payload.
5. Require the detail filter to contain user ID, trace ID, and observation ID.
6. Require invalid/foreign step selection not to fetch that payload.
7. Require an over-8-MiB selected payload to keep the shell usable with a local error.

**Red proof:**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_dashboard --verbosity 1
```

Expected failures identify the existing `include_io=True`, `limit=1000`, and whole-trace payload
fetch behavior.

## Task 3: Implement bounded service queries

**Repository:** `agent-langfuse-server`

**Modify:**

- `auth-service/history/langfuse_client.py`
- `auth-service/history/trace_views.py`
- `auth-service/templates/traces/session_detail.html`
- `auth-service/templates/traces/_trace_step_panel.html`
- `auth-service/history/tests/test_trace_dashboard.py`

**Interfaces:**

- Keep `list_observations(...)` lightweight and bounded to 100 rows/page, 20 pages, 2,000 rows.
- Add `get_observation(*, user_id, trace_id, observation_id, include_io=True)` using one structured,
  three-identity filter and `limit=1`.
- Add a distinct oversized-payload result handled inside the selected panel.
- Make `session_detail`, `trace_detail`, and `trace_step_fragment` use the lightweight index; only
  the selected observation performs a detail call.

**Green proof:**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_dashboard --verbosity 1
rtk proxy conda run -n dl python manage.py test history.tests --verbosity 1
rtk proxy conda run -n dl python manage.py check
git diff --check
```

Expected: targeted and full History tests pass, Django reports no issues, diff check is clean.

## Task 4: Add failing ClickHouse guard contract test

**Repository:** `ansatz-agent-platform`

**Modify:**

- `tests/test_voice_trace_compose_contract.py`

Test parsed `profilers.xml` for exact defaults and maximum constraints for memory, result bytes,
execution time, and threads. The execution-time value is 35 seconds because production evidence
shows Langfuse explicitly requests that value; the maximum constraint must not reject it.

**Red proof:**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract -v
```

Expected: new guard assertions fail against the unlimited current profile.

## Task 5: Implement and verify ClickHouse guard

**Repository:** `ansatz-agent-platform`

**Modify:**

- `deploy/voice-trace/clickhouse/users.d/profilers.xml`
- `tests/test_voice_trace_compose_contract.py`
- `docs/runbooks/storage-auth-personal-traces.md`
- `docs/02-progress.md` only after production verification
- `docs/03-file-index.md` to route to this design/task book

**Green proof:**

```bash
rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract -v
bash tests/run.sh
git diff --check
```

Expected: contract and full platform suites pass with no shellcheck/config regression.

## Task 6: Review, PR, merge, and synchronize

For each repository:

1. Inspect intended diff and run a secret scan.
2. Commit only task-owned files.
3. Push the task branch and open a non-draft PR against `main`.
4. Wait for required checks; fix failures test-first.
5. Merge only after checks pass.
6. Fetch canonical main and fast-forward only.
7. Prove local `main == origin/main == merged commit` and both canonical/feature worktrees are clean.

## Task 7: Deploy merged ClickHouse guard

1. Reconfirm platform candidate commit is reachable from current `origin/main`.
2. Back up deployed Compose/profile, business row counts, and all container IDs.
3. Use the reviewed bounded ClickHouse remediation procedure to copy the merged profile and restart
   only the exact ClickHouse container.
4. Query `system.settings` and prove effective values and maximum constraints.
5. Run the complete Voice Trace health check; rollback immediately on normal-query regression.

## Task 8: Build and deploy merged Auth service

1. Reconfirm clean canonical service `main`, `HEAD == origin/main`, and merged commit ancestry.
2. Build/test the Auth image from that exact commit using the established release path.
3. Record image tag and SHA-256; preserve current image
   `localhost/ansatz-auth-service:main-20260827-01c73ca1ad` as rollback.
4. Back up the Auth database and owner-only environment file without exposing their contents.
5. Recreate only Auth and explicit Voice Trace dependents required by the deployment helper.
6. Verify the running image ID/tag matches the reviewed build.

## Task 9: Production acceptance and completion evidence

1. Capture pre-test ClickHouse/Langfuse restart counts and host memory.
2. Open the pathological session repeatedly and its known 3.9 MiB observation.
3. Require successful login and normal Dashboard/Session/Trace responses.
4. Require unchanged restart counts, no new kernel OOM event, and no query above the configured
   ClickHouse memory guard.
5. Run `bash scripts/check-voice-trace.sh` and verify unrelated host workloads were not mutated.
6. Record merged commits, image digest, deployed profile hash, rollback identifiers, and acceptance
   evidence in `docs/02-progress.md` before claiming completion.
