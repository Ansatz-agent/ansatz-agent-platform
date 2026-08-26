# Personal Analytics Dashboard Implementation Plan

> **Design:** `docs/superpowers/specs/2026-08-26-personal-analytics-dashboard-design.md`

**Goal:** Replace the ordinary user's minimal Trace dashboard with visually faithful, owner-scoped Dashboard and Model Analytics pages backed only by real uploaded observation fields.

**Architecture:** A pure Python projection converts bounded, owner-filtered Langfuse observations into presentation models. Django views parse bounded query state and render two server-side pages sharing an isolated analytics shell. HTML/CSS and inline SVG provide the reference layout and charts; JavaScript is progressive enhancement only.

**Tech stack:** Python 3.10+, Django 5.2, Django templates, scoped CSS, small vanilla JavaScript, SVG, Django TestCase, Ruff, OpenCLI browser.

## Working locations

- Platform docs worktree: `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/platform-personal-analytics`
- Server implementation worktree: `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/worktrees/server-personal-analytics`
- Server branch: `feature/personal-analytics-dashboard` from `main@31a22bf1bf`
- Reference screenshots (scratch only; never commit): `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/dashboard-reference/`

All Python commands below run from the server worktree's `auth-service/` directory. Use `rtk proxy python` only when the inherited interpreter is already the Conda `dl` interpreter; otherwise use `rtk proxy conda run -n dl python`.

## Task 1: Build the observation analytics projection

**Files:**

- Create `auth-service/history/trace_analytics.py`
- Create `auth-service/history/tests/test_trace_analytics.py`
- Modify `auth-service/history/tests/test_trace_dashboard.py`

### Step 1: Write one failing behavior test

Add a fixture containing root spans, tool spans, two generation models, cached input, reasoning output, missing pricing, explicit zero pricing, latency, and errors. Assert that:

- only generation rows contribute model token/cost totals;
- cached and reasoning subsets are not double-counted in total tokens;
- duplicated provider prefixes normalize to one model;
- missing price remains unavailable while explicit zero is valid;
- daily, model, session, cache, latency, and error aggregates reconcile exactly.

Run:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_analytics -v 2
```

Expected: FAIL because `history.trace_analytics` does not exist.

### Step 2: Implement the minimum pure projection

Implement typed presentation dictionaries/dataclasses, safe numeric parsing, field-presence-aware cost parsing, model normalization, time bucketing, percentile calculation, chart geometry, compact formatting, deterministic insights, and `build_dashboard()` / `build_model_analytics()`.

Expected public interfaces:

```python
build_dashboard(items, *, days, now, query, metric, granularity, chart) -> dict
build_model_analytics(items, *, days, now, model, metric, granularity, chart) -> dict
parse_analytics_query(querydict, *, page) -> AnalyticsQuery
```

### Step 3: Run GREEN and refactor

Run the focused test command again.

Expected: PASS with exact aggregate assertions. Then move the legacy `aggregate_dashboard` behavior onto the new projection or delete it only after its view tests are updated and green.

## Task 2: Add bounded Model Analytics routing and secure view contracts

**Files:**

- Modify `auth-service/config/urls.py`
- Modify `auth-service/history/trace_views.py`
- Modify `auth-service/history/tests/test_trace_dashboard.py`
- Modify `auth-service/history/langfuse_client.py` only if a field required by the approved mapping is not currently requested

### Step 1: Write one failing view test

Assert that `GET /traces/models/`:

- requires the Hermes session;
- sends only `request.user.pk` to Langfuse;
- filters a forged foreign observation locally;
- bounds invalid days, metric, granularity, chart, model, and long search values;
- uses one observation fetch without IO/metadata;
- renders a generic 503 without upstream details.

Run:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_dashboard.TraceDashboardViewTests -v 2
```

Expected: FAIL because `trace-model-analytics` and its view do not exist.

### Step 2: Implement the minimum route/view change

Add named route `trace-model-analytics`. Share query parsing and observation loading between both views. Keep `_owned()` defense in depth. Ensure templates receive a complete empty projection during 503 rendering so the shared shell remains stable.

### Step 3: Run GREEN

Run the focused view test command.

Expected: PASS; existing detail and owner-isolation tests remain green.

## Task 3: Implement the shared pixel-fidelity shell and Dashboard

**Files:**

- Create `auth-service/templates/traces/analytics_base.html`
- Replace `auth-service/templates/traces/dashboard.html`
- Create focused partials under `auth-service/templates/traces/analytics/`
- Create `auth-service/history/static/history/trace_analytics.css`
- Create `auth-service/history/static/history/trace_analytics.js`
- Modify `auth-service/history/tests/test_trace_dashboard.py`
- Modify `auth-service/history/tests/test_security_headers.py` only if its exact asset contract requires the new scoped assets

### Step 1: Write one failing Dashboard contract test

Assert semantic/accessibility inventory and real values for:

- shared header, Dashboard/Model Analytics tabs, date control, user/logout/refresh controls;
- filter rail and four KPI cards;
- summary banner, Usage Trend, Cost Mix, Token Mix, Daily Activity, Top Models, Recent Sessions;
- links to existing session/trace details;
- no input/output/tool payload content;
- empty and unavailable states retaining the shell.

Run:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_dashboard -v 2
```

Expected: FAIL on missing component labels and assets.

### Step 2: Implement the Dashboard markup and scoped design system

- Reproduce the approved 48 px bar, 280 px rail, desktop gutters, panel geometry, control density, neutral canvas, white cards, green accent, type scale, borders, and shadows.
- Use inline SVG/semantic HTML for trend, donuts, heatmap, and ranked model marks.
- Preserve full accessible summaries and table values.
- Implement theme and collapse behavior as progressive enhancement; no metric/date navigation may depend on JavaScript.
- At narrow widths, stack filters and panels while limiting horizontal scrolling to dense panel interiors.

### Step 3: Run GREEN and static checks

Run:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_dashboard -v 2
rtk proxy conda run -n dl python manage.py check
```

Expected: both exit 0.

## Task 4: Implement the full Model Analytics page

**Files:**

- Create `auth-service/templates/traces/model_analytics.html`
- Create/modify partials under `auth-service/templates/traces/analytics/`
- Modify `auth-service/history/static/history/trace_analytics.css`
- Modify `auth-service/history/tests/test_trace_dashboard.py`
- Modify `auth-service/history/tests/test_trace_analytics.py`

### Step 1: Write one failing page behavior test

Assert the page renders and reconciles:

- selected-scope cost, tokens, cache hit rate, and top-model share;
- distribution controls and marks;
- Token Composition and Cache Split by Model;
- Effective-cost scatterplot excluding models without usable price evidence;
- evidence-gated What Changed signals;
- complete Model Breakdown columns;
- Recent Model Calls linked only to owned trace IDs;
- selected model and metric state in URLs/forms.

Run:

```bash
rtk proxy conda run -n dl python manage.py test history.tests.test_trace_analytics history.tests.test_trace_dashboard -v 2
```

Expected: FAIL because the template/sections are incomplete.

### Step 2: Implement the minimum complete page

Build every approved section using the shared shell. Keep missing values explicit. Normalize only presentation grouping; never rewrite stored observation data. Use bounded model names in links and retain owner-scoped detail navigation.

### Step 3: Run GREEN and refactor shared partials

Run the focused tests again.

Expected: PASS. Remove duplicated panel/control markup only while focused tests stay green.

## Task 5: Validate visual fidelity and responsive behavior

**Files:**

- Modify `auth-service/history/static/history/trace_analytics.css`
- Modify templates only to fix observed layout/accessibility defects
- Store generated diagnostics only under `/Users/yuxiaoy/Projects/Ansatz-agent/tmp/dashboard-visual-qa/`

### Step 1: Generate deterministic populated and empty HTML

Use Django's test client with the representative observation fixture and patched Langfuse client to render both pages. Save diagnostic HTML under the workspace scratch directory with static asset paths resolved to the worktree. Do not commit rendered HTML or private reference images.

Expected: HTTP 200 for populated/empty Dashboard and Model Analytics fixtures.

### Step 2: Capture target viewports

Open the generated pages in the owned OpenCLI browser session and capture:

- 1728×906;
- 1440×900;
- 390×844.

Compare against `/tmp/dashboard-reference` equivalents using the design's normalized geometry criteria. Check header/sidebar/panel bounds, text scale, controls, internal scrolling, focus, light/dark theme, and empty state.

Expected: desktop geometry within 4 px for named structural boundaries and typography within 1 px by role; no page-level overflow at 390 px.

### Step 3: Iterate and recapture

Fix only evidence-backed layout defects, rerun focused tests after each CSS/template change, and retain the final local screenshots as untracked verification evidence.

## Task 6: Full verification and review

**Files:** all changed server and platform files

### Step 1: Run formatting/lint checks

```bash
ruff check auth-service/history/trace_analytics.py auth-service/history/trace_views.py auth-service/history/tests/test_trace_analytics.py auth-service/history/tests/test_trace_dashboard.py
```

Expected: exit 0. If `ruff` is not on PATH, invoke the already available project tool without installing or changing an environment.

### Step 2: Run the complete service proof

```bash
rtk proxy conda run -n dl python manage.py test -v 2
rtk proxy conda run -n dl python manage.py check --deploy
git diff --check
```

Expected: every command exits 0; inspect full outputs and exact test counts.

### Step 3: Review the branch diff

Review against `main` for owner isolation, XSS/query handling, numeric correctness, double-counting, sparse data, information disclosure, accessibility, responsive overflow, and unrelated regressions. Address every high/medium finding and rerun the affected proof commands.

### Step 4: Delivery readiness

Record changed files, proof output, visual evidence paths, and any non-blocking limitations. Do not commit, push, open a PR, merge, deploy, or delete worktrees unless the user explicitly requests that remote/destructive action.
