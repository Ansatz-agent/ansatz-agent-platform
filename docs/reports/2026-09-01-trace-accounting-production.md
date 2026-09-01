# Trace token and cost accounting production delivery

Date: 2026-09-01

## Outcome

The Trace accounting path now preserves provider-reported usage through streamed
NeMo Relay calls, attaches normalized usage and estimated provider cost to the
logical LLM scope, and projects that accounting onto the final physical
generation before Langfuse ingestion. The personal `/traces` UI continues to
prefer ingested values and no longer represents missing evidence as a real zero.

The Gateway change is merged and deployed on the SJTU production node. The
client change is merged on `origin/main` and becomes active on an installation
after that installation runs the normal `ansatz update`/desktop update flow.

## Reviewed source and releases

| Component | PR | Merged `origin/main` commit | Deployed artifact |
|---|---|---|---|
| Hermes client | [hermes-agent#24](https://github.com/Ansatz-agent/hermes-agent/pull/24) | `41f892309ac9bb0be5b6ec04db42d8568f08b89f` | Source update; no server image |
| Trace Gateway | [ansatz-agent-platform#11](https://github.com/Ansatz-agent/ansatz-agent-platform/pull/11) | `df12eda064e4cc0bf8310e87cc43493826eb4c1c` | `localhost/ansatz-trace-gateway:main-20260901-df12eda064` |
| Personal Trace UI | [agent-langfuse-server#10](https://github.com/Ansatz-agent/agent-langfuse-server/pull/10) | `5fae11d253a5c3b8722edbe8277c70d57b2b91e9` | `localhost/ansatz-auth-service:main-20260901-5fae11d253` |

The Gateway image ID is
`sha256:c77c636834b282209d96722297587c5635e2e956d23c984f873ee9a99562388a`.
Its statically linked binary SHA-256 is
`6e026de8ee2c73cc76337c51bc1bc9ecaaa445acef4d7b38e8ed55b4b2806086`.
The exact source archive SHA-256 is
`769654a915c0f11d289eca737cdc64f0f833b875ace666a6d0188047a4af6887`.
The known rollback image remains
`localhost/ansatz-trace-gateway:user-display-20260825`, image ID
`sha256:1070c359d4578c8f848886dbe4e128809fd68e4422dbf05008eaac31edede1f3`.

## Root cause

Providers may put usage only in the final streaming chunk. NeMo Relay's decoded
stream did not expose that usage-only chunk to the conversation accounting
path. The physical generation therefore retained model/input/output but no
usage. The logical `hermes.logical_llm_call` scope also ended without usage or
cost, so the Gateway had no trusted accounting to project and Langfuse stored
empty maps.

The client now observes raw provider chunks before Relay decoding can omit an
accounting-only chunk. It normalizes the provider usage, computes cost with the
existing route-aware pricing code, and ends the logical call with both values.
The Gateway associates a logical call with its direct physical attempts and
projects accounting only onto the latest attempt, preventing retry
double-counting. Existing physical usage remains authoritative.

## Verification

Client verification on the SJTU build node:

- complete Relay suite: `21 passed`;
- new logical accounting cases: `2 passed`;
- focused streamed accounting capture: `1 passed`;
- Ruff 0.15.10 over all five changed files: `All checks passed!`;
- full streaming file: `30 passed, 3 failed, 4 skipped`; all three failures
  imported the optional `anthropic` package, which was absent from the isolated
  test environment and is unrelated to the changed paths.

Gateway verification:

- focused projection tests passed;
- the complete Gateway Go suite passed across all seven packages;
- the production container reports `healthy`, restart count zero, and runs the
  exact reviewed image above.

Production end-to-end acceptance used the dedicated `trace-e2e-a-20260823`
account and the public authentication, native session, Trace token, and
`/trace-ingest/v1/traces` routes. Batch
`c3bf1b06-e7e0-4789-8065-72a3c3859d37` received HTTP 200 and durable receipt
`accepted`. Trace `02a903b46b3580bf1b5d2a7eac4d968d` persisted exactly two
observations. Its physical `deepseek-v4-flash` generation contains:

- usage: `input=123`, `output=45`, `total=168`;
- cost: `total=0.000321` USD;
- service version: `41f892309`.

The same owner loaded `/traces/trace/02a903b46b3580bf1b5d2a7eac4d968d/`
with HTTP 200, and the rendered page contained `168 tokens` and `$0.00032`.
The second production test account received HTTP 404 for the same Trace,
reconfirming owner isolation.

## SJTU port-mapping incident and recovery

The first Gateway-only recreation used only the base Compose file. On SJTU,
the loopback host mappings live in `docker-compose.sjtu.yml`; omitting that
override recreated a healthy private container without
`127.0.0.1:8080:8080`. Nginx consequently returned 502 for public ingestion.

The exact Gateway image was recreated with both Compose files. Post-recovery
evidence shows:

- `8080/tcp -> 127.0.0.1:8080`;
- Compose labels list both `docker-compose.yml` and
  `docker-compose.sjtu.yml`;
- Gateway health is `healthy`, restart count zero;
- all other seven Voice Trace containers remain healthy;
- the public end-to-end batch above succeeds.

Future SJTU service recreation must render and apply both files. A container's
private health check is not sufficient proof that Nginx can reach its loopback
publication.

## Historical boundary

The 17 existing `wangzihe` generation observations predate the client fix and
contain empty `usage_details` and `cost_details`. Their stored input/output is
not provider billing evidence, and the auth database has no separate usage
ledger for those calls. Exact historical Token/Cost therefore cannot be
reconstructed without the provider's original accounting or the user's local
client records. Those observations remain explicitly unavailable rather than
being assigned fabricated zeros or unlabeled estimates.

Completion for `wangzihe` requires one new conversation after the Windows
installation updates to client `main@41f892309` or later, followed by a
production read proving that the new generation contains non-empty usage and
cost. The verified test-account Trace proves the deployed server chain; it does
not claim that the external `wangzihe` installation has updated.
