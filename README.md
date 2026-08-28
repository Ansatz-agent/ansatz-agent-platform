# Ansatz Agent Platform

<p><a href="README.md"><kbd>English</kbd></a> <a href="README.zh-CN.md"><kbd>中文</kbd></a></p>

Ansatz Agent Platform is the integration and operations repository for a
client/server Agent observability system. Hermes-based clients run Agent work
locally, authenticate through a shared account service, and upload traces to a
durable gateway. Langfuse stores and visualizes those traces while the platform
keeps ordinary-user and administrator access separate.

### Architecture

```text
Hermes clients
  |-- authenticate ------------------------------> Auth service (/auth)
  |-- encrypted local trace outbox
          `--> Trace Gateway (/trace-ingest)
                   |-- durable inbox and receipts
                   `--> Langfuse
                          |-- personal dashboard (/traces)
                          `-- admin console (/langfuse)
```

The platform is designed around three boundaries:

- **Local-first execution:** temporary authentication or network failures must
  not stop an existing local conversation.
- **Durable trace delivery:** clients retain unsent traces in an encrypted
  outbox; the gateway acknowledges a batch only after durable admission and
  delivers it asynchronously with stable idempotency.
- **Scoped access:** ordinary users can see only traces owned by their stable
  account ID, while administrators use a separate Langfuse account.

### What this repository owns

| Path | Responsibility |
|---|---|
| `services/trace-gateway/` | Trace-token validation, durable inbox, idempotent receipts, and asynchronous delivery |
| `deploy/voice-trace/` | Compose and edge-routing contracts for the Voice Trace stack |
| `scripts/` | Bootstrap, deployment, migration, health-check, and support utilities |
| `tests/` | Platform, routing, deployment, and protocol contract tests |
| `docs/` | Requirements, current status, runbooks, reports, and authoritative file routing |
| `components.lock.yaml` | Reviewed revisions of independently maintained component repositories |

The Desktop client, authentication service, Hermes reference runtime, and
NeMo Relay are maintained in separate repositories. They are pinned here for
integration review rather than vendored into this repository.

### Getting started

Clone the repository and run the platform contract suite:

```bash
git clone git@github.com:Ansatz-agent/ansatz-agent-platform.git
cd ansatz-agent-platform
bash tests/run.sh
```

Run the Trace Gateway unit and integration tests separately:

```bash
cd services/trace-gateway
go test ./...
```

Deployment is environment-specific and handles authentication credentials,
Langfuse keys, persistent storage, and existing workloads. Before operating a
stack, read the relevant runbook instead of treating the example environment
file as a production recipe.

### Documentation

- [Current progress and delivery boundaries](docs/02-progress.md)
- [Authoritative file and component index](docs/03-file-index.md)
- [Storage, authentication, and trace-access requirements](docs/requirements/2026-08-24-server-storage-auth-trace-access.md)
- [Storage, authentication, and personal-trace operations](docs/runbooks/storage-auth-personal-traces.md)
- [Offline trace delivery and recovery](docs/runbooks/trace-upload-continuity.md)
- [Latest continuity verification report](docs/reports/2026-08-25-auth-trace-continuity-e2e.md)

Project status changes independently from deployment status. Check the progress
document and its linked evidence before treating a feature as released or
running in production.

### Security

Never commit `.secrets/`, runtime state, authentication caches, trace payloads,
wrapped encryption keys, service environment files, or production credentials.
Diagnostics and evidence must remain payload-free and secret-free.
