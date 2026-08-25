# Authentication Continuity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep a previously authenticated Hermes Desktop user locally authorized through authentication-service outages and Web Session expiry, while adding immutable account identity, explicitly revocable native client Sessions, structured revocation, asynchronous Trace credentials, offline restart, and data-preserving sign-out.

**Architecture:** Django issues a persistent opaque native Session bound to an immutable account UUID and installation UUID; only token digests are stored server-side. The Python auth owner restores this credential from the OS credential store before networking and separates durable local authorization from online validation health. Electron treats only sign-out or matching structured account/current-Session revocation as terminal, starts local Trace capture without waiting for a cloud credential, and keeps the renderer/backend mounted while validation is degraded.

**Tech Stack:** Django 5.2, Python 3.10+, httpx, keyring, pytest, Electron 40, TypeScript 6, Vitest 4, React 19, Playwright 1.58.

**Spec:** `docs/superpowers/specs/2026-08-24-auth-trace-continuity-design.md`

## Global Constraints

- Server starts only from `agent-langfuse-server/main@31a22bf1bf49d6006f140fa2f726e6759845c1e7` on `feature/auth-continuity-protocol` in its dedicated worktree.
- Client starts only from `agent-hermes-client/main@80bc34f5f18d2d58d1866b3140f8a1c6bc953928` on `feature/auth-continuity` in its dedicated worktree.
- Use `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity` for the server worktree and `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity` for the client worktree.
- Never inspect, copy, cherry-pick, diff against, or otherwise use content from `fix/relay-token-cost`; its staged changes are unrelated user work.
- This plan changes `agent-hermes-client` and `agent-langfuse-server`; Gateway durable inbox work remains in the Trace continuity plan.
- Use Conda environment `dl` for Python. Use `rtk proxy python` only when it already resolves inside `dl`; otherwise use `rtk proxy conda run -n dl python`.
- Do not create a virtual environment or modify packages without explicit approval.
- Every behavior change follows one-test RED, observed expected failure, minimum GREEN, then refactor while green.
- Native Session and Trace tokens never appear in logs, exceptions, fixtures, snapshots, renderer data, or database plaintext.
- Local capability is terminal only after user sign-out, `account_disabled`, `account_revoked`, or `session_revoked` for the cached account/current Session.
- Timeout, DNS/TLS/VPN/proxy/network failure, 429, 5xx, malformed response, bridge failure, Web Session expiry, and `invalid_session_credential` preserve scope, backend, windows, and conversation tree.
- Sign-out and structured revocation preserve SessionDB, attachments, projects, profiles, and local conversations.
- Existing `/auth/api/session/`, Web login/logout, and legacy Trace-token routes remain during compatibility rollout.
- Client Stream A owns authentication state/lifecycle. Stream B owns the durable Trace outbox. Their only intentional overlap is the Trace-listener startup seam in `apps/desktop/electron/main.ts`, resolved on `feature/auth-trace-continuity`.
- Client work obeys `agent-hermes-client/AGENTS.md`; Electron/renderer work also obeys `agent-hermes-client/apps/desktop/AGENTS.md`. The Django `auth-service/` tree has no deeper scoped AGENTS file, so the outer workspace rules apply there.

## Fixed Cross-Repository Contract

```text
POST   /auth/api/client-session/
GET    /auth/api/client-session/
DELETE /auth/api/client-session/current/
POST   /auth/api/client-session/trace-token/

Authorization: Bearer SESSION_TOKEN_VALUE
X-Ansatz-Installation-ID: 11111111-1111-4111-8111-111111111111
Cache-Control: no-store
```

Issue body keys are `installation_id, client_version`. A 201 response contains `account_id, session_id, session_token, installation_id, username, issued_at`. Active GET is HTTP 200 with `state=active`, identity, username, and `server_time`. Explicit revocation is HTTP 403 with `state=revoked`, `code=account_disabled|account_revoked|session_revoked`, matching identity, `revoked_at`, and `retryable=false`. Unknown/malformed credential is HTTP 401 with `code=invalid_session_credential,retryable=true` and is never local revocation. Stored `signed_out` is externally reported as `session_revoked` because the initiating client has already cleared local credentials.

---

### Task 1: Server AccountIdentity and ClientSession Models (S1)

**Files:**
- Modify: `agent-langfuse-server/auth-service/history/models.py`
- Create: `agent-langfuse-server/auth-service/history/migrations/0006_account_identity_client_session.py`
- Modify: `agent-langfuse-server/auth-service/history/tests/test_migrations.py`
- Create: `agent-langfuse-server/auth-service/history/tests/test_client_session_models.py`

**Interfaces:**
- Consumes: Django `settings.AUTH_USER_MODEL`, migration `history.0005_trace_upload_token`.
- Produces: `AccountIdentity`, `ClientSession`, `AccountIdentity.State`, `ClientSession.RevocationReason` for Tasks 2–5.
- Produces invariants: one immutable UUID per User, unique Session UUID/digest, installation binding, retained revocation evidence, protected deletion.

- [ ] **Step 1: Write the failing backfill test**

```python
class AccountIdentityMigrationTests(TransactionTestCase):
    migrate_from = [("history", "0005_trace_upload_token")]
    migrate_to = [("history", "0006_account_identity_client_session")]

    def test_existing_users_receive_distinct_uuid4_account_ids(self):
        executor = MigrationExecutor(connection)
        executor.migrate(self.migrate_from)
        old_apps = executor.loader.project_state(self.migrate_from).apps
        User = old_apps.get_model("auth", "User")
        first = User.objects.create(username="first-existing")
        second = User.objects.create(username="second-existing")
        executor = MigrationExecutor(connection)
        executor.migrate(self.migrate_to)
        apps = executor.loader.project_state(self.migrate_to).apps
        Identity = apps.get_model("history", "AccountIdentity")
        rows = list(Identity.objects.order_by("user_id").values_list("user_id", "account_id", "state"))
        self.assertEqual([row[0] for row in rows], [first.pk, second.pk])
        self.assertEqual(len({row[1] for row in rows}), 2)
        self.assertTrue(all(row[1].version == 4 for row in rows))
        self.assertEqual([row[2] for row in rows], ["active", "active"])
```

- [ ] **Step 2: Run RED**

From server worktree `auth-service/`:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_migrations.AccountIdentityMigrationTests
```

Expected: FAIL because migration `0006_account_identity_client_session` does not exist.

- [ ] **Step 3: Implement minimum models/migration**

```python
class AccountIdentity(models.Model):
    class State(models.TextChoices):
        ACTIVE = "active", "Active"
        REVOKED = "revoked", "Revoked"
    account_id = models.UUIDField(default=uuid.uuid4, unique=True, editable=False)
    user = models.OneToOneField(settings.AUTH_USER_MODEL, on_delete=models.PROTECT, related_name="account_identity")
    state = models.CharField(max_length=16, choices=State.choices, default=State.ACTIVE)
    revoked_at = models.DateTimeField(null=True, blank=True)
    revocation_reason = models.CharField(max_length=32, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)

class ClientSession(models.Model):
    class RevocationReason(models.TextChoices):
        SIGNED_OUT = "signed_out", "Signed out"
        SESSION_REVOKED = "session_revoked", "Session revoked"
        ACCOUNT_DISABLED = "account_disabled", "Account disabled"
        ACCOUNT_REVOKED = "account_revoked", "Account revoked"
    session_id = models.UUIDField(default=uuid.uuid4, unique=True, editable=False)
    account = models.ForeignKey(AccountIdentity, on_delete=models.PROTECT, related_name="client_sessions")
    installation_id = models.UUIDField()
    credential_digest = models.CharField(max_length=64, unique=True)
    client_version = models.CharField(max_length=64)
    created_at = models.DateTimeField()
    last_seen_at = models.DateTimeField()
    revoked_at = models.DateTimeField(null=True, blank=True)
    revocation_reason = models.CharField(max_length=32, choices=RevocationReason.choices, blank=True)
```

Migration `0006` creates both tables then runs a forward function which iterates `User.objects.order_by("pk").iterator()` and creates `account_id=uuid.uuid4(),state="active"`; reverse is `migrations.RunPython.noop`.

- [ ] **Step 4: Run GREEN**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_migrations.AccountIdentityMigrationTests
```

Expected: PASS with two distinct UUIDv4 identities.

- [ ] **Step 5: Add immutability/protected-deletion cycle**

Add tests that change persisted `account_id/session_id` and expect `ValidationError`, and delete User/AccountIdentity with retained rows and expect `ProtectedError`. Implement `save()` checks against persisted UUID values before update. Run:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_client_session_models
```

Expected before implementation: FAIL because UUID mutation succeeds. Expected after implementation: PASS; identity mutation and destructive deletion are rejected.

- [ ] **Step 6: Commit**

```bash
rtk git add auth-service/history/models.py auth-service/history/migrations/0006_account_identity_client_session.py auth-service/history/tests/test_migrations.py auth-service/history/tests/test_client_session_models.py
rtk git commit -m "feat(auth): add immutable accounts and client sessions"
```

### Task 2: Server Native Session Domain Service (S2)

**Files:**
- Create: `agent-langfuse-server/auth-service/history/client_sessions.py`
- Create: `agent-langfuse-server/auth-service/history/tests/test_client_sessions.py`

**Interfaces:**
- Consumes: Task 1 models, transactions, `timezone.now()`, `secrets.token_urlsafe(32)`.
- Produces: `IssuedClientSession`, `ClientSessionResolution`, `account_identity_for_user()`, `issue_client_session()`, `resolve_client_session()`, `revoke_client_session()`, `revoke_account_sessions()`.

- [ ] **Step 1: Write the failing lifecycle test**

```python
def test_issue_resolve_revoke_preserves_digest_only_evidence(self):
    issued = issue_client_session(user=self.user, installation_id=INSTALLATION_ID, client_version="0.17.0")
    self.assertEqual(issued.record.credential_digest, hashlib.sha256(issued.access_token.encode()).hexdigest())
    self.assertNotEqual(issued.record.credential_digest, issued.access_token)
    active = resolve_client_session(token=issued.access_token, installation_id=INSTALLATION_ID)
    self.assertEqual((active.record, active.code, active.explicit_revocation), (issued.record, None, False))
    revoke_client_session(session=issued.record, reason="session_revoked")
    revoked = resolve_client_session(token=issued.access_token, installation_id=INSTALLATION_ID)
    self.assertEqual((revoked.record, revoked.code, revoked.explicit_revocation), (issued.record, "session_revoked", True))
```

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_client_sessions
```

Expected: FAIL with missing `history.client_sessions`.

- [ ] **Step 3: Implement minimum domain service**

```python
@dataclass(frozen=True)
class IssuedClientSession:
    access_token: str
    record: ClientSession

@dataclass(frozen=True)
class ClientSessionResolution:
    record: ClientSession | None
    code: str | None
    explicit_revocation: bool

def credential_digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()
```

`issue_client_session()` transactionally gets/creates AccountIdentity, creates one random token, stores only digest, and returns plaintext once. `resolve_client_session()` validates token length `32..128`, installation match, inactive User → `account_disabled`, revoked identity → `account_revoked`, revoked Session → stored reason with `signed_out` mapped to `session_revoked`; unknown/mismatch → `invalid_session_credential,False`. Active resolution updates `last_seen_at`. Revocation updates rows without deletion.

- [ ] **Step 4: Run GREEN**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_client_sessions
```

Expected: PASS for issue/digest, installation mismatch, inactive User, account revoke, sign-out mapping, second-Session isolation, and no token in captured logs.

- [ ] **Step 5: Commit**

```bash
rtk git add auth-service/history/client_sessions.py auth-service/history/tests/test_client_sessions.py
rtk git commit -m "feat(auth): implement persistent client session lifecycle"
```

### Task 3: Server Native Session HTTP API (S3)

**Files:**
- Modify: `agent-langfuse-server/auth-service/history/auth_views.py`
- Modify: `agent-langfuse-server/auth-service/config/urls.py`
- Create: `agent-langfuse-server/auth-service/history/tests/test_native_client_session_api.py`
- Modify: `agent-langfuse-server/auth-service/history/tests/test_auth_surface.py`
- Preserve: `agent-langfuse-server/auth-service/history/tests/test_client_session_api.py`

**Interfaces:**
- Consumes: Task 2 service and existing strict JSON/UUID response helpers.
- Produces named routes `native-client-session`, `native-client-session-current`, and `_native_session_resolution(request)`.
- Produces the fixed POST/GET/DELETE wire contract consumed by Task 7.

- [ ] **Step 1: Write failing route lifecycle test**

```python
def test_web_bootstrap_status_and_explicit_revoke_have_exact_shapes(self):
    csrf = self.authenticate_web_session()
    issued = self.client.post(reverse("native-client-session"), data=json.dumps({
        "installation_id": str(INSTALLATION_ID), "client_version": "0.17.0"
    }), content_type="application/json", HTTP_X_CSRFTOKEN=csrf)
    self.assertEqual(issued.status_code, 201)
    self.assertEqual(set(issued.json()), {"account_id", "session_id", "session_token", "installation_id", "username", "issued_at"})
    body = issued.json()
    headers = {"HTTP_AUTHORIZATION": f"Bearer {body['session_token']}", "HTTP_X_ANSATZ_INSTALLATION_ID": str(INSTALLATION_ID)}
    active = self.client.get(reverse("native-client-session"), **headers)
    self.assertEqual((active.status_code, active.json()["state"]), (200, "active"))
    ClientSession.objects.filter(session_id=body["session_id"]).update(revoked_at=timezone.now(), revocation_reason="session_revoked")
    revoked = self.client.get(reverse("native-client-session"), **headers)
    self.assertEqual((revoked.status_code, revoked.json()["code"], revoked.json()["retryable"]), (403, "session_revoked", False))
```

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_client_session_api
```

Expected: FAIL with `NoReverseMatch` for `native-client-session`.

- [ ] **Step 3: Implement strict routes**

```python
path("auth/api/client-session/", native_client_session, name="native-client-session"),
path("auth/api/client-session/current/", native_client_session_current, name="native-client-session-current"),
```

Use a method-dispatch view. POST invokes a `@csrf_protect` issue helper and requires authenticated, unexpired Web Session plus exact body keys. GET requires one Bearer token and lowercase UUIDv4 installation header. Return 200 active, 403 only for explicit reasons, or 401 `{"state":"unavailable","code":"invalid_session_credential","retryable":true}`. DELETE persists `signed_out` then returns 204. Every response is `no-store`.

- [ ] **Step 4: Run GREEN plus legacy compatibility**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_client_session_api history.tests.test_client_session_api history.tests.test_auth_surface
```

Expected: PASS; new shapes are exact and `/auth/api/session/` stays compatible.

- [ ] **Step 5: Commit**

```bash
rtk git add auth-service/history/auth_views.py auth-service/config/urls.py auth-service/history/tests/test_native_client_session_api.py auth-service/history/tests/test_auth_surface.py
rtk git commit -m "feat(auth): expose native client session API"
```

### Task 4: Server Administrative Revocation (S4)

**Files:**
- Modify: `agent-langfuse-server/auth-service/history/admin.py`
- Modify: `agent-langfuse-server/auth-service/history/tests/test_admin_auth.py`

**Interfaces:**
- Consumes: Task 2 revoke functions and Task 1 models.
- Produces `AccountIdentityAdmin`, `ClientSessionAdmin`, `HermesUserAdmin`; actions `revoke_accounts`, `revoke_sessions`, `disable_accounts`.

- [ ] **Step 1: Write failing action test**

```python
def test_admin_session_revoke_is_isolated_and_disable_reaches_remaining_sessions(self):
    first, second = self.two_native_sessions(self.user)
    self.client.force_login(self.admin)
    response = self.client.post(reverse("admin:history_clientsession_changelist"), {"action": "revoke_sessions", "_selected_action": [first.pk]}, follow=True)
    self.assertEqual(response.status_code, 200)
    first.refresh_from_db(); second.refresh_from_db()
    self.assertEqual(first.revocation_reason, "session_revoked")
    self.assertIsNone(second.revoked_at)
    response = self.client.post(reverse("admin:auth_user_changelist"), {"action": "disable_accounts", "_selected_action": [self.user.pk]}, follow=True)
    self.assertEqual(response.status_code, 200)
    second.refresh_from_db()
    self.assertEqual(second.revocation_reason, "account_disabled")
```

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_admin_auth.AccountAdministrationTests.test_admin_session_revoke_is_isolated_and_disable_reaches_remaining_sessions
```

Expected: FAIL because ClientSession registration/actions are absent.

- [ ] **Step 3: Implement actions/read-only admins**

```python
@admin.action(description="Revoke selected native Sessions")
def revoke_sessions(modeladmin, request, queryset):
    for session in queryset.select_related("account"):
        revoke_client_session(session=session, reason=ClientSession.RevocationReason.SESSION_REVOKED)

@admin.action(description="Disable selected accounts")
def disable_accounts(modeladmin, request, queryset):
    for user in queryset:
        user.is_active = False
        user.save(update_fields=["is_active"])
        revoke_account_sessions(account=account_identity_for_user(user), reason=ClientSession.RevocationReason.ACCOUNT_DISABLED)
```

Register identity/session with immutable `readonly_fields`, no add/delete. Replace stock UserAdmin with `HermesUserAdmin`; `revoke_accounts` sets identity revoked and revokes all active Sessions as `account_revoked`. Re-enabling a User never revives an old Session.

- [ ] **Step 4: Run GREEN**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_admin_auth
```

Expected: PASS; authorization remains superuser-only, selected revoke is isolated, disable/revoke reaches all active Sessions, rows remain.

- [ ] **Step 5: Commit**

```bash
rtk git add auth-service/history/admin.py auth-service/history/tests/test_admin_auth.py
rtk git commit -m "feat(auth): add structured administrative revocation"
```

### Task 5: Native Trace Token and Structured Introspection (S5)

**Files:**
- Modify: `agent-langfuse-server/auth-service/history/models.py`
- Create: `agent-langfuse-server/auth-service/history/migrations/0007_trace_token_client_session.py`
- Modify: `agent-langfuse-server/auth-service/history/trace_tokens.py`
- Modify: `agent-langfuse-server/auth-service/history/client_sessions.py`
- Modify: `agent-langfuse-server/auth-service/history/auth_views.py`
- Modify: `agent-langfuse-server/auth-service/config/urls.py`
- Modify: `agent-langfuse-server/auth-service/history/tests/test_trace_tokens.py`
- Create: `agent-langfuse-server/auth-service/history/tests/test_native_trace_tokens.py`
- Modify: `agent-langfuse-server/auth-service/history/tests/test_client_sessions.py`

**Interfaces:**
- Consumes: active Task 2 ClientSession resolution.
- Produces nullable `TraceUploadToken.client_session`, `TraceUploadToken.revocation_reason`, `TraceTokenIntrospection`, route `native-trace-token`.
- Produces a transactionally shared revocation hook: every Session sign-out/revoke and every account-wide revoke also revokes all bound Trace tokens before returning.
- Active introspection adds `account_id,session_id,installation_id`; retains `platform_user_id,platform_username` during migration.

- [ ] **Step 1: Write failing classification test**

```python
def test_native_trace_introspection_separates_refresh_from_explicit_revoke(self):
    session = self.issue_native_session()
    token = self.issue_native_trace(session).json()["access_token"]
    active = self.introspect(token).json()
    self.assertEqual((active["active"], active["account_id"], active["session_id"]), (True, session["account_id"], session["session_id"]))
    record = TraceUploadToken.objects.get()
    record.expires_at = timezone.now(); record.save(update_fields=["expires_at"])
    self.assertEqual(self.introspect(token).json(), {"active": False, "reason": "token_expired", "explicit_revocation": False})
    record.expires_at = timezone.now() + timedelta(minutes=15); record.save(update_fields=["expires_at"])
    record.client_session.revoked_at = timezone.now(); record.client_session.revocation_reason = "session_revoked"
    record.client_session.save(update_fields=["revoked_at", "revocation_reason"])
    self.assertEqual(self.introspect(token).json(), {"active": False, "reason": "session_revoked", "explicit_revocation": True})
```

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_trace_tokens
```

Expected: FAIL because native route, client_session field, and structured result are absent.

- [ ] **Step 3: Implement native-bound issuance/classification**

Migration `0007` adds nullable protected `client_session` and `revocation_reason` choices `rotated|revoked`; legacy user/session digest fields remain. Define:

```python
@dataclass(frozen=True)
class TraceTokenIntrospection:
    record: TraceUploadToken | None
    reason: str
    explicit_revocation: bool
```

`issue_trace_token(*, client_session: ClientSession | None = None, user=None, session_key: str | None = None, installation_id: UUID | None = None) -> IssuedTraceToken` accepts exactly one authority form: `client_session`, or legacy `user+session_key+installation_id`. Classify malformed/unknown `invalid_token`; explicit Session/account state; expiry; rotated/revoked; wrong scope/audience `invalid_token`; otherwise active. Register bearer-only `POST /auth/api/client-session/trace-token/` named `native-trace-token`; preserve the legacy CSRF route.

Extend the Task 2 revocation service rather than duplicating revocation in views/admin: `revoke_client_session()` revokes every active bound Trace token in the same transaction, and `revoke_account_sessions()` delegates through the same primitive for every Session. Add tests for user sign-out, selected-Session admin revoke, and account-wide revoke; each must leave retained Trace-token rows with `revoked_at` and `revocation_reason="revoked"`.

- [ ] **Step 4: Run GREEN plus compatibility**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_trace_tokens history.tests.test_trace_tokens
```

Expected: PASS; native reasons are exact and legacy issuance remains available.

- [ ] **Step 5: Commit**

```bash
rtk git add auth-service/history/models.py auth-service/history/migrations/0007_trace_token_client_session.py auth-service/history/trace_tokens.py auth-service/history/client_sessions.py auth-service/history/auth_views.py auth-service/config/urls.py auth-service/history/tests/test_trace_tokens.py auth-service/history/tests/test_native_trace_tokens.py auth-service/history/tests/test_client_sessions.py
rtk git commit -m "feat(auth): bind trace credentials to native sessions"
```

### Task 6: Versioned Cross-Repository Contract Fixture (X1)

**Files:**
- Create: `agent-langfuse-server/auth-service/contracts/native-client-session-v1.json`
- Create: `agent-langfuse-server/auth-service/history/tests/test_native_client_session_contract.py`
- Create: `agent-hermes-client/docs/contracts/native-client-session-v1.json`
- Create: `agent-hermes-client/tests/hermes_cli/client_auth/test_contract_fixture.py`

**Interfaces:**
- Consumes: Tasks 3/5 final route and reason vocabulary.
- Produces byte-identical fixture copies consumed by both repositories and integration comparison.

- [ ] **Step 1: Write failing fixture consumers**

```python
def test_contract_fixture_matches_routes_and_reason_vocabulary(self):
    contract = json.loads((Path(__file__).parents[2] / "contracts/native-client-session-v1.json").read_text())
    self.assertEqual(contract["version"], 1)
    self.assertEqual(contract["routes"]["session"], reverse("native-client-session"))
    self.assertEqual(contract["routes"]["current"], reverse("native-client-session-current"))
    self.assertEqual(contract["routes"]["trace_token"], reverse("native-trace-token"))
    self.assertEqual(contract["explicit_revocations"], ["account_disabled", "account_revoked", "session_revoked"])
```

Client test loads its repository copy and checks those literals plus `transient_codes == ["invalid_session_credential"]`.

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_client_session_contract
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_contract_fixture.py -q
```

Expected: both FAIL with `FileNotFoundError`.

- [ ] **Step 3: Add identical JSON fixtures**

```json
{
  "version": 1,
  "routes": {"session": "/auth/api/client-session/", "current": "/auth/api/client-session/current/", "trace_token": "/auth/api/client-session/trace-token/"},
  "headers": {"authorization": "Authorization", "installation_id": "X-Ansatz-Installation-ID"},
  "explicit_revocations": ["account_disabled", "account_revoked", "session_revoked"],
  "transient_codes": ["invalid_session_credential"],
  "issue_request_keys": ["client_version", "installation_id"],
  "issue_response_keys": ["account_id", "installation_id", "issued_at", "session_id", "session_token", "username"],
  "active_status_keys": ["account_id", "installation_id", "server_time", "session_id", "state", "username"]
}
```

- [ ] **Step 4: Run GREEN and byte comparison**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_client_session_contract
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_contract_fixture.py -q
rtk cmp /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity/auth-service/contracts/native-client-session-v1.json /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity/docs/contracts/native-client-session-v1.json
```

Expected: tests PASS; `cmp` exits 0 with no output.

- [ ] **Step 5: Commit each repository copy**

```bash
rtk git add auth-service/contracts/native-client-session-v1.json auth-service/history/tests/test_native_client_session_contract.py
rtk git commit -m "test(auth): publish native session contract fixture"
```

```bash
rtk git add docs/contracts/native-client-session-v1.json tests/hermes_cli/client_auth/test_contract_fixture.py
rtk git commit -m "test(auth): pin native session contract fixture"
```

### Task 7: Python Native Session HTTP Client (C1)

**Files:**
- Modify: `agent-hermes-client/hermes_cli/client_auth/client.py`
- Modify: `agent-hermes-client/tests/hermes_cli/client_auth/test_client.py`

**Interfaces:**
- Consumes: Task 6 route/header/reason contract.
- Produces `NativeSessionCredential`, `NativeSessionStatus`, `ExplicitSessionRevocation`, `AuthClient.issue_client_session()`, `client_session_status()`, `logout_client_session()`, native `trace_token()`.
- Keeps cookie `status/logout/legacy_trace_token` only for migration compatibility.

- [ ] **Step 1: Write failing transient/terminal parser test**

```python
@pytest.mark.parametrize(("response", "terminal"), [
    (httpx.Response(401, json={"state": "unavailable", "code": "invalid_session_credential", "retryable": True}), False),
    (httpx.Response(429, json={"detail": "rate_limited"}), False),
    (httpx.Response(503, json={"detail": "unavailable"}), False),
    (httpx.Response(200, text="bad", headers={"Content-Type": "application/json"}), False),
    (httpx.Response(403, json={"state": "revoked", "code": "session_revoked", "account_id": ACCOUNT_ID, "session_id": SESSION_ID, "revoked_at": "2026-08-24T12:00:00+00:00", "retryable": False}), True),
])
def test_native_status_only_accepts_matching_structured_revocation(response, terminal):
    client = AuthClient(transport=SingleResponseTransport(response))
    if terminal:
        with pytest.raises(ExplicitSessionRevocation) as caught:
            client.client_session_status(native_credential())
        assert (caught.value.account_id, caught.value.session_id, caught.value.reason) == (ACCOUNT_ID, SESSION_ID, "session_revoked")
    else:
        with pytest.raises(AuthServiceError) as caught:
            client.client_session_status(native_credential())
        assert not isinstance(caught.value, ExplicitSessionRevocation)
```

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_client.py -k test_native_status_only_accepts_matching_structured_revocation -q
```

Expected: FAIL because native types/methods are absent.

- [ ] **Step 3: Implement strict native client**

```python
@dataclass(frozen=True)
class NativeSessionCredential:
    account_id: str
    session_id: str
    session_token: str
    installation_id: str
    username: str
    issued_at: str

@dataclass(frozen=True)
class NativeSessionStatus:
    account_id: str
    session_id: str
    installation_id: str
    username: str
    server_time: str

class ExplicitSessionRevocation(AuthServiceError):
    def __init__(self, *, code: str, account_id: str, session_id: str, revoked_at: str):
        super().__init__(code)
        self.reason = code
        self.code = code
        self.account_id, self.session_id, self.revoked_at = account_id, session_id, revoked_at
```

Implement fixed-origin requests and exact headers. Construct `ExplicitSessionRevocation` only for exact keys, valid UUID/timestamp, `retryable is False`, a three-item explicit code, and identity equality to the cached credential. `reason` is the canonical internal terminal field; `code` is an equal wire-vocabulary alias, and tests require equality. Mismatched 403 is `invalid_response`. Native Trace uses the new bearer route; legacy Trace receives a separate method name.

- [ ] **Step 4: Run GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_client.py -q
```

Expected: PASS for issue/status/delete/Trace, fixed origin, no-store, identity match, schema drift, and secret redaction.

- [ ] **Step 5: Commit**

```bash
rtk git add hermes_cli/client_auth/client.py tests/hermes_cli/client_auth/test_client.py
rtk git commit -m "feat(auth): add native session HTTP client"
```

### Task 8: Python Durable Authorization Cache and Validation State Machine (C2)

**Files:**
- Modify: `agent-hermes-client/hermes_cli/client_auth/runtime.py`
- Modify: `agent-hermes-client/tests/hermes_cli/client_auth/test_runtime.py`

**Interfaces:**
- Consumes: Task 7 native interfaces and existing `_SecretBackend`.
- Produces schema-v2 `NativeCredentialRecord`, schema-v1 `LegacyCredentialRecord`, `RevocationTombstone`, `ValidationState`, expanded `RuntimeSnapshot`, local restore, legacy upgrade, revalidation.
- Preserves `AuthScope(runtime_instance_id,epoch)` through validation changes.

- [ ] **Step 1: Write failing transient matrix**

```python
@pytest.mark.parametrize("reason", ["server_unavailable", "rate_limited", "invalid_response", "invalid_session_credential", "runtime_unavailable"])
def test_native_validation_failure_preserves_scope_and_cached_authorization(reason):
    owner, backend, client, clock = native_owner_factory()
    active = owner.login("alice", bytearray(b"secret"), installation_id=INSTALLATION_ID, client_version="0.17.0")
    client.native_status_error = AuthServiceError(reason)
    clock.now = owner.next_refresh_at
    degraded = owner.validate_now()
    assert degraded.state is AuthState.AUTHENTICATED
    assert degraded.scope == active.scope
    assert (degraded.account_id, degraded.session_id) == (active.account_id, active.session_id)
    assert (degraded.validation_state, degraded.validation_reason) == (ValidationState.DEGRADED, reason)
    assert backend.raw is not None
```

- [ ] **Step 2: Run RED**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py -k test_native_validation_failure_preserves_scope_and_cached_authorization -q
```

Expected: FAIL because current refresh locks and increments epoch.

- [ ] **Step 3: Implement v2 records and non-terminal validation**

```python
class ValidationState(StrEnum):
    UNKNOWN = "unknown"
    VALIDATING = "validating"
    ONLINE = "online"
    DEGRADED = "degraded"

@dataclass(frozen=True)
class NativeCredentialRecord:
    credential: NativeSessionCredential
    last_validated_at: str

@dataclass(frozen=True)
class LegacyCredentialRecord:
    cookie_record: CookieRecord
    principal_key: str

@dataclass(frozen=True)
class RevocationTombstone:
    account_id: str
    session_id: str
    reason: str
    revoked_at: str
```

Encode `version=2,kind=native`; retain cookie `version=1`; tombstone `version=2,kind=revoked` has no token. Expand RuntimeSnapshot with account/session/install/principal IDs, validation fields, last validation, legacy flag. `refresh()` loads keyring and returns cached authorization before network; `validate_now()` handles network deterministically. Ordinary AuthServiceError calls `snapshot.degraded()` without identity/scope mutation and schedules full-jitter base 1s/cap 5m. Success calls `snapshot.online()` with unchanged scope.

- [ ] **Step 4: Run transient GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py -k test_native_validation_failure_preserves_scope_and_cached_authorization -q
```

Expected: PASS for every transient reason.

- [ ] **Step 5: Add offline restart test and observe RED/GREEN**

```python
def test_native_cache_restores_before_network_after_process_restart():
    first, backend, client, _ = native_owner_factory()
    logged_in = first.login("alice", bytearray(b"secret"), installation_id=INSTALLATION_ID, client_version="0.17.0")
    client.native_status_error = AuthServiceError("server_unavailable")
    restarted = VaultOwner(client, secret_backend=backend, clock=FakeClock(), jitter=lambda *_: 1.0)
    restored = restarted.refresh()
    assert restored.state is AuthState.AUTHENTICATED
    assert (restored.account_id, restored.session_id) == (logged_in.account_id, logged_in.session_id)
    assert restored.runtime_instance_id != logged_in.runtime_instance_id
    assert client.native_status_calls == 0
```

RED expected: current owner performs status/locks. Minimum GREEN: `_load_record()` publishes cached authenticated snapshot with a fresh process runtime instance and queues validation separately.

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py -k test_native_cache_restores_before_network_after_process_restart -q
```

Expected after minimum implementation: PASS with zero validation calls before restore returns.

- [ ] **Step 6: Add explicit revoke test and observe RED/GREEN**

```python
def test_matching_explicit_revoke_writes_secret_free_tombstone_once():
    owner, backend, client, _ = native_owner_factory()
    active = owner.login("alice", bytearray(b"secret"), installation_id=INSTALLATION_ID, client_version="0.17.0")
    client.native_status_error = ExplicitSessionRevocation(code="session_revoked", account_id=active.account_id, session_id=active.session_id, revoked_at="2026-08-24T12:00:00+00:00")
    revoked = owner.validate_now()
    assert (revoked.state, revoked.reason, revoked.epoch) == (AuthState.LOCKED, "session_revoked", active.epoch + 1)
    decoded = json.loads(backend.raw)
    assert decoded["kind"] == "revoked"
    assert "session-token-sentinel" not in backend.raw
```

RED expected: no tombstone/identity match. Minimum GREEN: match current identity, write tombstone, publish one terminal epoch; repeated validation is idempotent.

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py -k test_matching_explicit_revoke_writes_secret_free_tombstone_once -q
```

Expected after minimum implementation: PASS and persisted JSON contains no token.

- [ ] **Step 7: Add legacy restore/atomic upgrade test and observe RED/GREEN**

```python
def test_legacy_cookie_restores_offline_then_upgrades_atomically_online():
    raw = legacy_v1_blob()
    backend = FakeSecretBackend(raw=raw)
    client = FakeAuthClient(native_status_error=AuthServiceError("server_unavailable"))
    owner = VaultOwner(client, secret_backend=backend, clock=FakeClock(), jitter=lambda *_: 1.0)
    restored = owner.refresh()
    assert restored.legacy is True
    assert restored.principal_key == "legacy:" + hashlib.sha256(raw.encode()).hexdigest()
    client.native_status_error = None
    upgraded = owner.validate_now()
    assert upgraded.legacy is False
    assert upgraded.account_id == ACCOUNT_ID
    assert json.loads(backend.raw)["kind"] == "native"
```

RED expected: v1 expiry rejects offline. Minimum GREEN: digest raw canonical v1 record, restore locally, validate Web cookie online, issue native Session, verify v2 write/readback before replacing v1. Failed write retains v1/degraded state. Sign-out deletes either active schema.

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py -k test_legacy_cookie_restores_offline_then_upgrades_atomically_online -q
```

Expected after minimum implementation: PASS; offline restore uses digest identity and online success stores native schema.

- [ ] **Step 8: Run complete GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_runtime.py -q
```

Expected: PASS; replace `test_refresh_failure_revokes_scope_without_extra_grace` with preservation semantics while retaining logout/epoch isolation.

- [ ] **Step 9: Commit**

```bash
rtk git add hermes_cli/client_auth/runtime.py tests/hermes_cli/client_auth/test_runtime.py
rtk git commit -m "feat(auth): restore durable local authorization offline"
```

### Task 9: Python-to-Electron Bridge Protocol v2 (C3)

**Files:**
- Modify: `agent-hermes-client/hermes_cli/client_auth/bridge.py`
- Modify: `agent-hermes-client/tests/hermes_cli/client_auth/test_bridge.py`
- Modify: `agent-hermes-client/apps/desktop/electron/auth-bridge.ts`
- Modify: `agent-hermes-client/apps/desktop/electron/auth-bridge.test.ts`
- Modify: `agent-hermes-client/apps/desktop/electron/auth-runtime-contract.ts`
- Modify: `agent-hermes-client/apps/desktop/electron/auth-runtime-contract.test.ts`
- Modify: `agent-hermes-client/apps/desktop/electron/task4-auth-runtime.contract.test.ts`

**Interfaces:**
- Consumes: Task 8 public snapshot.
- Produces `AUTH_BRIDGE_PROTOCOL_VERSION=2`, `NativeClientContext`, expanded `BridgeStatus`, validated status/login frames carrying native context.
- Produces one exact preload-to-renderer adapter: the sanitized `BridgeStatus` object is forwarded unchanged and `DesktopAccountStatus` is a structural alias of the exported public status shape, with no second parser or divergent field list.
- Renderer login remains `login(username,password)`; DesktopAuthBridge injects context.

- [ ] **Step 1: Write failing exact round-trip test**

```typescript
test('status carries native context and accepts degraded health without secrets', async () => {
  const child = fakeChild()
  const bridge = new DesktopAuthBridge({ cwd: '/repo', pythonExecutable: '/python', nativeClientContext: {
    installation_id: '11111111-1111-4111-8111-111111111111', client_version: '0.17.0'
  }, spawnChild: () => child })
  const pending = bridge.status()
  const request = JSON.parse(child.stdin.frames[0])
  assert.deepEqual(request.params, { installation_id: '11111111-1111-4111-8111-111111111111', client_version: '0.17.0' })
  child.stdout.emit('data', JSON.stringify({ version: 2, id: request.id, result: nativeBridgeStatus({ validation_state: 'degraded', validation_reason: 'server_unavailable' }) }) + '\n')
  const status = await pending
  assert.equal(status.validation_state, 'degraded')
  assert.equal(JSON.stringify(status).includes('session_token'), false)
})
```

- [ ] **Step 2: Run RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/auth-bridge.test.ts
```

Expected: FAIL because context, v2, and expanded fields are absent.

- [ ] **Step 3: Implement exact v2 types/validators**

```typescript
export type NativeClientContext = { installation_id: string; client_version: string }
export type BridgeStatus = {
  state: 'checking' | 'authenticated' | 'signed_out' | 'locked'
  username: string | null; account_id: string | null; session_id: string | null
  installation_id: string | null; principal_key: string | null
  runtime_instance_id: string; epoch: number; valid_until: number
  validation_state: 'unknown' | 'validating' | 'online' | 'degraded'
  validation_reason: string | null; last_validated_at: string | null
  legacy: boolean; reason: string | null
}
```

Mirror exact keys in Python `_PUBLIC_KEYS/_validated_public_result`. Add `invalid_session_credential` to validation reasons and three explicit reasons to terminal vocabulary. Status/login frames include context; logout stays empty; Trace stays explicit. Update auth marker protocol expectation to 2.

- [ ] **Step 4: Run GREEN**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_bridge.py -q
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/auth-bridge.test.ts electron/auth-runtime-contract.test.ts electron/task4-auth-runtime.contract.test.ts
```

Expected: PASS; malformed/extra fields fail, secrets are absent, packaging expects v2.

- [ ] **Step 5: Commit**

```bash
rtk git add hermes_cli/client_auth/bridge.py tests/hermes_cli/client_auth/test_bridge.py apps/desktop/electron/auth-bridge.ts apps/desktop/electron/auth-bridge.test.ts apps/desktop/electron/auth-runtime-contract.ts apps/desktop/electron/auth-runtime-contract.test.ts apps/desktop/electron/task4-auth-runtime.contract.test.ts
rtk git commit -m "feat(auth): carry durable authorization over bridge v2"
```

### Task 10: Electron Coordinator Preservation and Terminal Revocation (C4)

**Files:**
- Modify: `agent-hermes-client/apps/desktop/electron/auth-coordinator.ts`
- Modify: `agent-hermes-client/apps/desktop/electron/auth-coordinator.test.ts`

**Interfaces:**
- Consumes: Task 9 BridgeStatus, ConnectionScope, cleanup callback.
- Produces `isLocallyAuthorized()`, terminal classifier, transient degradation merge, exact-once cleanup for matching current identity.

- [ ] **Step 1: Write failing transient matrix**

```typescript
test.each(['server_unavailable', 'rate_limited', 'invalid_response', 'invalid_session_credential', 'runtime_unavailable'])(
  'preserves local scope for transient %s', async reason => {
    const { bridge, cleanup, coordinator } = fixture(nativeAuthenticated)
    await coordinator.start()
    const before = coordinator.scope('local')
    bridge.status.mockRejectedValueOnce(new AuthBridgeError(reason, reason))
    const result = await coordinator.refresh('local', { recoverRuntime: true })
    assert.deepEqual(coordinator.scope('local'), before)
    assert.equal(result.state, 'authenticated')
    assert.equal(result.validation_state, 'degraded')
    assert.equal(cleanup.mock.calls.length, 0)
    await assert.doesNotReject(coordinator.require('local', 'local'))
  }
)
```

- [ ] **Step 2: Run RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/auth-coordinator.test.ts
```

Expected: FAIL because `applyFailure()` deletes scope and cleans capability.

- [ ] **Step 3: Implement split authority**

```typescript
const EXPLICIT_TERMINAL_REASONS = new Set(['account_disabled', 'account_revoked', 'session_revoked'])
function isLocallyAuthorized(status: BridgeStatus): boolean {
  return status.state === 'authenticated' && Boolean(status.principal_key)
}
```

For local existing scope, applyFailure returns last authorized identity with degraded health. `requireConnection()` checks local authorization/exact scope, not online lease. `applyStatus()` cleans only signed-out, matching explicit identity, or account switch. Mismatched explicit identity degrades; repeated terminal status cannot clean twice. Keep legacy remote expiry behavior isolated.

- [ ] **Step 4: Add terminal/recovery table and run GREEN**

Add rows for three explicit codes, sign-out, mismatched identity, recovery online, and account switch. Matching terminal removes scope/cleans once; mismatch preserves; recovery keeps epoch/zero cleanup.

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/auth-coordinator.test.ts
```

Expected: PASS for transient, terminal, identity mismatch, connection isolation, recovery.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/auth-coordinator.ts apps/desktop/electron/auth-coordinator.test.ts
rtk git commit -m "fix(auth): preserve desktop scope during service outages"
```

### Task 11: Electron Startup and Trace Credential Decoupling (C5)

**Files:**
- Modify: `agent-hermes-client/apps/desktop/electron/main.ts`
- Create: `agent-hermes-client/apps/desktop/electron/desktop-trace-startup.ts`
- Create: `agent-hermes-client/apps/desktop/electron/desktop-trace-startup.test.ts`
- Modify: `agent-hermes-client/apps/desktop/electron/desktop-runtime-gate.test.ts`

**Interfaces:**
- Consumes: Task 10 stable local scope and the existing token-independent `TraceForwarder.start(epoch)` listener.
- Produces `prepareLocalTraceCapture(scope): Promise<TraceContext | null>`; backend waits only for local capture setup, never a cloud token.
- Integration seam: Trace Stream replaces listener storage/pump internals without changing authentication authority.

- [ ] **Step 1: Write failing token-independent startup test**

```typescript
test('backend preparation continues when local trace capture cannot start', async () => {
  const events: string[] = []
  const result = await prepareLocalTraceCapture({
    startListener: async () => Promise.reject(new Error('local listener unavailable')),
    onDiagnostic: message => events.push(`diagnostic:${message}`),
    scheduleRetry: () => events.push('retry')
  })
  events.push('spawn-backend')
  assert.equal(result, null)
  assert.deepEqual(events, ['diagnostic:trace capture unavailable', 'retry', 'spawn-backend'])
})
```

- [ ] **Step 2: Run RED**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/desktop-trace-startup.test.ts electron/desktop-runtime-gate.test.ts
```

Expected: FAIL because the new startup seam is absent and `ensureDesktopTraceForwarder()` awaits `provider.current()` before backend preparation.

- [ ] **Step 3: Implement minimum decoupling**

Create the exact seam:

```typescript
export async function prepareLocalTraceCapture<T>({
  startListener,
  onDiagnostic,
  scheduleRetry
}: {
  startListener: () => Promise<T>
  onDiagnostic: (message: string) => void
  scheduleRetry: () => void
}): Promise<T | null> {
  try {
    return await startListener()
  } catch {
    onDiagnostic('trace capture unavailable')
    scheduleRetry()
    return null
  }
}
```

In `ensureDesktopTraceForwarder`, remove credential preflight and retain local listener creation; Stream A does not otherwise modify `trace-forwarder.ts`, whose durable storage/pump internals belong to Stream B:

```typescript
const forwarder = new TraceForwarder({ credentialProvider: provider, installationId: desktopInstallationId })
const started = await forwarder.start(scope.epoch)
```

Implement `prepareLocalTraceCapture()` to catch listener/setup failure, emit only redacted diagnostics, schedule retry, and return null so Hermes still starts. A cloud credential error occurs only in the upload pump. Primary and pooled backends use this seam. Auth subscription starts/retains backend for every locally authorized status; cleanup and runtime-gate invalidation occur only through Task 10 terminal transitions.

- [ ] **Step 4: Run GREEN**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/desktop-trace-startup.test.ts electron/desktop-runtime-gate.test.ts
```

Expected: PASS; cloud token is not required for listener/backend, listener failure schedules retry without blocking backend, runtime gate stays ready while validation degrades.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/electron/main.ts apps/desktop/electron/desktop-trace-startup.ts apps/desktop/electron/desktop-trace-startup.test.ts apps/desktop/electron/desktop-runtime-gate.test.ts
rtk git commit -m "fix(auth): decouple local runtime from trace credentials"
```

### Task 12: Renderer Keeps Conversation Mounted (C6)

**Files:**
- Modify: `agent-hermes-client/apps/desktop/src/components/auth-gate.tsx`
- Modify: `agent-hermes-client/apps/desktop/src/components/auth-gate.test.tsx`
- Modify: `agent-hermes-client/apps/desktop/src/i18n/index.ts`
- Modify: `agent-hermes-client/apps/desktop/src/i18n/auth-catalog.test.ts`

**Interfaces:**
- Consumes: Task 9's sanitized public `BridgeStatus`, exposed through preload as the structurally identical `DesktopAccountStatus`, and Task 10 semantics.
- Produces cached-authorization gate plus passive validation-health display.
- Protected tree mounts for local authorization + runtime readiness and remains the same React instance through degradation/recovery.

- [ ] **Step 1: Write failing timeout preservation test**

```tsx
it('keeps the protected conversation mounted when validation times out', async () => {
  vi.useFakeTimers()
  const { emit } = renderGate({ status: vi.fn(async () => authenticatedOnline) })
  expect(await screen.findByText('Protected Hermes application')).not.toBeNull()
  act(() => emit({ ...authenticatedOnline, validation_state: 'degraded', validation_reason: 'runtime_unavailable' }))
  await act(async () => vi.advanceTimersByTimeAsync(15_000))
  expect(screen.getByText('Protected Hermes application')).not.toBeNull()
  expect(screen.queryByRole('heading', { name: 'Sign in to Ansatz' })).toBeNull()
})
```

- [ ] **Step 2: Run RED**

```bash
rtk npm --prefix apps/desktop run test:ui -- src/components/auth-gate.test.tsx
```

Expected: FAIL because current catch/event path replaces status and renders login.

- [ ] **Step 3: Implement local authorization gate**

```typescript
function hasCachedLocalAuthorization(status: DesktopAccountStatus): boolean {
  return status.state === 'authenticated' && Boolean(status.principal_key)
}
function degradedFrom(current: DesktopAccountStatus, reason: string): DesktopAccountStatus {
  return hasCachedLocalAuthorization(current)
    ? { ...current, validation_state: 'degraded', validation_reason: reason }
    : unavailableStatus()
}
```

Status/login catch uses functional state update and preserves event revision ordering. Mount children when cached authorization and `runtime_ready`. Degraded status is passive localized copy; it never autofocuses, navigates, resets conversation state, or renders credential inputs.

- [ ] **Step 4: Add terminal/recovery cycle and run GREEN**

Update old lock expectations: `session_expired/server_unavailable/runtime_unavailable` retain children. Add table rows `account_disabled/account_revoked/session_revoked/signed_out` which unmount. Add a child instance counter proving degraded→online does not remount.

```bash
rtk npm --prefix apps/desktop run test:ui -- src/components/auth-gate.test.tsx
rtk npm --prefix apps/desktop run test:ui -- src/i18n/auth-catalog.test.ts
```

Expected: PASS for preservation, terminal navigation, safe copy, and same child instance.

- [ ] **Step 5: Commit**

```bash
rtk git add apps/desktop/src/components/auth-gate.tsx apps/desktop/src/components/auth-gate.test.tsx apps/desktop/src/i18n/index.ts apps/desktop/src/i18n/auth-catalog.test.ts
rtk git commit -m "fix(auth): keep conversations mounted while validation degrades"
```

### Task 13: Sign-Out Preservation and Continuity E2E (C7)

**Files:**
- Modify: `agent-hermes-client/apps/desktop/e2e/fixed-auth-contract-server.ts`
- Create: `agent-hermes-client/apps/desktop/e2e/auth-continuity.spec.ts`
- Modify: `agent-hermes-client/apps/desktop/e2e/installed-windows-auth.spec.ts`
- Modify: `agent-hermes-client/apps/desktop/e2e/auth-assertions.ts`

**Interfaces:**
- Consumes: completed client/server contract and lifecycle.
- Produces controllable auth server `setMode()`, `revokeCurrent()`, `currentIdentity()` and black-box offline/sign-out evidence.
- Local digests cover SessionDB/WAL/SHM when present, attachments, projects, profiles, and exported conversation content; credential-store files never enter reports.

- [ ] **Step 1: Write failing offline restart E2E**

```typescript
test('cached authorization restarts offline and silently revalidates', async () => {
  const fixture = await launchAuthenticatedFixture()
  await fixture.login()
  await fixture.expectBackendReady()
  await fixture.createConversation('offline continuity sentinel')
  const before = fixture.localDataDigests()
  fixture.server.setMode('timeout')
  await fixture.restartDesktop()
  await expect(fixture.page.getByText('offline continuity sentinel')).toBeVisible()
  await expect(fixture.page.locator('[contenteditable="true"]')).toBeVisible()
  await expect(fixture.page.getByRole('heading', { name: /sign in to ansatz/i })).toHaveCount(0)
  expect(fixture.backendProcessCount()).toBeGreaterThan(0)
  fixture.server.setMode('online')
  await fixture.expectValidationState('online')
  expect(fixture.localDataDigests()).toEqual(before)
})
```

- [ ] **Step 2: Build/run RED**

```bash
rtk npm --prefix apps/desktop run build
rtk npm --prefix apps/desktop exec -- playwright test e2e/auth-continuity.spec.ts --reporter=list
```

Expected: FAIL because the fixed server lacks native/outage controls and current restart returns to login or tears down backend.

- [ ] **Step 3: Implement controllable server**

```typescript
type AuthServiceMode = 'online' | 'timeout' | '429' | '500' | 'malformed'
interface FixedAuthContractServer {
  setMode(mode: AuthServiceMode): void
  revokeCurrent(reason: 'account_disabled' | 'account_revoked' | 'session_revoked'): void
  currentIdentity(): { accountId: string; sessionId: string; installationId: string }
}
```

Implement exact native routes. Timeout holds beyond client deadline; mode change releases held sockets. Structured revoke carries matching identity. Server diagnostics redact random test credentials.

- [ ] **Step 4: Add sign-out failing test and minimum GREEN**

```typescript
test('sign out clears access but preserves local artifacts byte for byte', async () => {
  const fixture = await launchAuthenticatedFixture()
  await fixture.login()
  await fixture.createConversation('preserve on signout')
  await fixture.createAttachment('attachment-preservation.bin', Buffer.from([0, 1, 2, 3]))
  const before = fixture.localDataDigests()
  await fixture.page.evaluate(() => window.hermesDesktop.auth.logout())
  await expect(fixture.page.getByRole('heading', { name: /sign in to ansatz/i })).toBeVisible()
  expect(fixture.localDataDigests()).toEqual(before)
  expect(fixture.backendProcessCount()).toBe(0)
})
```

RED expected if cleanup deletes/mutates data. Minimum GREEN confines sign-out to credential deletion, access revocation, windows/backend teardown; no SessionDB/attachment/project/profile path is removed.

- [ ] **Step 5: Add revoke/transient matrix and run GREEN**

Add explicit revoke test asserting cleanup event count 1 and identical hashes. Add transient rows timeout/429/500/malformed/bridge-owner restart asserting same backend PID and conversation DOM. Extend Windows installed auth test: backend descendants survive outage and disappear only after terminal revoke/sign-out.

```bash
rtk npm --prefix apps/desktop exec -- playwright test e2e/auth-continuity.spec.ts --reporter=list
```

Expected: PASS for offline restart, silent recovery, transient matrix, one terminal cleanup, and byte-identical data.

- [ ] **Step 6: Run Windows proof when Windows runner exists**

```powershell
npm --prefix apps/desktop exec -- playwright test e2e/installed-windows-auth.spec.ts --reporter=list
```

Expected: PASS. If implementation host is macOS, record Windows proof as unavailable rather than claim it.

- [ ] **Step 7: Commit**

```bash
rtk git add apps/desktop/e2e/fixed-auth-contract-server.ts apps/desktop/e2e/auth-continuity.spec.ts apps/desktop/e2e/installed-windows-auth.spec.ts apps/desktop/e2e/auth-assertions.ts
rtk git commit -m "test(auth): prove offline restart and data-preserving signout"
```

### Task 14: Operational Contract Handoff

**Files:**
- Create: `agent-langfuse-server/auth-service/NATIVE_CLIENT_SESSION.md`
- Modify: `agent-langfuse-server/auth-service/OPERATIONS.md`
- Modify: `agent-langfuse-server/auth-service/PROJECT_STATUS.md`

**Interfaces:**
- Consumes: implemented routes, migrations, admin actions, codes, rollout order.
- Produces operator procedure for disable/revoke, compatibility/rollback, secret handling, native Trace classification.

- [ ] **Step 1: Write concrete handoff**

Document immutable account identity; all route/header/request/response examples with sentinel tokens; retryable invalid credential; three terminal codes plus identity match; admin actions and prohibition on Session deletion; migrations 0006/0007; native Trace reasons; retained legacy routes; rollback that stops using routes but keeps records; local data preservation.

- [ ] **Step 2: Verify names through behavior tests**

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_native_client_session_contract history.tests.test_native_client_session_api history.tests.test_admin_auth history.tests.test_native_trace_tokens
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_contract_fixture.py -q
```

Expected: PASS; named routes/actions/codes are executable contracts.

- [ ] **Step 3: Commit server handoff**

```bash
rtk git add auth-service/NATIVE_CLIENT_SESSION.md auth-service/OPERATIONS.md auth-service/PROJECT_STATUS.md
rtk git commit -m "docs(auth): hand off native session operations"
```

Platform routing docs change only after Task 15 has evidence.

### Task 15: Integration Order and Fresh Completion Verification

**Files:**
- Integrate server branch `feature/auth-continuity-protocol`.
- Integrate client branch `feature/auth-continuity`.
- Resolve Stream A/B overlap on client `feature/auth-trace-continuity`, based on pinned client main.
- Modify only for contract wiring: `agent-hermes-client/apps/desktop/electron/main.ts`
- Create: `ansatz-agent-platform/docs/reports/2026-08-24-authentication-continuity.md`
- Modify: `ansatz-agent-platform/docs/02-progress.md`
- Modify: `ansatz-agent-platform/docs/03-file-index.md`

**Interfaces:**
- Consumes: Tasks 1–14 and Trace outbox integration interface.
- Produces one client where cached auth owns local capability, validation owns diagnostics, outbox owns durability, Trace provider owns upload readiness, Gateway receipt owns payload deletion.
- Produces pinned ancestry, contract parity, tests, secret scan, data hashes, and report.

- [ ] **Step 1: Verify ancestry**

```bash
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity merge-base --is-ancestor 80bc34f5f18d2d58d1866b3140f8a1c6bc953928 feature/auth-continuity
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity merge-base --is-ancestor 31a22bf1bf49d6006f140fa2f726e6759845c1e7 feature/auth-continuity-protocol
```

Expected: both exit 0. Inspect first-parent log/diff; no prohibited branch content.

- [ ] **Step 2: Integrate dependency order**

Client order: contract fixture → HTTP client → Python cache/runtime → bridge v2 → coordinator → lifecycle → renderer → E2E → Trace Stream modules. Resolve `main.ts` manually so durable local capture starts without cloud token and cleanup occurs only terminally. Never take an entire conflict side without reconciling the five authorities in the Interfaces block.

- [ ] **Step 3: Run server proof**

```bash
rtk proxy conda run -n dl python manage.py makemigrations --check --dry-run
rtk proxy conda run -n dl python manage.py check
rtk proxy conda run -n dl python manage.py test history.tests.test_migrations history.tests.test_client_session_models history.tests.test_client_sessions history.tests.test_native_client_session_api history.tests.test_client_session_api history.tests.test_native_client_session_contract history.tests.test_native_trace_tokens history.tests.test_trace_tokens history.tests.test_admin_auth history.tests.test_auth_surface
```

Expected: `No changes detected`; no system-check issues; tests PASS exit 0.

- [ ] **Step 4: Run client Python proof**

```bash
rtk proxy conda run -n dl bash scripts/run_tests.sh tests/hermes_cli/client_auth/test_contract_fixture.py tests/hermes_cli/client_auth/test_client.py tests/hermes_cli/client_auth/test_runtime.py tests/hermes_cli/client_auth/test_bridge.py -q
```

Expected: PASS exit 0.

- [ ] **Step 5: Run Electron/renderer proof**

```bash
rtk npm --prefix apps/desktop run test:desktop:platforms -- electron/auth-bridge.test.ts electron/auth-coordinator.test.ts electron/authenticated-runtime-preparation.test.ts electron/desktop-runtime-gate.test.ts electron/trace-forwarder.test.ts electron/auth-runtime-contract.test.ts
rtk npm --prefix apps/desktop run test:ui -- src/components/auth-gate.test.tsx src/i18n/auth-catalog.test.ts
rtk npm --prefix apps/desktop run typecheck
rtk npm --prefix apps/desktop run lint
```

Expected: Vitest PASS; TypeScript/ESLint exit 0.

- [ ] **Step 6: Run E2E proof**

```bash
rtk npm --prefix apps/desktop run build
rtk npm --prefix apps/desktop exec -- playwright test e2e/auth-continuity.spec.ts --reporter=list
```

Expected: build/E2E exit 0 and prove cached offline restart, transient matrix, silent recovery, token independence, structured revoke, data hashes.

- [ ] **Step 7: Audit contracts/secrets/diff**

```bash
rtk cmp /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity/auth-service/contracts/native-client-session-v1.json /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity/docs/contracts/native-client-session-v1.json
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity diff --check 31a22bf1bf49d6006f140fa2f726e6759845c1e7...HEAD
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity diff --check 80bc34f5f18d2d58d1866b3140f8a1c6bc953928...HEAD
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity diff --name-status 31a22bf1bf49d6006f140fa2f726e6759845c1e7...HEAD
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity diff --name-status 80bc34f5f18d2d58d1866b3140f8a1c6bc953928...HEAD
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-auth-continuity diff 31a22bf1bf49d6006f140fa2f726e6759845c1e7...HEAD | rtk rg -n '(session_token|access_token|Authorization: Bearer|__Host-ansatz_sessionid)'
rtk git -C /Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/client-auth-continuity diff 80bc34f5f18d2d58d1866b3140f8a1c6bc953928...HEAD | rtk rg -n '(session_token|access_token|Authorization: Bearer|__Host-ansatz_sessionid)'
```

Expected: `cmp`/diff check exit 0; paths are in scope; secret hits are field names, redaction rules, or non-secret sentinels only.

- [ ] **Step 8: Write evidence and routing updates**

Report exact commits, commands, timestamps, exit codes, test counts, data hashes, Windows evidence availability, and rollout steps. Update `02-progress.md` only with mutable status and `03-file-index.md` only with authoritative paths.

- [ ] **Step 9: Commit evidence after proof**

```bash
rtk git add docs/reports/2026-08-24-authentication-continuity.md docs/02-progress.md docs/03-file-index.md
rtk git commit -m "docs(auth): record authentication continuity verification"
```

No push, pull request, deployment, production migration, remote mutation, or destructive worktree cleanup is authorized.
