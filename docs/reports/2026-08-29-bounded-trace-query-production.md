# Bounded Trace query production delivery

Date: 2026-08-29

## Outcome

The personal Trace Explorer no longer bulk-loads observation input/output for
session and Trace collections. It fetches IO only for the selected observation,
using user, Trace, and observation identity together. ClickHouse now enforces a
1 GiB memory ceiling, 64 MiB result ceiling, 35-second execution ceiling, and
two-thread ceiling in the default profile, with matching maximum constraints.

Both changes are merged and deployed on Hermes. Public and private Voice Trace
health checks pass, the production largest-session regression returns HTTP 200,
and no new kernel OOM event was present after deployment.

## Reviewed source and PRs

| Component | PR | Merged `origin/main` commit | Reviewed change commit |
|---|---|---|---|
| Auth service | [agent-langfuse-server#9](https://github.com/Ansatz-agent/agent-langfuse-server/pull/9) | `44cb69235f834a7d2d43607f0afb102388770c0b` | `b3c20e9e4bf46d5eb9e8491a71a0df2bfc5c020d` |
| Platform / ClickHouse | [ansatz-agent-platform#9](https://github.com/Ansatz-agent/ansatz-agent-platform/pull/9) | `1b7adab99ae10739239b66aeeeeb8198d0360341` | `d9006ceb3d0a51756aecd36642f138a7c82fc21a` |

Both canonical worktrees were clean, fast-forwarded to `origin/main`, and
verified equal to the remote ref before production artifacts were built or
deployed. GitHub reported no CI jobs for either PR. The independent Claude Code
review reached its five-minute tool timeout while inspecting and attempting to
rerun tests; it reported no defect before timeout. Local and image-level test
evidence below remained the release gate.

## Test and artifact evidence

- TDD red evidence covered bulk IO, session previews, and missing detail lookup.
- Focused Auth tests: 24 passed.
- Local full History suite: 266 passed; Django system check and `compileall`
  passed.
- Platform contract suite: 29 passed, including the secret-bootstrap contract.
- The final Auth image itself ran 267 History tests: all passed in 201.314 s,
  with no Django system-check issue.
- Build host: `sys-521ge-node1.nvidia.com`, under the task-owned
  `/mnt/workspace/l40s/yuxiao/ansatz-auth-44cb69235f` directory.
- Exact Git archive SHA-256:
  `499c27fa9d3f78389b621ba921d2b2c8df154360c1e0afd986302d9fef541da9`.
- Auth image: `localhost/ansatz-auth-service:main-20260829-44cb69235f`.
- Auth image ID:
  `b9a322969ae1bd06ee96431d6f2e91af5e2b3237f052e8684c9592bc434b0201`.
- Auth archive SHA-256, identical on L40S, local staging, and Hermes:
  `076bbf4e8e431757ebbc8a223119f64efe2188d8d36cd333ef5807561be4eed8`.
- Deployed profiler SHA-256:
  `26e7e93d4eb901d06259a9d79e9a1bb894c37382044a172217701e5d1abc4bae`.
- Ruff was not installed in the mandated local `dl` Conda environment
  (`ModuleNotFoundError: No module named 'ruff'`); no environment package was
  installed or changed. The repository tests, Django check, `compileall`, and
  `git diff --check` supplied the release evidence.

## Deployment and recovery evidence

The ClickHouse profiler was parsed first with the exact deployed ClickHouse
image. The old profiler and container metadata were backed up before the exact
ClickHouse container was restarted. Business rows remained `1411 -> 1411`, and
the complete Voice Trace health check passed immediately afterward.

The first Auth recreation attempt demonstrated that Podman Compose 1.0.6 did
not replace the container reliably; automatic rollback restored the prior
environment and healthy old image. The second attempt hit the explicit
Trace-Gateway dependency and also preserved/restarted the old Auth container.
The final cutover followed dependency order: remove/recreate Trace Gateway,
then Auth, then Trace Gateway. It deployed the new image and passed internal
health, Django system check, NPM syntax checks, and the complete public/private
health script.

Podman Compose restarted several existing Voice Trace containers during the
dependency cutover. Final Auth, Trace Gateway, Langfuse Web, Langfuse Worker,
and ClickHouse containers were running with restart count zero. `cv-php8`
remained in its pre-existing `Created` state and was not mutated. No kernel OOM,
out-of-memory, or killed-process event appeared after the change window.

Rollback assets remain on Hermes:

- previous Auth image: `localhost/ansatz-auth-service:main-20260827-01c73ca1ad`;
- Auth environment, database, and container evidence:
  `/data/ansatz-agent/voice-trace/backups/auth-main-20260829-44cb69235f`;
- previous ClickHouse profiler and container evidence:
  `/data/ansatz-agent/voice-trace/backups/bounded-trace-query-1b7adab99a`;
- effective Auth and ClickHouse evidence:
  `/data/ansatz-agent/voice-trace/evidence/auth-main-20260829-44cb69235f` and
  `/data/ansatz-agent/voice-trace/evidence/bounded-trace-query-1b7adab99a`.

## Production acceptance

The largest production session contains 843 observations and 570,830,633
bytes of event data. A read-only owner-scoped RequestFactory acceptance run in
the deployed Auth container produced:

| Route case | HTTP | Rendered bytes | Time |
|---|---:|---:|---:|
| Largest session, metadata-only collection | 200 | 2,787 | 0.462 s |
| Largest Trace overview | 200 | 1,248,452 | 0.744 s |
| 4.3 MiB selected observation | 200 | 4,554,471 | 0.864 s |
| 1.1 MiB selected observation | 200 | 1,250,333 | 0.718 s |

None returned the local payload-limit message or global unavailable response.
The production ClickHouse defaults and maximum constraints were re-read after
all container restarts and remained exactly 1 GiB, 64 MiB, 35 seconds, and two
threads. The public login page, anonymous Trace redirect, Langfuse health, Auth
health, Trace Gateway health, and private Langfuse health all passed.

## Remaining boundary

This delivery fixes the personal Trace query/OOM path and adds ClickHouse
guardrails. It does not change Langfuse source code, the Trace Gateway protocol,
the closed-source client update mechanism, or the separately tracked
authentication-continuity/offline-outbox release.
