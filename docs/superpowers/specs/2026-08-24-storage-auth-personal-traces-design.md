# Storage, Unified Auth, and Personal Traces Design

**Status:** Approved by the user's standing implementation approval
**Date:** 2026-08-24
**Requirements:** `docs/requirements/2026-08-24-server-storage-auth-trace-access.md`

## Goals

1. Move Hermes platform container storage and persistent state from the full root disk to `/data` without losing accounts or traces.
2. Replace the public `/agent` portal with a focused `/auth` identity surface backed by the existing Django user database.
3. Provide a lightweight `/traces` dashboard where an authenticated user can read only their own Langfuse data.
4. Preserve `/langfuse` as the administrator-only full project dashboard.
5. Make the packaged Hermes client call `/auth` directly and continue mandatory complete Trace upload.

## Non-goals

- Replacing the existing account database with Langfuse accounts.
- Giving ordinary users Langfuse project membership or Project API Keys.
- Reproducing every Langfuse administrator feature in `/traces`.
- Adding SSO into the Langfuse administrator UI in this rollout.

Administrators continue to authenticate with an independent Langfuse administrator account at `/langfuse`; ordinary `/auth` accounts never become Langfuse project members.
- Preserving compatibility for clients that still call `/agent`.
- Changing the three Hermes launch entrypoints beyond their existing desktop Trace path.

## Requirements

- Public routes are exactly `/auth`, `/traces`, `/trace-ingest/v1/traces`, and `/langfuse`; `/agent` is retired.
- Authentication, Trace issuance, and Dashboard access use one Django session and the existing user rows.
- Authorization uses immutable `str(User.pk)` as `sub`; username is display metadata only.
- Ordinary-user reads are server-side and always scoped to `userId=sub`.
- Dashboard secrets never cross the browser boundary.
- Existing full Trace input/output, tool payload, usage, session, and entrypoint fields remain available.
- All code behavior changes follow test-first development.

## Architecture

```text
Hermes client
  ├─ HTTPS /auth/login + /auth/api/* ───────┐
  └─ HTTPS /trace-ingest/v1/traces          │
                                             ▼
                                      Django auth service
                                      existing SQLite users
                                             │ introspection
                                             ▼
                                      Trace Gateway
                                             │ canonical OTLP
                                             ▼
                                         Langfuse

Browser
  ├─ /auth/login ── shared secure session ── /traces
  │                                           │ server-side Project API
  │                                           └─ forced userId = session sub
  └─ administrator ───────────────────────── /langfuse
```

The existing Django auth container becomes the `auth-service` and owns two public surfaces. `/auth` contains login, logout, session status, and Trace token issuance. `/traces` contains server-rendered HTML and read-only JSON/detail calls. Both share a host-only secure session cookie. Legacy history/import/memory routes remain in source for data preservation but are not publicly routed.

## Interfaces and Data Flow

### Authentication routes

| Method | Route | Result |
|---|---|---|
| GET/POST | `/auth/login/` | CSRF-protected username/password login |
| POST | `/auth/logout/` | Revoke session Trace tokens and log out |
| GET | `/auth/api/session/` | `{authenticated, sub, username, role, server_time, session_expires_at, trace_dashboard_url}` |
| POST | `/auth/api/trace-token/` | Existing opaque short-lived upload credential |
| POST | `/internal/trace-token/introspect/` | Private service call returning `platform_user_id`, `platform_username`, installation, scope, audience, and expiry |

The cookies are `__Host-ansatz_sessionid` and `__Host-ansatz_csrftoken`, with `Secure`, no Domain attribute, and `Path=/`. The session cookie is HttpOnly and both cookies are SameSite=Lax.

### Trace identity

The Gateway discards client-provided canonical identity attributes. It writes:

- `user.id` and `langfuse.user.id` from `platform_user_id`;
- `langfuse.trace.metadata.username` from `platform_username`;
- session and conversation IDs from trusted Gateway headers;
- Langfuse usage details derived from NeMo Relay provider usage.

This makes Langfuse `userId` stable if a username changes while preserving a readable current username.

### Dashboard reads

`auth-service` holds `LANGFUSE_INTERNAL_BASE_URL`, `LANGFUSE_PROJECT_PUBLIC_KEY`, and `LANGFUSE_PROJECT_SECRET_KEY`. Its Langfuse client uses Basic authentication against the private container address and bounded timeouts.

- Dashboard reads `/api/public/v2/observations` with `userId=str(request.user.pk)`, bounded date ranges, and `basic,time,model,usage,metrics,trace_context` fields. It groups the returned observations by `traceId` and `sessionId` and aggregates token, cost, and model values.
- Session detail repeats the same mandatory user filter and adds the selected session ID.
- Trace detail repeats the mandatory user filter and adds the selected trace ID, then applies a second local ownership filter before rendering. A result set with no owned observation returns 404.
- Input/output and metadata fields are requested only for detail views, keeping the summary response bounded.

The UI supports fixed 7/30/90-day ranges and a bounded session search. It groups traces by `sessionId`, renders current-period summary cards, a daily usage trend, model mix, session table, session timeline, and escaped JSON/text detail. No delete, project settings, API-key, user-management, or mutation controls are exposed.

## Storage Layout and Migration

The target layout is:

```text
/data/containers/storage/
/data/ansatz-agent/voice-trace/
  deploy/
  secrets/
  backups/
  evidence/
  data/
    auth/
    postgres/
    clickhouse/
    clickhouse-logs/
    redis/
    minio/
```

Migration preflights that `/data` is an independent mounted filesystem with enough free capacity. It stages a metadata-preserving copy, records running containers and database counts, gracefully stops containers, and performs a final update copy. Hermes runs Podman 3.3.1, whose database retains the logical static-directory path, so the logical graphroot remains `/var/lib/containers/storage` while an `/etc/fstab` bind mount makes its physical source `/data/containers/storage`. A private overlay self-bind preserves Podman's required mount-propagation behavior, and CNI state is bound from `/data/containers/cni`. The platform stack itself uses direct `/data` volume sources. Obsolete root-filesystem copies are cleared only through an isolated root-filesystem view after mounts, containers, databases, and HTTPS pass verification.

Rollback before underlying-copy cleanup restores the saved `fstab`, removes only the exact task-owned bind targets in reverse order, and starts the recorded old containers. After cleanup, rollback uses the preserved images and application data in `/data`; application releases remain independently reversible through pinned image tags.

## Security and Privacy

- Passwords remain Django password hashes; no credential plaintext is added to source, logs, or deployment commands.
- Browser-provided `userId`, role, username, or Langfuse filter fields never determine authorization.
- Project API Keys stay in owner-only environment files and service environment variables.
- Langfuse error payloads are not reflected verbatim to users; Dashboard failures render a generic unavailable state and are logged server-side without secrets.
- Trace input/output and tool payloads are intentionally complete per product requirements and visible only to their owner or administrators.
- `/agent`, `/auth/internal`, and `/healthz` are not exposed as private-service escape paths.

## Failure Behavior

- Invalid or expired sessions redirect HTML requests to `/auth/login/` and return 401 for JSON APIs.
- Langfuse timeouts show a retryable Dashboard unavailable message; they do not fall back to unscoped queries.
- A trace or session that does not belong to the current user returns 404, preventing existence disclosure.
- If storage copy, physical graphroot validation, container recreation, or health checks fail before commit, the migration restores the original mounts and containers.
- `/agent` returns 404 after cutover; there is no redirect that could hide an outdated client.

## Testing

- Django tests cover `/auth` routes, Cookie contract, session identity/role, forced-user list filters, detail ownership checks, aggregation, pagination bounds, escaped rendering, and Langfuse failure behavior.
- Gateway Go tests cover trusted username projection, hostile metadata removal, and usage projection.
- Client tests cover exact `/auth` paths, host-only Cookie contract, redirects, login, status, logout, and Trace token issuance.
- Platform contract tests cover `/data` mounts, private Langfuse credentials, `/auth` and `/traces` proxy routes, `/agent` retirement, and migration guardrails.
- Production verification uses two ordinary users plus the administrator, a real client conversation, Langfuse API evidence, public HTTPS checks, and filesystem/graphroot checks.

## Acceptance Criteria

1. `podman info` retains the compatible logical graphroot `/var/lib/containers/storage`, `findmnt` proves its physical filesystem root is `/containers/storage` on the `/data` device, the root disk has safe free capacity, and service writes land under `/data`.
2. Account and Langfuse data counts survive migration and all current platform containers are healthy.
3. `/agent` returns 404 and `/auth/login/` works with an existing account.
4. The updated Hermes client logs in through `/auth`, sends a real complete Trace, and that Trace has the authenticated immutable user ID, username metadata, Token, and Cost.
5. User A can list/open User A data and receives 404 for User B session/Trace identifiers; User B has the symmetric isolation.
6. `/traces` presents the approved lightweight information architecture.
7. The administrator can still log into `/langfuse` and see all received Trace data.

## Rollout

1. Run test-first implementation in isolated worktrees.
2. Build auth and Gateway images on `l40s`, transfer pinned archives to Hermes, and verify hashes.
3. Perform the `/data` migration and current-stack recovery first.
4. Deploy new images and Compose/proxy configuration in one bounded cutover.
5. Verify users, real Trace upload, isolation, administrator access, and disk state.
6. Update runbooks and retain only named rollback artifacts; do not publish secrets.

## Unresolved Decisions

None for this rollout. Langfuse administrator SSO remains a separately scoped future enhancement.
