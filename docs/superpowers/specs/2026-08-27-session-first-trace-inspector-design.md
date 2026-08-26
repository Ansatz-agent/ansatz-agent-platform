# Session-first Trace Inspector Design

**Status:** Approved by user on 2026-08-27
**Date:** 2026-08-27
**Classification:** Architectural revision
**Supersedes:** The index and Content-detail layout portions of `2026-08-26-content-first-agent-trace-explorer-design.md`
**Scope:** Ordinary-user Trace navigation in `agent-langfuse-server/auth-service`

## Problem

The current Trace index groups observations by trace ID and renders one large row per trace. This exposes an implementation-level unit before the user has chosen a conversation/session. A session containing many traces becomes fragmented and difficult to scan.

The current trace Content view renders every semantic block as a fully expanded vertical card. In the QA fixture, five blocks already produce a 2,373 px document in a 906 px viewport; two model blocks alone consume 555 px and 531 px. Page height grows with both step count and payload length, so long traces become impractical to inspect.

## Goals

1. Make Session the primary unit in the Trace index: one row per owner-scoped session.
2. Preserve the hierarchy `Session → Trace → Step/content` throughout navigation and breadcrumbs.
3. Keep a long trace navigable in a bounded viewport by showing one selected step at a time.
4. Keep captured content primary while retaining Performance and Raw as secondary diagnostic views.
5. Preserve exact payload access, deep links, owner isolation, escaped rendering, readable typography, and mobile usability.

## Non-goals

- Changing trace ingestion, session IDs, Langfuse storage, identity, retention, or pricing.
- Merging unrelated sessions or inferring sessions when the client did not provide a session ID.
- Generating summaries or chain-of-thought not present in captured evidence.
- Editing, replaying, sharing, deleting, or annotating traces.
- Replacing the administrator Langfuse dashboard.

## Alternatives

### A. Keep trace rows and collapse content cards

This reduces page height but preserves the wrong top-level unit and still forces users to expand many cards to understand a run.

### B. Group by session but keep the vertical content document

This fixes the index hierarchy but not the long-trace failure mode.

### C. Session-first index plus bounded trace inspector — recommended

The Trace destination lists sessions. A session page lists its traces. A trace page uses a compact step rail and one wide selected-step inspector. The document height is bounded by the viewport rather than multiplied by every payload.

## AgentTap Reference Review

The user-provided AgentTap repository was reviewed at commit
`cda0dfd9949788290a0053c7c1ccdf59eef47efd` (Apache-2.0). Its Phase 5 tracing UI uses a
trace list, metrics tree, and selected-span detail as three independently scrollable panes.
The following ideas are adopted conceptually, with an original implementation in this
project:

- independent scrolling regions so long lists and payloads do not grow the whole page;
- compact tree/rail rows carrying type, name, model, duration, token count, and error state;
- persistent selected-step state that drives one detail pane;
- Input / Output / Metadata detail structure;
- sensible per-kind defaults, copyable identifiers, collapsible structure, and explicit
  placeholders for missing captured data;
- deterministic collapse/selection behavior across refreshes.

The following AgentTap choices are deliberately not copied:

- its trace-first left pane, because this product's user journey is session-first;
- its always-visible three-pane trace layout, because removing the trace-list pane after
  entering a session gives captured content materially more width;
- its first-LLM default selection, because ordinary users should first see the root request
  and final response in Overview;
- its visual styling, density, and implementation code.

## Information Architecture

### 1. Trace destination: session index

The primary navigation label remains `Trace`, but its first surface is a session index. Each row represents one authenticated-user session and contains:

- session display name and stable session ID;
- first activity and last activity;
- trace count, total steps, total tokens, total cost, and error count;
- models and tools used;
- first captured request preview and latest captured response preview;
- `Open session` action.

Rows sort by last activity descending. Search matches only the already owner-scoped session projection: session ID/name, trace IDs, captured request/response previews, models, and tools.

Observations without a session ID are grouped into a clearly labelled `Unsessioned traces` bucket rather than mixed with real sessions or dropped.

### 2. Session detail: trace list

The session page becomes a compact trace browser, not a stack of expanded payload cards. It contains:

- a breadcrumb back to all sessions;
- a compact session summary;
- one row per trace, ordered chronologically;
- trace name/ID, status, start time, duration, step count, tokens, cost, request preview, and response preview;
- a direct `Inspect trace` action.

The page does not render full trace payloads. That work belongs to the trace inspector.

### 3. Trace detail: bounded content inspector

The default `Content` view uses two panes under a compact trace header:

```text
Session / Trace breadcrumb
Trace title · status · model · tools · tokens · cost · duration

Steps (fixed rail)             Selected content (wide pane)
────────────────────           ───────────────────────────────────
Overview                  →    Request + final response summary
1 Model response          →    Captured input | Response
2 Tool: search            →    Arguments | Result / error
3 Model response          →    Captured input | Response
```

The step rail is independently scrollable and displays sequence, semantic kind, short name, status, duration, model/tool identity, and error marker. Selecting a step replaces the content pane instead of appending another card to the page.

The initial selection is `Overview`, exposing the root user request and final response. This preserves content-first comprehension before the user drills into an individual observation.

The selected content pane:

- uses a readable maximum line length while retaining most of the horizontal viewport;
- shows input/output side by side on wide screens and as tabs on narrow screens;
- has an independently scrollable payload area bounded to the viewport;
- supports `Wrap lines`, `Copy`, and `Show raw metadata` controls;
- preserves complete escaped payloads without truncating stored data;
- shows an explicit empty state when a field was not captured.

Step links are ordinary owner-scoped URLs using `?step=<observation_id>`, so navigation
still works without JavaScript. JavaScript progressively fetches and swaps a server-rendered
step fragment, updates browser history, and caches already viewed authorized fragments. The
initial HTML contains only compact step descriptors plus the selected content, not every
full payload; long traces therefore remain bounded in both document height and initial DOM
size.

`Performance` retains the waterfall and filters. `Raw` retains the ordered audit payload. Neither is the default.

## Routing and Compatibility

| Method | Route | Result |
|---|---|---|
| GET | `/traces/sessions/` | Canonical owner-scoped session index |
| GET | `/traces/runs/` | Compatibility redirect preserving bounded query parameters |
| GET | `/traces/session/<session_id>/` | Compact trace list for one owner-scoped session |
| GET | `/traces/trace/<trace_id>/?step=<observation_id>` | Trace inspector with optional deep-linked selected step |
| GET | `/traces/trace/<trace_id>/step/<observation_id>/` | Owner-scoped selected-step HTML fragment for progressive navigation |

The `Trace` navigation item links to the canonical session index. Existing trace and session links remain valid. Unknown or foreign session/trace/step identifiers do not expose existence; the server returns the existing owner-safe result.

## Data Projection and Flow

1. The existing server-side Langfuse client fetches observations with forced authenticated `userId` and `include_io=True`.
2. A session projection groups owner-scoped observations by `sessionId`, then traces by `traceId`.
3. Session aggregates are derived only from captured observations.
4. Session detail reuses the per-trace projection without full payload rendering.
5. Trace detail reuses semantic content blocks but emits compact step descriptors plus the complete selected block.
6. The selected step is validated against the already owner-scoped trace observations; it never changes the upstream owner filter.

The initial page renders only Overview or the explicitly selected block. The fragment endpoint
reuses the same owner-scoped trace lookup and semantic projection, returns escaped HTML, and
never accepts a user/account selector. Client-side caching is in-memory and page-scoped only.

## Security and Privacy

- Every index/detail request retains immutable server-side authenticated-user scoping.
- Session, trace, and step query/path values never select another user.
- Foreign and nonexistent identifiers remain indistinguishable.
- Payloads and metadata remain template-escaped.
- Copy controls copy only content already visible and authorized in the page.
- No public link, export, mutation, or cross-account search is introduced.

## Failure Behavior

- Langfuse timeout/failure renders the existing generic unavailable state with no unscoped fallback.
- Missing session IDs go to `Unsessioned traces`.
- Missing root input/output leaves an explicit unavailable field in Overview.
- Unknown observation kinds appear as neutral Events in the step rail and Raw view.
- Invalid `step` values fall back to Overview without revealing whether another trace owns that observation ID.
- With JavaScript unavailable, the server renders Overview and ordinary links for step selection.

## Responsive Behavior

- Desktop: 280–320 px step rail plus a flexible content inspector.
- Tablet: narrower rail with labels truncated, full values available by title.
- Mobile: step rail becomes a horizontal selector or drawer; input/output become tabs; content remains at least 14 px and payload text at least 13 px.
- Page-level scrolling is limited to the shell; the step rail and payload pane own long internal scrolling.

## Testing

- Session index groups multiple trace IDs into exactly one session row.
- Owner isolation excludes foreign sessions, traces, payloads, and search hits.
- Unsessioned observations are grouped deterministically.
- Session aggregates and ordering are correct.
- Session detail lists traces without rendering full payloads.
- Trace detail defaults to Overview and shows only one selected step panel.
- Valid/invalid `step` selection, semantic kinds, errors, missing fields, and unknown observations are covered.
- Performance and Raw remain reachable and correct.
- CSS contracts enforce bounded inspector height, independent scroll regions, and typography floors.
- Browser QA covers a fixture with at least 50 steps and long input/output in desktop/mobile and light/dark themes.

## Acceptance Criteria

1. `/traces/sessions/` renders one row per owner-scoped session, not one row per trace.
2. Opening a session shows a compact trace list and no full payload stack.
3. Opening a trace displays Overview immediately, with a step rail and one selected content pane.
4. Selecting step 40 in a 50-step trace does not require scrolling through steps 1–39.
5. A long input/output does not expand the whole document; it scrolls within the content pane.
6. The selected tool call keeps arguments, result, status, and errors together.
7. Performance and Raw remain available without dominating the default view.
8. Owner isolation, unavailable behavior, full regression, browser console, responsive, and light/dark checks pass.

## Rollout

1. Obtain explicit approval for this design revision.
2. Write an exact test-first implementation plan.
3. Implement locally without commit, push, PR, merge, or deployment.
4. Verify short and 50-step fixtures in the foreground browser.
5. Obtain separate authorization for Git delivery and deployment.

## Decisions Requested

1. Approve option C: session index → compact trace list → bounded two-pane trace inspector.
2. Use `Overview` as the initial selected item, followed by semantic steps.
3. Add canonical `/traces/sessions/` while preserving `/traces/runs/` as a compatibility redirect.
