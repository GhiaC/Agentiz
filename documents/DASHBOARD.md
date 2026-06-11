# Agentize Dashboards — Developer Guide

A simple, practical guide to Agentize's built-in web dashboards, written so an
AI assistant (or a new developer) can understand and improve them quickly.

Agentize serves three kinds of HTML dashboard (wired in [`routes.go`](../routes.go)):

| Route | Served by | Source of data | What it shows |
|-------|-----------|----------------|----------------|
| `/agentize/docs` | **this package** (`documents/`) | knowledge tree (filesystem nodes) | Interactive browser of nodes, tools, auth |
| `/agentize/graph` | `visualize/` (go-echarts) | knowledge tree | ECharts force-graph of the node tree |
| `/agentize/debug/*` | `debuger/` + `debuger/pages` | **database** (DebugStore) | Sessions, messages, tool-calls, users, plans, summaries, files |

This document focuses on the **`/agentize/docs` Knowledge dashboard** (the
`documents/` package), then points to the others and lists concrete improvements.

---

## 1. The Knowledge dashboard (`/agentize/docs`)

A single, self-contained HTML page. All data is embedded inline as JavaScript —
there are **no XHR/API calls**; the page is rendered once from a snapshot of the
knowledge tree.

### 1.1 Data pipeline

```
model.Node map ──► documents.NewAgentizeDocument(nodes, getChildren)
                     │   builds NodeDocument{} per node + a TreeNode{} tree
                     ▼
   GenerateHTMLWithRegisteredTools(registeredTools)
                     │   json.Marshal(tree)   → treeData
                     │   json.Marshal(nodes)  → nodesData
                     │   json.Marshal(set)    → registeredTools
                     ▼
   components.Page(...).Render(ctx)   (templ)
                     │   embeds the 3 JS blobs into a <script>
                     ▼
                HTML string ──► HTTP response
```

Entry points: [`handleDocs`](../routes.go) → [`documents.go`](./documents.go)
`GenerateHTMLWithRegisteredTools`.

### 1.2 What the browser receives

Three global JS objects are injected:

- `treeData` — the hierarchy: `{path, id, title, description, level, children[]}`
  (children are nested `TreeNode`s). Used to render the tree and compute stats.
- `nodesData` — a flat map `path → NodeDocument`: `{path, id, title, description,
  content, children[] (paths), tools[], auth}`. The source of truth for details.
- `registeredTools` — a set `{toolName: true}` of tools that actually have a Go
  function registered in the `FunctionRegistry`. Used for the ✓/⚠️ badge.

### 1.3 The three views (all logic in [`components/scripts.templ`](./components/scripts.templ))

1. **Tree view** (`renderTree` / `renderNode`) — indented, collapsible node tree.
   Per node: level badge, title (link to detail), path, description, tool count,
   an expandable Tools list (with registered badge), and a Markdown **Content
   Preview** (rendered with `marked.js`, toggle to plain text, copy button).
2. **Detail view** (`showNodeDetail`) — one node in full: path, id, description,
   rendered content, **Auth** (per-user permission grid), children (clickable),
   and tools with description, status and pretty-printed `input_schema`.
3. **Tools view** (`showAllTools` / `renderAllTools`) — every tool across the tree,
   **deduplicated by name**, sorted, with status, registered badge, input schema,
   and the list of nodes that expose it.

Plus a **Stats** bar (`calculateStats`: total nodes, total tools, tree depth) and a
client-side **Search** box (`setupSearch`: filters nodes by title/description/path/
tool text and auto-expands matches). Navigation is hash-based
(`#tools`, `#node-<path>`) so views are linkable and survive reload.

### 1.4 Rendering internals & gotchas

- **templ codegen.** Markup lives in `components/*.templ`; the committed
  `*_templ.go` files are **generated** — never hand-edit them. After changing a
  `.templ` you must run `templ generate` (see [`components/INSTALL.md`](./components/INSTALL.md)).
- **Placeholder injection is fragile.** `GenerateHTMLWithRegisteredTools` passes the
  three `const …` JS strings into `Page(...)` *and* then does
  `bytes.ReplaceAll("{ treeData }", …)` on the output. Keep the literal tokens
  `{ treeData }`, `{ nodesData }`, `{ registeredTools }` in `scripts.templ` in sync
  with that replace logic, or the data will be missing.
- **Everything is inlined.** The full knowledge base — including every node's full
  Markdown `content` — is serialized into the page. Big trees ⇒ big pages.
- **XSS.** Text goes through `escapeHtml`, but node `content` is rendered as HTML via
  `marked.parse`. Treat knowledge content as trusted input.
- **No per-user filtering.** `/agentize/docs` renders the whole tree regardless of
  the `auth` rules it displays; auth here is informational, not enforced.

---

## 2. The Graph dashboard (`/agentize/graph`)

`handleGraph` calls `GenerateGraphVisualization` (in `visualize/`), which uses
**go-echarts** to write an ECharts force-graph of the tree to a temp file, then
swaps the ECharts asset URL for a CDN one and serves it. It is a separate renderer
from this package and shows structure only (no tools/auth detail).

> Note: a large bundled `documents/main-echart.js` ECharts asset used to live here
> but was **removed** — it was dead weight (~106 KB, referenced nowhere; the graph
> page loads ECharts from a CDN in `routes.go`). The `/agentize/docs` page loads
> only `marked` from a CDN and builds plain HTML.

## 3. The Debug dashboards (`/agentize/debug/*`)

These are the **data-driven** dashboards, backed by the `DebugStore` interface
(implemented by the SQLite/Mongo session stores). They expose operational data:
sessions, messages, tool-calls (with input/output), users, summarization logs and
opened files. This is where runtime/usage data already lives — important for the
improvements below.

> **Plans are the exception:** `/agentize/debug/plans` reads from the planning
> `PlanStore` (in-memory `MemoryStore` by default), **not** the `DebugStore` — so it is
> empty after a restart unless a durable `Persister` is wired. The detail page shows the
> plan's steps with their dependencies (DAG edges), per-step status/duration/tokens, and
> failure reasons. See [PLANNING.md](../docs/PLANNING.md).

---

## 4. Data model: what exists vs. what is shown

| Field (`model.Node`) | In `nodesData`? | Shown in UI? | Notes |
|----------------------|-----------------|--------------|-------|
| Path, ID, Title, Description | yes | yes | — |
| Content (Markdown) | yes | yes | rendered with marked.js |
| Children | yes (paths) | yes | — |
| Tools (name, desc, schema, status) | yes | yes | + registered badge |
| Auth.**Users** | yes | yes | permission grid |
| Auth.**Groups** | **no** | **no** | dropped in `NewAgentizeDocument` |
| Auth.**Roles** | **no** | **no** | dropped in `NewAgentizeDocument` |
| Hash | no | no | available, never surfaced |
| LoadedAt | no | no | available, never surfaced |

---

## 5. Review findings

Fixed in this iteration ✅
1. ~~**Dead UI branch.**~~ The `nodeData.policy.*` badges (which never rendered
   because `NodeDocument` has no `policy` field) were removed; the "Children: N"
   badge now reads `nodeData.children` directly.
2. ~~**Auth half-shown.**~~ `Auth.Groups`, `Auth.Roles` and `Inherit` are now
   converted in `NewAgentizeDocument` and rendered in the detail view's Auth grid
   alongside Users.
3. ~~**No freshness/identity info.**~~ `Hash` (short) and `LoadedAt` are now on
   `NodeDocument` and shown in the detail header. A **Registered Tools** stat
   (`registered / unique`) was added to the stats bar.

Still open
4. **No usage/runtime data.** The dashboard is still purely structural. It cannot
   answer "which tools/nodes are actually used?" even though the DB knows (see §6.B).
5. **Page weight.** Full content is inlined for every node; no lazy loading.
6. **No per-user auth filtering** at `/agentize/docs` (auth is shown, not enforced).

---

## 6. Improvement roadmap

### A. Surface data that already exists (cheap, high value)
- ✅ Done: `Auth.Groups` / `Auth.Roles` / `Inherit` now shown; `Hash` + `LoadedAt`
  shown; **Registered Tools** coverage stat added; dead `policy` badges removed.
- ✅ Done: the Tools view now has an **All / ✓ Registered / ⚠️ Unregistered** filter
  (with live counts) so defined-but-unregistered tools (likely dead/mis-typed) are
  one click away (`setToolsFilter` in `scripts.templ`).
- Next: also flag **registered-but-undeclared** tools (registered in the
  `FunctionRegistry` but exposed by no node) — needs the registered set minus the
  declared set, then a small list.

### B. Join runtime data from the DB (the "store more / show more" ask)
The `DebugStore` already records `tool_calls` and `opened_files`. Aggregate them and
pass a small `usage` map into the page to make the knowledge dashboard *live*:
- Per **tool**: call count, error rate, p50/p95 duration, last-used time → show on
  the tool card and sort the Tools view by "most used" / "never used".
- Per **node**: how often its files were opened (from `opened_files`) → a heat
  indicator on the tree so unused branches are obvious.
- To make this efficient, consider adding lightweight rollup tables/queries (e.g.
  `tool_usage_daily(tool, day, count, errors, total_ms)`), updated from the existing
  `AfterAction` callback or a periodic job, so the dashboard reads aggregates instead
  of scanning raw rows. This pairs naturally with the new Prometheus metrics
  (`agentize_tool_calls_total`, `agentize_llm_*`) — the dashboard can show the same
  numbers without Prometheus by querying these rollups.

### C. Display upgrades
- Make the tree the source for an **embedded ECharts graph** inside `/agentize/docs`
  (click a graph node → open its detail) so structure and detail live on one page.
- Add filters/sort to the Tools view: by status, by registered/unregistered, by
  usage; and a **search-by-input-schema** (find tools that take a given parameter).
- Lazy-load node `content` (fetch on expand) to shrink the initial page for big trees.
- Add a "problems" panel summarizing findings #1–#4 (dead tools, missing auth, stale
  nodes) so issues are visible at a glance.

### D. How to implement safely
1. Add/rename fields on `NodeDocument` (+ the conversion in `NewAgentizeDocument`) —
   pure Go, no codegen needed for the data layer.
2. For any **visual** change, edit the `.templ` file and run `templ generate`; never
   edit `*_templ.go` by hand.
3. Keep the `{ treeData }` / `{ nodesData }` / `{ registeredTools }` token contract
   intact; add new blobs the same way (string injected + `ReplaceAll`).
4. For DB-backed usage, extend the `DebugStore` interface with read-only aggregate
   queries and pass the result as a new injected JS object (e.g. `usageData`).

---

*Keep this file in sync when you change the data model, the views, or the routes.*
