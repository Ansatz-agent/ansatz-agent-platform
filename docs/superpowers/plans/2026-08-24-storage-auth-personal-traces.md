# Storage, Auth, and Personal Traces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkboxes so verification evidence can be recorded without creating commits.

**Goal:** Move the Hermes platform to `/data`, replace `/agent` with `/auth`, add an owner-scoped `/traces` dashboard, and keep complete authenticated Trace upload working for the packaged client.

**Architecture:** The existing Django authentication service owns `/auth` and the server-rendered `/traces` UI. It queries Langfuse v2 observations only from the server with Project API credentials and forces every ordinary-user read to `str(request.user.pk)`, grouping observations by Trace and session. The Trace Gateway remains the only client ingest path and canonicalizes the authenticated immutable user ID plus username metadata. Podman storage and all platform state move physically to `/data` through guarded, reversible bind mounts while retaining the Podman 3.3.1-compatible logical graphroot.

**Tech Stack:** Django, Go, OpenTelemetry/OTLP, Langfuse Project API, Nginx Proxy Manager, Podman Compose, Python contract tests, Hermes Python client, TypeScript desktop E2E fixture.

**Spec:** `docs/superpowers/specs/2026-08-24-storage-auth-personal-traces-design.md`

## Global Constraints

- Preserve all existing account, PostgreSQL, ClickHouse, Redis, MinIO, and Langfuse state.
- Do not expose Langfuse Project API credentials to a browser or client.
- Do not trust client- or browser-provided user IDs, usernames, roles, or metadata.
- Do not retain a compatibility route for `/agent`; it must return 404 after cutover.
- Do not add plaintext credentials, tokens, API keys, deployment secrets, or copied production data to Git.
- Follow red-green-refactor for every behavior change and run fresh verification before any completion claim.
- Use `git diff` and command output as checkpoints. Do not commit, push, merge, or open a PR unless the user separately requests it.

---

### Task 1: Lock the deployment and storage contracts

**Files:**

- Modify: `tests/test_voice_trace_compose_contract.py`
- Modify: `tests/test_voice_trace_proxy_contract.py`
- Create: `tests/test_storage_migration_contract.py`
- Modify: `deploy/voice-trace/docker-compose.yml`
- Modify: `deploy/voice-trace/.env.example`
- Modify: `deploy/voice-trace/nginx/server_proxy.conf`
- Modify: `deploy/voice-trace/scripts/configure-npm.sh`
- Modify: `deploy/voice-trace/scripts/deploy-voice-trace.sh`
- Create: `deploy/voice-trace/scripts/migrate-hermes-storage-to-data.sh`

- [x] Add failing contract assertions that every persistent bind mount resolves beneath `/data/ansatz-agent/voice-trace/data`, auth receives private Langfuse API variables, `/auth` and `/traces` proxy to `auth-service`, `/agent` returns 404, and internal routes remain blocked.
- [x] Add failing migration-script tests for an independent `/data` mount, minimum free-space check, exact old/new graphroot validation, preserved running-container manifest, staged and final copies, configuration backup, rollback before cleanup, and refusal to delete an unverified path.
- [x] Run `rtk proxy conda run -n dl python -m unittest tests.test_voice_trace_compose_contract tests.test_voice_trace_proxy_contract tests.test_storage_migration_contract -v` from the platform worktree. Expected: new assertions fail because the current layout still targets `/root` and `/var/lib/containers` and the migration script is absent.
- [x] Make the minimum Compose, proxy, deploy-script, and migration-script changes. Invoke all shell scripts through `bash`; never execute them directly.
- [x] Re-run the same command. Expected: all named contract tests pass.

### Task 2: Canonicalize authenticated username and Langfuse usage

**Files:**

- Modify: `services/trace-gateway/internal/auth/introspector.go`
- Modify: `services/trace-gateway/internal/auth/introspector_test.go`
- Modify: `services/trace-gateway/internal/otlp/canonicalize.go`
- Modify: `services/trace-gateway/internal/otlp/canonicalize_test.go`

- [x] Add failing Go tests proving introspection requires and carries `platform_username`, hostile client username metadata is removed, the trusted username is written to `langfuse.trace.metadata.username`, and NeMo Relay input/output/total/cache/reasoning usage maps to `langfuse.observation.usage_details`.
- [x] Run `rtk go test ./internal/auth ./internal/otlp` from `services/trace-gateway`. Expected: the new tests fail against the current principal and canonicalization logic.
- [x] Implement the smallest principal and OTLP projection changes. Reuse the previously validated usage mapping from the isolated `fix/langfuse-token-usage` worktree without copying unrelated edits.
- [x] Add a red-green regression for the observed `openai/openai/gpt-5.5` value and collapse only an adjacent duplicated provider prefix so Langfuse receives the priced `openai/gpt-5.5` alias.
- [x] Re-run `rtk go test ./internal/auth ./internal/otlp`. Expected: both packages pass.
- [x] Run `rtk go test ./...`. Expected: the complete Gateway suite passes.

### Task 3: Replace the public `/agent` authentication surface with `/auth`

**Files:**

- Modify: `auth-service/config/settings.py`
- Modify: `auth-service/config/urls.py`
- Modify: `auth-service/history/auth_views.py`
- Modify: `auth-service/history/tests/test_auth_views.py`
- Modify: `auth-service/templates/registration/login.html`
- Modify: `auth-service/templates/base.html`

- [x] Add failing Django tests for `/auth/login/`, `/auth/logout/`, `/auth/api/session/`, `/auth/api/trace-token/`, private introspection, the `__Host-ansatz_sessionid` and `__Host-ansatz_csrftoken` cookie contract, `sub`, username, role, Dashboard URL, and the absence of public `/agent` routes.
- [x] Add a failing introspection assertion for `platform_username` derived from the authenticated Django user row.
- [x] Run the auth test module inside the existing auth-service image with the worktree source bind-mounted. Expected: the new route, cookie, and response assertions fail while the database is an isolated test database.
- [x] Define explicit `/auth` URL patterns without `FORCE_SCRIPT_NAME`, update session/cookie settings, redirect successful browser login to `/traces/`, and extend session/introspection output without changing password storage.
- [x] Re-run the same test module in the image. Expected: all authentication tests pass.

### Task 4: Build the owner-scoped lightweight `/traces` Dashboard

**Files:**

- Create: `auth-service/history/langfuse_client.py`
- Create: `auth-service/history/trace_views.py`
- Create: `auth-service/history/tests/test_trace_dashboard.py`
- Modify: `auth-service/config/urls.py`
- Create: `auth-service/templates/traces/dashboard.html`
- Create: `auth-service/templates/traces/session_detail.html`
- Create: `auth-service/templates/traces/trace_detail.html`
- Modify: `auth-service/static/css/app.css`

- [x] Write failing unit tests with an injected fake Langfuse transport. Prove list queries always use `userId=str(request.user.pk)`, accept only bounded 7/30/90-day ranges, and group only owned traces into sessions.
- [x] Write failing tests proving session and Trace detail use the authenticated user ID, a foreign Trace returns 404 even when its ID is known, Langfuse errors render a generic retryable state, and response HTML escapes Trace data.
- [x] Write failing aggregation tests for sessions, traces, input/output/total tokens, cost, active days, daily trend, and model mix.
- [x] Run the new Django test module inside the existing auth-service image. Expected: imports or route assertions fail because the Dashboard does not yet exist.
- [x] Implement a bounded-timeout standard-library Langfuse client, owner-scoped views, and server-rendered templates with Ansatz styling and the approved NVIDIA-inspired information hierarchy.
- [x] Re-run the Dashboard module, then the complete auth-service suite. Expected: all tests pass and no secret appears in rendered HTML.

### Task 5: Point the packaged Hermes client at `/auth`

**Files:**

- Modify: `hermes_cli/client_auth/client.py`
- Modify: `tests/hermes_cli/client_auth/test_client.py`
- Modify: `apps/desktop/e2e/fixed-auth-contract-server.ts`
- Modify any existing desktop contract test that directly asserts the old `/agent` paths.

- [x] Add failing client tests for exact `/auth/login/`, `/auth/logout/`, `/auth/api/session/`, and `/auth/api/trace-token/` paths; host-only cookies with `Path=/`; `/auth` and `/traces` same-origin redirects; and the extended session response.
- [x] Run `bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_client.py -q` from the client worktree. Expected: new path and cookie assertions fail against the current `/agent` implementation.
- [x] Update constants, redirect validation, cookie validation, and session parsing while preserving the mandatory background Trace uploader.
- [x] Update the fixed desktop auth contract server to match production.
- [x] Re-run the targeted client test, then the relevant desktop E2E contract test through its repository-defined command. Expected: both pass.

### Task 6: Build immutable server images and verify archives

**Files:**

- Generated outside Git: platform-local `tmp/` build manifests, image archives, and checksums.

- [x] Inspect every referenced remote build script before invoking it with `bash`.
- [x] On `l40s`, verify `hostname`, GPU inventory only if GPU work is involved, the task-scoped directory beneath `/mnt/workspace/l40s/yuxiao/`, and available disk space.
- [x] Copy only source/build context to the task directory, build pinned auth-service and Trace Gateway images, and run their unit suites inside the build environment.
- [x] Save each image to a named archive, calculate SHA-256 through the approved proxy, transfer it to Hermes, recalculate SHA-256 there, and require an exact match.
- [x] Load the images on Hermes under unique immutable tags. Expected: `podman image inspect` resolves both tags and no source-controlled file contains a deployment secret.

### Task 7: Migrate Hermes storage to `/data`

**Files:**

- Remote runtime: `/etc/fstab` bind mounts preserving logical `/var/lib/containers/storage`
- Remote data: `/data/containers/storage/`
- Remote release: `/data/ansatz-agent/voice-trace/`
- Evidence: project-local `tmp/` plus remote `/data/ansatz-agent/voice-trace/evidence/`

- [x] Record `df`, `findmnt`, `podman info`, exact running containers, account count, key database/table counts, and public health results before migration.
- [x] Run the migration script in preflight mode. Expected: it proves `/data` is a separate filesystem with capacity for the current graphroot plus a safety margin.
- [x] Stage the metadata-preserving graphroot and platform-data copy while services run.
- [x] Enter the bounded maintenance window: gracefully stop verified containers, perform the final update copy, save and update `fstab`, bind the new physical graphroot/CNI paths beneath their Podman-compatible logical paths, isolate overlay propagation, and start the previously running containers.
- [x] Recreate the voice-trace stack using `/data/ansatz-agent/voice-trace` and `/data` bind mounts, then verify all health checks, account/data counts, and public HTTPS routes.
- [x] Only after verification marks the physical graphroot committed, use an isolated root-filesystem view to clear the exact underlying old contents without traversing active `/data` mounts, then verify root-disk recovery. If any pre-commit check fails, restore the saved `fstab`, exact mounts, and old containers instead.

### Task 8: Deploy `/auth`, `/traces`, and the updated Gateway

**Files:**

- Remote release: `/data/ansatz-agent/voice-trace/deploy/`
- Remote owner-only environment/secrets: `/data/ansatz-agent/voice-trace/secrets/`

- [x] Install the tested Compose and proxy configuration without putting secrets in command output or Git.
- [x] Supply the auth service with the internal Langfuse base URL and existing Project API credentials through owner-only runtime configuration.
- [x] Recreate the auth-service and Gateway from pinned tags and reload the NPM proxy configuration.
- [x] Verify `/agent` is 404; `/auth/login/` and `/traces/` are reachable over HTTPS; `/auth/internal`, `/internal`, and health endpoints remain inaccessible publicly; `/langfuse` remains available to the administrator.

### Task 9: Run production authorization and Trace acceptance tests

**Files:**

- Evidence only: project-local `tmp/` and remote owner-only evidence directory.

- [ ] Log in as two existing ordinary users through `/auth`, verify session `sub` values are distinct and stable, and obtain short-lived upload tokens without exposing them in logs.
- [ ] Upload a real complete client conversation for one user through the normal packaged-client relay path.
- [ ] Prove Langfuse contains its full user input, final assistant output, child model/tool observations, authenticated user ID, username metadata, session ID, non-zero Token, and Cost when the model reports billable usage.
- [ ] Prove User A can list/open only User A sessions and traces; direct requests for User B IDs return 404. Repeat symmetrically for User B.
- [ ] Prove the Langfuse administrator can see all users' traces and that Dashboard HTML contains no Project API key.
- [x] Verify all platform containers, public HTTPS routes, new graphroot, active bind-mount locations, root-disk free space, and persistent-data counts.

### Task 10: Update authoritative documentation and complete the audit

**Files:**

- Modify: `docs/02-progress.md`
- Modify: `docs/03-file-index.md`
- Create or modify the authoritative deployment/migration runbook selected by the file index.
- Modify this plan by checking completed steps and linking evidence paths.

- [x] Document the implemented routes, permission model, `/data` layout, migration/rollback procedure, build image tags/hashes, and operational verification without credentials.
- [x] Run platform, Gateway, auth-service, and targeted client suites fresh and inspect full output and exit status.
- [x] Run secret-pattern scans across all three worktree diffs and inspect each `git diff` for unrelated changes; reviewed expected placeholder/test-only key patterns separately.
- [ ] Audit every acceptance criterion in the approved design against authoritative filesystem, runtime, HTTPS, database, UI, and test evidence.
- [ ] Report any remaining limitation as incomplete work. Claim completion only if all requirements have direct evidence.
