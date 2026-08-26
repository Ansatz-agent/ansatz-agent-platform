# Content-first Agent Trace Explorer Design

**Status:** Approved by user on 2026-08-26
**Date:** 2026-08-26
**Classification:** Architectural
**Scope:** Ordinary-user `/traces` experience in `agent-langfuse-server/auth-service`

## Goals

1. Add a first-class `Trace` destination beside `Dashboard` and `Model Analytics`.
2. Make captured agent content—not latency—the primary reading experience.
3. Let a user understand a run in order: user request, model/agent response, tool call, tool result, and final response.
4. Preserve a separate performance view for latency diagnosis without letting it dominate the default view.
5. Raise list, table, payload, label, and navigation typography to a readable product scale.
6. Preserve strict owner-scoped access and complete escaped payload access.

## Non-goals

- Replacing the administrator-only Langfuse dashboard.
- Editing, replaying, deleting, annotating, or sharing traces.
- Inventing chain-of-thought or reasoning text that was not captured.
- Adding new trace fields to the client or Gateway in this phase.
- Performing semantic summarization with another model.
- Changing trace ingestion, identity, pricing, or retention.

## Design Alternatives

### A. Improve the existing waterfall

Keep the three-column execution timeline as the default and enlarge expandable payloads. This is the smallest change, but latency remains the visual owner and content remains hidden behind disclosure controls.

### B. Three-pane inspector

Use a step tree, selected-step content inspector, and metadata sidebar. This is efficient for expert debugging, but the narrow content pane and persistent inspector make long prompts, responses, and tool results harder to read.

### C. Content-first run narrative — recommended

Use a compact run outline beside a wide chronological content document. Put prompt, response, tool arguments, tool result, and final answer directly in the reading flow. Keep duration as muted trailing metadata and move the waterfall to a separate `Performance` view.

Option C best matches the product's ordinary-user audience and the user's explicit priority: first understand what the agent did, then inspect how long it took.

## Requirements

- Primary navigation is `Dashboard`, `Model Analytics`, `Trace`.
- `Trace` opens a user-owned trace index at `/traces/runs/`.
- Existing trace details remain at `/traces/trace/<trace_id>/`.
- The trace detail default view is `Content`; secondary views are `Performance` and `Raw`.
- The first desktop viewport exposes the user request or first captured content block without requiring an observation expansion.
- Tool calls present tool name, arguments, result, status, and error evidence as one semantic unit.
- LLM observations present model identity and captured input/output as readable content cards.
- Duration is secondary metadata in `Content`; the full waterfall appears only in `Performance`.
- Raw payloads remain available and escaped.
- Ordinary body text is at least 14 px; secondary labels are at least 12 px; payload text is at least 13 px.
- Exact token counts remain available by title/detail while aggregate surfaces may use K/M/B notation.

## Information Architecture

### Trace index

The index is a dedicated browsing surface rather than a copy of Recent Sessions.

Each trace row/card shows:

- trace or conversation title;
- start time and session;
- user-request preview when captured;
- final-response preview when captured;
- status, model names, tool count, token count, and cost;
- a direct `Open trace` action.

Filters cover date range, status, model, tool presence, session/trace ID search, and free-text search over already-fetched display fields. Latency is not a default column or sort owner.

### Trace detail: Content

```text
Trace title · session · status · started time
Steps · models · tools · tokens · cost                     Duration (muted)

Run outline              Content document
────────────              ─────────────────────────────────────────
User request        →     USER REQUEST
Model response      →     readable captured prompt/response
Tool: search        →     TOOL CALL · arguments
Tool result         →     TOOL RESULT · output/error
Final response      →     FINAL RESPONSE
```

The outline is sticky on wide screens and collapses into a horizontal step navigator on smaller screens. Selecting an outline item scrolls to the corresponding content card. Content cards use semantic headings and readable text/JSON rendering. Long payloads have bounded preview plus `Show complete content`; they are not globally collapsed by default.

### Trace detail: Performance

This view owns the waterfall, duration, start/end timestamps, concurrency, slow-step ordering, per-step tokens, and per-step cost. It reuses the current deterministic timing calculations. It is available in one click but is not the initial tab.

### Trace detail: Raw

This view preserves ordered observations, IDs, types, input, output, metadata, and timestamps for audit/debug use. It does not compete with the default reading hierarchy.

## Architecture

- `app_shell.html` adds the `Trace` primary-navigation item and a dedicated active-state block.
- A new server-rendered trace-index template consumes owner-scoped observation projections.
- `trace_views.py` adds a trace-index projection and evolves trace detail into semantic content blocks plus existing performance geometry.
- `trace_detail.html` renders the default content document, secondary performance view, and raw view.
- Existing CSS/JavaScript remain framework-free; JavaScript handles view switching, outline navigation, filtering, and bounded content expansion.
- The existing Dashboard Recent Sessions table remains a summary entry point and links into the same trace/session routes.

## Interfaces and Data Flow

| Method | Route | Purpose |
|---|---|---|
| GET | `/traces/runs/` | Owner-scoped trace index with bounded date/filter query parameters |
| GET | `/traces/trace/<trace_id>/` | Content-first trace detail |
| GET | `/traces/session/<session_id>/` | Existing session detail and links to traces |

All routes call the existing server-side Langfuse client with forced `userId=str(request.user.pk)`. Detail access retains the second local ownership check and returns 404 for absent or foreign identifiers.

The semantic projection maps only captured evidence:

- root input → `user_request` when present;
- generation input/output → `model_exchange`;
- tool input/output → one `tool_exchange` block;
- root output → `final_response` when present;
- unclassified spans → `event` with captured fields;
- timing and usage → secondary metadata and the Performance projection.

No generated summary or inferred reasoning is introduced.

## Security and Privacy

- Authorization remains immutable user-ID based; query parameters never select another owner.
- Project API credentials remain server-side.
- All text, JSON, tool arguments, results, and metadata remain template-escaped.
- Foreign and nonexistent traces both return 404.
- Search is bounded to the authenticated user's fetched result set and accepted query limits.
- Complete trace content is intentionally sensitive; the feature adds no public links, exports, or mutation actions.

## Failure Behavior

- Langfuse timeout or upstream failure renders the existing retryable unavailable state without an unscoped fallback.
- Missing root input/output omits that content block and clearly labels the remaining captured evidence.
- Malformed structured payloads render as escaped text rather than breaking the page.
- Unknown observation types render as neutral events and remain available in Raw.
- JavaScript failure leaves server-rendered Content readable and Raw reachable through ordinary links or visible fallback controls.

## Testing

- Write one failing behavior test before each route, projection, template, interaction, or typography change.
- Test the third primary-navigation item and its active state on index/detail pages.
- Test owner isolation and 404 equivalence on the new index/detail paths.
- Test semantic ordering of user, model, tool, result, and final-response blocks.
- Test absent input/output, malformed payloads, unknown observation kinds, and error observations.
- Test that Content is the default and Performance contains the waterfall.
- Test typography floors and responsive outline behavior.
- Browser QA covers desktop/mobile, light/dark modes, long text, long JSON, tool errors, and traces with many observations.
- Full Django, JavaScript syntax, system, and diff verification remains required.

## Acceptance Criteria

1. `Dashboard`, `Model Analytics`, and `Trace` are visible primary destinations, with correct active states.
2. `Trace` lists only the signed-in user's traces and supports bounded search/filtering.
3. Opening a trace shows captured content in chronological semantic order without expanding timeline rows.
4. User request, tool arguments/result, model response, and final response are visually distinguishable and readable.
5. Latency does not own the default layout; duration is muted metadata and the waterfall is in `Performance`.
6. The default desktop viewport includes actual trace content, not only KPI cards and timing visualization.
7. Body, secondary, and payload typography meet the 14/12/13 px floors.
8. Recent Sessions is readable at normal desktop scale and continues to link to exact session/trace details.
9. Owner-isolation, failure-state, full test, browser QA, and dark/light checks pass.

## Rollout

1. Approve this design specification.
2. Write the exact test-first implementation plan under `docs/superpowers/plans/`.
3. Implement locally on the current feature branch with no production mutation.
4. Verify fixture traces in light/dark and desktop/mobile modes.
5. Obtain separate authorization for commit, push, PR, merge, and deployment.

## Unresolved Decisions

1. Navigation label: `Trace` (requested wording) versus conventional plural `Traces`.
2. Trace index presentation: full-width rows versus compact cards. The recommendation is full-width rows because content previews compare better vertically.
3. Long content default: show approximately 12 lines before expansion versus fully expanded. The recommendation is a 12-line preview with explicit expansion to keep long tool results navigable.
