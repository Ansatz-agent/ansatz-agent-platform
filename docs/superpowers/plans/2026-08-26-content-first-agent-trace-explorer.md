# Content-first Agent Trace Explorer Implementation Plan

**Date:** 2026-08-26
**Design:** `docs/superpowers/specs/2026-08-26-content-first-agent-trace-explorer-design.md`
**Repository:** `../agent-langfuse-server`
**Delivery boundary:** Local implementation and browser QA only; no commit, push, PR, merge, or deployment.

## Task 1: Trace navigation and owner-scoped index

**Files**

- `auth-service/config/urls.py`
- `auth-service/history/trace_views.py`
- `auth-service/templates/traces/app_shell.html`
- `auth-service/templates/traces/trace_index.html` (new)
- `auth-service/history/tests/test_trace_dashboard.py`

**Test first**

1. Add a failing test for `reverse("trace-index") == "/traces/runs/"`.
2. Add a failing authenticated rendering test that requires a third `Trace` navigation item, active state, trace rows, content previews, compact usage, and direct detail links.
3. Add a failing owner-isolation test proving foreign observations are absent.
4. Add a failing unavailable-state test requiring HTTP 503 and no unscoped retry.

**Implementation**

- Add the route and `trace_index` view.
- Reuse `_client_list(..., include_io=True)` so the forced owner filter remains unchanged.
- Add a bounded `_trace_index_context` projection grouped by trace ID, sorted newest first, with request/final previews, session, status, models, tools, tokens, cost, and exact detail URL.
- Render full-width content-preview rows and date/status/model/search controls.

**Proof**

- `python manage.py test history.tests.test_trace_dashboard --verbosity 2`
- Expected: new red tests fail for missing route/template, then the complete module passes.

## Task 2: Semantic content projection

**Files**

- `auth-service/history/trace_views.py`
- `auth-service/history/tests/test_trace_dashboard.py`

**Test first**

1. Add a failing context test requiring ordered semantic blocks: `user_request`, `model_exchange`, `tool_exchange`, `final_response`.
2. Require tool input and output to remain in one block.
3. Require missing fields and unknown observation types to degrade to captured neutral events.
4. Require tokens/cost/duration to remain available as secondary metadata.

**Implementation**

- Add `_content_blocks` using only captured root/generation/tool/span evidence.
- Deduplicate a root final response only when it is byte-for-byte equivalent to the last model output.
- Preserve escaped display strings and raw values already produced by `_decorate_observations`.

**Proof**

- `python manage.py test history.tests.test_trace_dashboard.TraceDashboardTests.test_trace_detail_uses_content_first_semantic_blocks --verbosity 2`
- Expected: fail before projection; pass after ordered block implementation.

## Task 3: Content-first detail UI

**Files**

- `auth-service/templates/traces/trace_detail.html`
- `auth-service/history/static/history/trace_analytics.css`
- `auth-service/history/static/history/trace_analytics.js`
- `auth-service/history/tests/test_trace_dashboard.py`
- `auth-service/history/tests/test_auth_surface.py`

**Test first**

1. Replace the old timeline-default assertion with a failing `Content`-default assertion.
2. Require `Performance` and `Raw` secondary views.
3. Require a run outline and visible prompt/tool/result/final content without row expansion.
4. Require the waterfall selector to exist only inside the Performance panel.
5. Require typography floors: body 14 px, secondary 12 px, payload 13 px.

**Implementation**

- Rebuild the page heading and compact facts so duration is trailing muted metadata.
- Render sticky outline + content document.
- Render semantic content cards with bounded previews and accessible expansion.
- Move the existing execution waterfall into Performance.
- Keep Raw observations intact.
- Update tab switching and outline scrolling with server-rendered fallback readability.

**Proof**

- `python manage.py test history.tests.test_trace_dashboard history.tests.test_auth_surface --verbosity 2`
- `node --check history/static/history/trace_analytics.js`
- Expected: both Django modules and JavaScript syntax pass.

## Task 4: Readability consistency

**Files**

- `auth-service/history/static/history/trace_analytics.css`
- `auth-service/history/tests/test_auth_surface.py`

**Test first**

- Preserve the new failing/green typography tests for Recent Sessions and Token composition.
- Add coverage for trace index rows, outline labels, content headings, content payloads, and performance labels.

**Implementation**

- Use 14–16 px for meaningful text, 12–13 px for secondary metadata, and no 8–10 px trace content.
- Verify responsive behavior without shrinking text below the floor.

**Proof**

- `python manage.py test history.tests.test_auth_surface --verbosity 2`
- Expected: all typography contract tests pass.

## Task 5: Browser and full verification

**Files/artifacts**

- `tmp/trace-explorer-content-first/` screenshots and geometry evidence only.

**Verification**

1. Restart the local QA Django server against the existing fake Langfuse dataset.
2. Open `/traces/runs/` and `/traces/trace/trace-ui-qa/` in the foreground browser.
3. Verify desktop light/dark, mobile, long payload visibility, tabs, outline navigation, and no console errors.
4. Run:
   - `python manage.py test history.tests --verbosity 1`
   - `python manage.py check`
   - `node --check history/static/history/trace_analytics.js`
   - `git diff --check`
5. Inspect full outputs and exit codes before claiming completion.

**Expected outcome**

- All tests and checks exit 0.
- The user can inspect the completed Trace index and content-first detail in the foreground browser.
- Changes remain local and uncommitted until separately authorized.
