# Session-first Trace Inspector Implementation Plan

**Date:** 2026-08-27
**Design:** `docs/superpowers/specs/2026-08-27-session-first-trace-inspector-design.md`
**Repository:** `../agent-langfuse-server`
**Delivery:** Local implementation → browser QA → commit → PR → merge → sync local `main` → build and deploy Hermes auth-service from merged `main`.

## Task 1: Canonical Session index and compatibility route

**Files:** `auth-service/config/urls.py`, `history/trace_views.py`,
`templates/traces/app_shell.html`, `templates/traces/trace_index.html`,
`history/tests/test_trace_dashboard.py`.

**Test first:**

1. Require `reverse("trace-index") == "/traces/sessions/"`.
2. Require `/traces/runs/` to redirect to the canonical route while preserving only bounded `days` and `q`.
3. Seed two traces in one session plus one other session and require exactly two session rows.
4. Require session aggregates, request/latest-response previews, models/tools, ordering, and owner isolation.
5. Require missing session IDs to appear in one deterministic `Unsessioned traces` bucket.

**Implementation:** replace the trace-group projection with `_session_index_context`; retain
forced owner scoping and generic unavailable behavior. The navigation continues to say `Trace`
but links to the canonical session index.

**Proof:** targeted view tests fail before route/projection work and pass afterwards.

## Task 2: Compact Session detail

**Files:** `history/trace_views.py`, `templates/traces/session_detail.html`,
`history/tests/test_trace_dashboard.py`, `history/static/history/trace_analytics.css`.

**Test first:** require one compact row per trace, chronological ordering, aggregate metrics,
request/response previews, direct inspector links, absence of full payload `<pre>` blocks, and
foreign-session 404 behavior.

**Implementation:** reuse one per-trace summary projector in index and session detail; render a
compact table/list with no expanded payloads and a breadcrumb back to all sessions.

**Proof:** session-detail tests pass and rendered HTML contains no legacy payload grid.

## Task 3: Server-side selected-step projection and fragment endpoint

**Files:** `config/urls.py`, `history/trace_views.py`,
`templates/traces/_trace_step_panel.html`, `history/tests/test_trace_dashboard.py`.

**Test first:**

1. Require default `overview` selection with root request/final response.
2. Require a valid observation ID to select exactly one semantic step.
3. Require invalid/foreign IDs to fall back safely without leaking existence.
4. Require the fragment endpoint to apply the same owner and trace checks and return escaped HTML.
5. Cover LLM, tool, event, error, missing input/output, and malformed structured payloads.

**Implementation:** create stable compact step descriptors from decorated observations; Overview
is synthetic and root-owned. Render only the selected panel. The fragment endpoint returns the
same partial template and no account-selecting interface.

**Proof:** targeted projection, fragment, escaping, and owner-isolation tests pass.

## Task 4: Bounded two-pane inspector and progressive navigation

**Files:** `templates/traces/trace_detail.html`,
`history/static/history/trace_analytics.css`,
`history/static/history/trace_analytics.js`, `history/tests/test_auth_surface.py`,
`history/tests/test_trace_dashboard.py`.

**Test first:** require the step rail, selected-content panel, ordinary fallback links,
Content/Performance/Raw tabs, bounded viewport CSS, independent scroll regions, 14/12/13 px
typography floors, and no vertical semantic-card document.

**Implementation:**

- compact breadcrumb/header and facts;
- independently scrolling 300 px step rail carrying type/name/model/duration/tokens/error;
- wide selected detail with Input/Output/Metadata controls and tool arguments/result together;
- line-wrap toggle and copy control using structured event handlers;
- intercepted same-origin fragment navigation with history updates, in-memory cache, busy state,
  and ordinary-link fallback;
- Performance and Raw remain secondary views.

**Proof:** Django tests and `node --check` pass; no inline event handlers or unsafe HTML from
captured content.

## Task 5: Long-trace fixture and browser verification

**Files/artifacts:** existing QA mock dataset plus
`tmp/trace-explorer-session-first/` screenshots/evidence.

1. Add a 50-step owner-scoped QA session containing long LLM/tool input/output and an error.
2. Restart local mock/Django preview.
3. Verify session index grouping, compact session trace list, step 40 direct selection,
   fragment navigation, browser history, copy/wrap controls, Performance/Raw, and zero console errors.
4. Verify desktop/mobile and light/dark screenshots; prove page height remains bounded while the
   rail/payload panes scroll independently.
5. Run `python manage.py test history.tests --verbosity 1`, `python manage.py check`,
   `node --check history/static/history/trace_analytics.js`, and `git diff --check`.

## Task 6: Git delivery

1. Inspect the exact diff and ensure the branch is based on current `origin/main`.
2. Commit only intended repository files with a focused message.
3. Push the feature branch and create a PR against `main`.
4. Wait for required checks, inspect failures, and fix until green.
5. Merge the PR, fetch, switch local `main`, and update it by fast-forward only.
6. Verify local `main == origin/main == PR merge commit` and the worktree is clean.

## Task 7: Hermes deployment from merged main

1. Read the authoritative deployment/runbook and current production identity before mutation.
2. Verify remote host, repository/ref, current image/container IDs, health, and rollback image.
3. Build a new auth-service image from the verified merged `main` commit; tag includes `main`,
   date, and commit prefix.
4. Update only auth-service using the repository deployment mechanism and restart it.
5. Verify container/image labels, private/public health, login, `/traces/sessions/`, owner-scoped
   Session/Trace pages, and storage/CPU/memory sanity.
6. Record deployed commit/image evidence in `docs/02-progress.md`; preserve rollback data.

## Completion Evidence

- Approved design and this plan match the delivered behavior.
- Full automated suite and browser long-trace QA pass.
- PR is merged into `main`; local and remote `main` match.
- Hermes auth-service runs an image built from that exact merged `main` commit.
- Production Trace routes render correctly with owner-scoped data and no legacy trace-first index.
