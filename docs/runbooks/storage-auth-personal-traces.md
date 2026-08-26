# Hermes Storage, Auth, and Personal Traces Runbook

Last verified: **2026-08-26**

This runbook operates the production Voice Trace stack on SSH alias `hermes`. It does not cover the unrelated `cv-php8`/single-cv or Nginx Proxy Manager applications except for read-only health verification.

## Runtime contract

- Release root: `/data/ansatz-agent/voice-trace`
- Logical Podman graphroot: `/var/lib/containers/storage`
- Physical graphroot: `/data/containers/storage`, bound at the logical path through `/etc/fstab`
- CNI state: `/data/containers/cni`, bound at `/var/lib/cni`
- Compose project: `ansatz-voice-trace-20260823`
- Public routes: `/auth`, `/traces`, `/trace-ingest/v1/traces`, `/langfuse`
- Retired route: `/agent` and every descendant return 404
- Langfuse's ordinary-user query key is the authenticated Django username because the production
  Gateway projects that trusted value into `user.id`/`langfuse.user.id`. The immutable account UUID
  remains `ansatz.account.id`, and the numeric Django user ID remains `platform.user.id` metadata.

Podman 3.3.1 retains its logical static-directory path in its database. Do not rewrite `storage.conf` to `/data`; verify the bind with:

```bash
rtk proxy ssh hermes 'podman info --format "{{.Store.GraphRoot}}"; findmnt -n -o SOURCE,FSROOT,TARGET -M /var/lib/containers/storage; df -h / /data'
```

Expected: the logical graphroot is `/var/lib/containers/storage`, its FSROOT is `/containers/storage` on the `/data` device, and `/data` has safe free capacity.

## Health verification

From the platform repository:

```bash
bash scripts/check-voice-trace.sh
```

The check requires all eight platform containers to run without host-published ports, validates private Gateway/auth/Langfuse health, then requires public `/agent` 404, `/auth/login/` 200, anonymous `/traces/` 302, and `/langfuse/` 200.

Administrators sign in separately at `/langfuse` with a Langfuse administrator account. Ordinary users sign in at `/auth` and access only `/traces`; the two account systems are intentionally not joined by SSO in this rollout.

`cv-php8` and `npm` are separate running services. Never stop, remove, recreate, prune, or change their images, mounts, external build containers, or networks as part of Voice Trace work.

## ClickHouse diagnostic-log retention

The Voice Trace ClickHouse instance uses repository-owned fragments under
`deploy/voice-trace/clickhouse/`. File logging is limited to warning level,
100 MiB per file and three rotations. High-volume diagnostic system logs that
Langfuse does not consume are disabled, including `metric_log`, `trace_log`,
`text_log`, and profiler-related logs. Query, part, and error logs remain
available with a seven-day TTL. Query, CPU, and memory profilers are disabled
for the default profile.

This is a Langfuse/ClickHouse observability-stack responsibility, not an auth
service responsibility. A repeating `system.metric_log` merge that reaches the
ClickHouse server memory limit can emit warning/trace records into both file
logs and `system.trace_log`; unrestricted trace-level rotation and memory
profiling then amplify the storage growth.

Apply the bounded remediation from the platform repository:

```bash
bash scripts/remediate-clickhouse-logging.sh
```

The helper backs up the prior Compose, deployed fragments, and any fragments
already present in the live container, records
business-table row/byte evidence, validates the rendered Compose without
printing secrets, and restarts
`ansatz-voice-trace-20260823_clickhouse_1` in place. Hermes Podman 3.3.1 does
not permit removing that container while its Langfuse dependents exist, so the
helper copies the reviewed fragments into the stopped container and preserves
every container ID. The repository Compose retains read-only fragment mounts
for a future full-stack deployment. While the exact ClickHouse container is
stopped, the helper deletes only `clickhouse-server.log*` and
`clickhouse-server.err.log*` beneath the Voice Trace log directory. After
restart it truncates only the explicitly disabled `system.*` log tables,
retrofitting seven-day TTLs on retained operational logs. It then proves that
all other host container IDs did not change, the canonical `events_core FINAL`
row count did not decrease, no pre-existing business table disappeared,
profiler settings are zero, disabled logs remain empty, 30-second file growth
is bounded, and the full Voice Trace health check passes.

On failure the helper restores the previous Compose/fragments and recreates
the exact ClickHouse container. Diagnostic rows and old file logs removed by a
successful run are intentionally unrecoverable; Langfuse business tables and
all authentication data remain out of the cleanup whitelist. Never substitute
a global Podman prune, a database drop, or direct deletion below
`data/clickhouse`.

## Deploy tested images

Build auth and Gateway images on the task-scoped L40S directory, run their full tests there, export named gzip archives, and require the same SHA-256 on L40S, the local project `tmp/` directory, and Hermes. Put only owner-readable archives in `$DEPLOY_ROOT/staging`.

Current tested release:

- Auth: `localhost/ansatz-auth-service:main-20260827-01c73ca1ad`; archive SHA-256 `54e641af2bba1418ea76c3512cf09bd7777e5c7da3dd23d4e53fe7014b2fb399`
- Trace Gateway: `localhost/ansatz-trace-gateway:auth-traces-20260824-r2`; archive SHA-256 `304c813b6c8811be9b5bcd83401de261a1c401034e207bc680f61d3cdec00350`

Before recreation:

1. Back up `data/auth/db.sqlite3` and the owner-only `secrets/server.env` beneath `$DEPLOY_ROOT/backups`.
2. Update only the intended image tag without printing the environment file.
3. Validate `podman-compose ... config` with output redirected away from logs because rendered Compose output contains secrets.
4. Recreate the exact platform service and its explicit dependents. Podman Compose 1.0.6 may ignore a single-service `--force-recreate`; confirm the container's `ImageName` and image ID with `podman inspect`.
5. Run `bash scripts/configure-voice-trace-npm.sh`, then `bash scripts/check-voice-trace.sh`.

The deployment helper implements these boundaries:

```bash
bash scripts/deploy-voice-trace.sh
```

## Storage migration and rollback

The guarded migration entrypoint is:

```bash
bash scripts/migrate-hermes-storage-to-data.sh status
```

For a new migration, use phases in order: `preflight`, `stage`, `cutover`, application verification, `verify`, recreate the platform with direct `/data` volumes, `retire-legacy-binds`, then `cleanup-old-graphroot`. Cleanup refuses to run without both verification markers and clears only exact underlying root-filesystem copies through an isolated bind view.

Before cleanup, `rollback` stops only recorded containers, removes exact task-owned binds in reverse order, restores `fstab.before`, reloads systemd mount units, and restarts the recorded containers. Never run cleanup against `/`, `/data`, an unresolved variable, or a live mount target.

## Trace authorization verification

- `/traces` calls Langfuse's private `/api/public/v2/observations` API server-side.
- Every request includes `userId=request.user.get_username()`; the value comes only from the
  authenticated server-side User object. Browser identity parameters are ignored, and session and
  Trace IDs are additional filters, never replacements.
- Detail views apply a second local ownership filter and return 404 for foreign IDs.
- Project API credentials remain in the auth-service environment and must never appear in browser HTML, commands, evidence, or Git.
- A production acceptance run must use a real packaged-client conversation. Synthetic Trace tools may test transport only and must not be reported as real-conversation proof.

Historical observations created before usage canonicalization can legitimately have empty usage and cost. The Gateway also collapses a duplicated adjacent provider prefix such as `openai/openai/gpt-5.5` to Langfuse's recognized `openai/gpt-5.5` pricing alias. After a new real conversation, require complete input/output and child observations, trusted user/session metadata, and non-zero Token/Cost when the upstream model reports billable usage and Langfuse recognizes its model/pricing alias.

## Image and mount cleanup safety

Do not run global `podman system prune`, Buildah cleanup, external-container removal, or forced image removal on Hermes. The host contains unrelated `cv-php8`/single-cv and NPM workloads plus external build-storage containers. If cleanup is separately authorized, first record every regular and external container, preserve every referenced image, and restrict removal to exact, proven-unreferenced Voice Trace tags. Shared base and dangling images retained by external build containers are out of scope.

After any cleanup, prove all ten current application containers are still running, `cv-php8` still uses its original image and `/var/www` mount, Voice Trace health passes, and the Podman graphroot still resolves physically to `/data`.
