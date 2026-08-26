# Personal Analytics Dashboard Design

**Date:** 2026-08-26

**Status:** Approved for implementation

**Classification:** Architectural

**Repositories:** `ansatz-agent-platform`, `agent-langfuse-server`

## 1. Goals

- Replace the current minimal `/traces/` page with a dense, production-quality personal analytics experience modeled on the information architecture and visual rhythm of the NVIDIA Tokenomics Dashboard.
- Deliver two first-class views: **Dashboard** and **Model Analytics**.
- Calculate every displayed value from the authenticated user's actual Langfuse observations uploaded by the Ansatz client.
- Preserve the existing owner-only security boundary and generic fail-closed behavior.
- Keep recent sessions and trace details discoverable from the analytics experience.
- Make absent usage, pricing, cache, or latency fields visibly unavailable instead of inventing zero-valued evidence.

## 2. Non-goals

- Copying NVIDIA trademarks, logos, profile photography, employee/department data, proprietary wording, or internal tool names.
- Adding organization-wide or cross-user analytics to the ordinary-user surface.
- Changing the OTLP upload schema, Gateway identity canonicalization, Langfuse pricing, or retention policy.
- Replacing the Langfuse administrator interface.
- Persisting a second analytics database or caching private user aggregates in Django.

## 3. Reference and visual direction

The authenticated reference was captured on 2026-08-26 at 1728×906 and 1440×900 for both pages. It establishes:

- a 48 px white application bar with brand, primary tabs, date selector, theme control, user identity, and refresh action;
- a 280 px sticky filter rail containing compact controls and stacked KPI cards;
- a pale neutral/green canvas, white panels, subtle one-pixel borders, 10–12 px radii, restrained shadows, and a bright green accent;
- compact chart headers with segmented metric and chart controls;
- a large primary trend/distribution panel, two- and three-column secondary panels, and dense horizontally scrollable tables;
- strong information hierarchy through 12–14 px UI text, 18–24 px headings, and large tabular KPI numerals.

The Ansatz implementation uses its own wordmark and terminology. At 1728×906 and 1440×900, normalized layout geometry must match the reference within 4 CSS px for the application bar, sidebar, outer gutters, panel boundaries, and control heights; typography must be within 1 CSS px of the corresponding reference role. Data-dependent chart marks and text lengths are excluded from geometric comparison. At 390×844, navigation, filters, charts, and tables must remain operable without page-level horizontal overflow; dense chart/table content may scroll inside its panel.

## 4. User experience

### 4.1 Shared shell

- The top bar identifies **Ansatz Analytics**, switches between Dashboard and Model Analytics, shows the selected date range, exposes a light/dark theme toggle, shows the signed-in username, and provides refresh/logout actions.
- The filter rail can collapse on desktop and becomes a compact filter drawer/stack on narrow screens.
- Date ranges remain bounded to 7, 30, or 90 days. The server determines current user identity; no browser-supplied identity is accepted.
- Query parameters preserve shareable view state: range, metric, granularity, chart type, model, and search.
- All interactive elements have visible focus states and accessible labels. Charts include textual summaries or tables.

### 4.2 Dashboard

The Dashboard contains:

1. KPI rail: total cost, daily average cost, total tokens, active days.
2. Personal summary banner: selected-range totals, active-day rate, highest-cost model, and error signal.
3. Usage Trend: daily/weekly buckets; cost, tokens, or unit cost; bar or line presentation.
4. Cost Mix: attributed model cost and share.
5. Token Mix: input, cached input, output, and reasoning-output composition.
6. Daily Activity: one intensity cell per selected day using the chosen primary metric.
7. Top Models: ranked by cost, tokens, or unit cost with direct Model Analytics links.
8. Recent Sessions: session name/ID, traces, tokens, cost, errors, last activity, and a detail link.

Search filters only the Recent Sessions section and never changes aggregate KPI or chart totals. Empty accounts retain the complete shell and explain how data appears after an authenticated client upload.

### 4.3 Model Analytics

The Model Analytics page contains:

1. KPI rail: selected-scope cost, tokens, cache hit rate, and top-model share.
2. Model distribution ranked by cost, tokens, or unit cost, with distribution and time-series variants.
3. Token composition for cached input, uncached input, output, and reasoning output.
4. Cache split by model.
5. Effective-cost scatterplot: total tokens on x, unit cost on y, spend represented by bubble size; models without usable price evidence appear in the table but not the scatterplot.
6. What Changed: deterministic concentration, seven-day movement, cache efficiency, error, and daily anomaly signals. Signals are omitted when evidence is insufficient.
7. Model breakdown table: calls, input/output/cache/reasoning tokens, total tokens, cost, unit cost, cost share, cache rate, average latency, p95 latency, and errors.
8. Recent Model Calls: timestamp, provider/span name, model, session, tokens, cost, latency, and status, linking to the owning trace.

A model selector narrows sections 2–8 while distribution continues to make the selected model's position understandable. When model names differ only by the known duplicated provider prefix (for example `openai/openai/gpt-5.5`), presentation normalizes them to the canonical alias while retaining the original value only in internal aggregation input.

## 5. Authoritative data mapping

Production schema inspection found 284 current observations comprising `SPAN` and `GENERATION` rows. Billable usage appears on generation rows. Available usage/cost buckets are `input`, `input_cached_tokens`, `output`, `output_reasoning_tokens`, and `total`; latency and error level are also populated.

| Analytics concept | Langfuse observation source | Rule |
| --- | --- | --- |
| Owner | `userId` | Must equal the authenticated Django username after both upstream and local filtering, matching the production Gateway's canonical Langfuse projection. |
| Session / trace | `sessionId`, `traceId`, `traceName` | Missing session falls back to a stable per-trace key. |
| Time / active day | `startTime`, `endTime` | Invalid timestamps are excluded from time charts and retained only where safely displayable. |
| Model | `providedModelName`, then `model`, then `modelId` | Normalize duplicated adjacent provider prefix for display grouping. |
| Provider/source | generation `name` | Display as the observed provider/span name; never infer NVIDIA-style tools. |
| Input tokens | `usageDetails.input`, fallback `inputUsage` | Non-negative finite numeric values only. |
| Cached input | `usageDetails.input_cached_tokens` | Counted as part of input and not added twice to `total`. |
| Output tokens | `usageDetails.output`, fallback `outputUsage` | Non-negative finite numeric values only. |
| Reasoning output | `usageDetails.output_reasoning_tokens` | A composition subset; not added twice when `total` already includes it. |
| Total tokens | `usageDetails.total`, fallback `totalUsage`, then input + output | One total per generation observation. |
| Cost | `totalCost`, then `costDetails.total`, then bucket sum | One total per generation observation. |
| Latency | `latency`, fallback parsed `endTime - startTime` | Clamp invalid or negative durations to unavailable. |
| Error | `level == ERROR` or non-empty error `statusMessage` | Count at observation and session level. |
| Tool activity | non-generation observation `name` | Used for session context and counts, never included in model cost. |

Only `GENERATION` observations contribute model token/cost analytics. This prevents duplicated wrapper spans from inflating totals. Dashboard session/trace/error counts may use all observation types.

Cache hit rate is `cached_input / input` when input is positive. Unit cost is `cost / total_tokens × 1,000,000` only when both cost evidence and positive tokens exist. A zero cost explicitly returned by Langfuse is valid; a missing cost field is unavailable and is not silently treated as priced at zero.

## 6. Architecture and interfaces

### 6.1 Read path

```text
authenticated request
  -> bounded query parser
  -> LangfuseClient.list_observations(user_id=current user, range)
  -> local owner filter
  -> pure analytics projection
  -> server-rendered Django template + inline SVG/CSS charts
```

- `history/trace_analytics.py` owns parsing, canonical model naming, safe numeric helpers, time bucketing, aggregate models, deterministic insights, and presentation-ready chart geometry.
- `history/trace_views.py` owns authentication, bounded query state, one Langfuse read per page, owner revalidation, response status, and template selection.
- `templates/traces/_analytics_shell.html` and focused partials own the shared shell and panels. They receive presentation-ready values and never perform arithmetic.
- `history/static/history/trace_analytics.css` scopes the new visual system under `.analytics-app` so the login, historical session pages, and administrator surfaces do not regress.
- `history/static/history/trace_analytics.js` contains progressive enhancements only: theme persistence, filter rail collapse, and client-side panel collapse. Navigation and metric selection work without JavaScript.
- `templates/traces/trace_detail.html` and `session_detail.html` keep their existing owner-only contracts; links from analytics use existing named URLs.

No new external frontend package or CDN is introduced. Charts use semantic HTML and inline SVG generated from bounded aggregate arrays.

### 6.2 URL contract

- `GET /traces/` — Dashboard.
- `GET /traces/models/` — Model Analytics.
- Accepted shared parameters: `days=7|30|90`, `metric=cost|tokens|unit_cost`, `granularity=day|week`, `chart=bar|line`, `q=<max 80 chars>`.
- Model Analytics additionally accepts `model=<known model name or all>`.
- Unknown values fall back to documented defaults and never reach a backend filter unsafely.

## 7. Security and privacy

- Both pages remain protected by `hermes_session_required`.
- Langfuse calls always use `request.user.get_username()` as `userId`, matching the trusted value
  emitted by the production Gateway; browser identity parameters are ignored. The immutable account
  UUID and numeric Django user ID remain trusted Trace metadata rather than Langfuse's display/query key.
- Returned observations are filtered again by exact owner before aggregation.
- Dashboard queries omit `io` and `metadata`; no prompt, answer, tool argument, or result is embedded in analytics HTML.
- All display strings are escaped by Django templates. Query strings and model names are bounded before use.
- CSV export is deferred; no new bulk-data disclosure endpoint is introduced in this change.
- Backend failures return a generic service-unavailable presentation and never include credentials, upstream URLs, exception text, or another user's data.

## 8. Failure and sparse-data behavior

- Langfuse timeout, malformed response, pagination overflow, or non-200 response produces HTTP 503 with the shared analytics shell and a generic retry message.
- Empty range produces zero count totals, unavailable price/ratio fields, empty chart states, and an upload guidance message.
- Missing model names group under `Unknown model` only for generation counts; they do not disappear from totals.
- Missing token/cost/latency buckets show an em dash and an explanatory tooltip where useful.
- A single data point never claims a trend or anomaly. Seven-day movement requires non-empty current and previous windows. Daily anomaly requires at least seven active daily values.
- Very large values use deterministic compact formatting while full values remain in accessible labels/table cells.

## 9. Testing

- Pure aggregation tests cover bucket semantics, no double-counting, model normalization, price evidence, latency, errors, sparse values, daily/weekly grouping, anomaly gates, and owner-independent deterministic output.
- View tests cover authentication, owner scoping, query bounds, empty state, 503 state, both routes, links, accessible labels, and absence of input/output content.
- Template contract tests cover the full panel inventory and scoped asset loading.
- Existing trace detail/session detail tests remain green.
- Screenshot verification uses representative populated and empty fixtures at 1728×906, 1440×900, and 390×844. Geometry is compared against the captured reference criteria in section 3; visual review also checks light/dark contrast, focus visibility, internal overflow, and no clipped controls.
- Full auth-service test suite and Django system checks run before completion.

## 10. Acceptance criteria

1. An authenticated user can switch between complete Dashboard and Model Analytics pages without leaving `/traces` ownership scope.
2. At desktop sizes, shell geometry, density, component hierarchy, control styling, and chart/table layout meet the pixel-fidelity tolerances in section 3 while carrying Ansatz branding.
3. All metrics reconcile with fixture observations according to section 5; cached/reasoning buckets and wrapper spans are not double-counted.
4. Production field variants observed on 2026-08-26 are supported.
5. Empty, partially populated, zero-cost, and unavailable states communicate evidence accurately.
6. No analytics response includes prompt/response/tool payloads or foreign-user identifiers.
7. Narrow-screen operation has no page-level horizontal overflow and preserves access to filters and table/chart content.
8. Existing authentication, session detail, trace detail, and security tests pass.

## 11. Rollout

1. Implement and verify on feature branches/worktrees in both repositories.
2. Review screenshots locally with synthetic representative data; never commit the private NVIDIA reference screenshots.
3. Merge through reviewed pull requests only when explicitly authorized.
4. Deploy auth-service only after merge, preserving current environment and Langfuse credentials.
5. Run authenticated smoke tests using one populated user and one empty user, then confirm owner isolation and container health.
6. Roll back by redeploying the previous auth-service image; no schema migration or stored data change is involved.

## 12. Resolved decisions

- The visual reference guides layout and interaction, but Ansatz branding and real Hermes categories replace NVIDIA identity and internal taxonomy.
- Personal Model Analytics replaces the reference's cross-employee table with Recent Model Calls because ordinary users cannot and should not inspect other users.
- Server rendering with inline SVG is preferred over a new JavaScript chart dependency to preserve the lightweight auth-service boundary.
- Export is not added in this change because it expands the private-data download surface and is not required to deliver Dashboard and Model Analytics.

## 13. Unresolved decisions

None block implementation. Future work may add a separately reviewed, owner-scoped CSV export and organization-level administrator analytics.
