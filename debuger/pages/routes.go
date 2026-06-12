package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

// routesNavPath is the base URL for the routing-DAG pages.
const routesNavPath = "/agentize/debug/routes"

// echartsCDN is the same ECharts build the knowledge-graph page uses.
const echartsCDN = "https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js"

// RenderRoutes renders the list of Core routing-decision DAGs (one per message).
func RenderRoutes(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	traces, err := dp.GetAllRouteTraces()
	if err != nil {
		return "", fmt.Errorf("failed to get route traces: %w", err)
	}

	total := len(traces)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, total, components.DefaultItemsPerPage)
	pageItems := traces[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("Routing Decisions (DAG)", "diagram-3", total)
	content += `<p class="text-muted small mb-3">After each user message the Core records how it decided and where it forwarded the message — every LLM turn, tool call, and agent dispatch — as a directed graph. Open one to see the graph.</p>`

	if total == 0 {
		content += components.InfoAlert("No routing traces recorded yet. They appear here after the Core processes user messages.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "When", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Session", NoWrap: true},
			{Header: "Message"},
			{Header: "Routed to", NoWrap: true},
			{Header: "Nodes", Center: true, NoWrap: true},
			{Header: "Tokens", Center: true, NoWrap: true},
			{Header: "Duration", Center: true, NoWrap: true},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Graph", Center: true, NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{Hover: true, Small: true, Responsive: true, AlignMiddle: true})
		for _, tr := range pageItems {
			content += routeTraceRow(tr)
		}
		content += components.TableEnd(true)
		content += components.PaginationSimple(page, total, components.DefaultItemsPerPage, routesNavPath)
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Routing DAG") + ui.NavbarAndBody(routesNavPath, content) + ui.Footer(), nil
}

func routeTraceRow(tr *model.RouteTrace) string {
	detailURL := routesNavPath + "/" + template.URLQueryEscaper(tr.TraceID)

	routed := `<span class="text-muted">— Core</span>`
	if agents := tr.DispatchedAgents(); len(agents) > 0 {
		routed = components.Badge(strings.Join(agents, " → "), "success")
	}

	return fmt.Sprintf(`<tr>
        <td class="text-nowrap" title="%s">%s</td>
        <td class="text-nowrap">%s</td>
        <td class="text-nowrap">%s</td>
        <td>%s</td>
        <td class="text-nowrap">%s</td>
        <td class="text-center">%d</td>
        <td class="text-center">%s</td>
        <td class="text-center text-nowrap">%s</td>
        <td class="text-center">%s</td>
        <td class="text-center"><a href="%s" class="btn btn-sm btn-outline-primary">View DAG</a></td>
    </tr>`,
		template.HTMLEscapeString(debuger.FormatTime(tr.CreatedAt)),
		template.HTMLEscapeString(debuger.FormatTimeAgo(tr.CreatedAt)),
		components.TruncatedLink(tr.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(tr.UserID), 16),
		components.TruncatedLink(tr.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(tr.SessionID), 22),
		template.HTMLEscapeString(truncateRunes(tr.Message, 80)),
		routed,
		tr.NodeCount(),
		formatCount(tr.TotalTokens),
		debuger.FormatDurationMs(tr.DurationMs),
		routeStatusBadge(tr.Status),
		detailURL,
	)
}

// RenderRouteDetail renders a single routing DAG: metadata, the interactive
// graph, the message/response, and an ordered step table (also a no-JS fallback).
func RenderRouteDetail(handler *debuger.DebugHandler, traceID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	tr, err := dp.GetRouteTraceByID(traceID)
	if err != nil {
		return "", fmt.Errorf("failed to get route trace: %w", err)
	}
	if tr == nil {
		return "", fmt.Errorf("route trace not found: %s", traceID)
	}

	content := ui.ContainerStart()
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Routing DAG", URL: routesNavPath},
		{Label: tr.TraceID, Active: true},
	})

	// Metadata card
	content += ui.CardStart("Routing Trace", "diagram-3")
	content += `<div class="row"><div class="col-md-6"><table class="table table-sm">`
	content += metaRow("Trace ID", components.InlineCode(tr.TraceID))
	content += metaRow("User", components.TruncatedLink(tr.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(tr.UserID), 30))
	content += metaRow("Session", components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(tr.SessionID)))
	content += metaRow("Status", routeStatusBadge(tr.Status))
	content += `</table></div><div class="col-md-6"><table class="table table-sm">`
	content += metaRow("Nodes", fmt.Sprintf("%d", tr.NodeCount()))
	content += metaRow("Total tokens", formatCount(tr.TotalTokens))
	content += metaRow("Duration", debuger.FormatDurationMs(tr.DurationMs))
	content += metaRow("Created at", template.HTMLEscapeString(debuger.FormatTime(tr.CreatedAt)))
	content += `</table></div></div>`
	if tr.Error != "" {
		content += fmt.Sprintf(`<div class="alert alert-danger mb-0"><strong>Error:</strong> %s</div>`, template.HTMLEscapeString(tr.Error))
	}
	content += ui.CardEnd()

	// Interactive DAG
	content += ui.CardStart("Decision & Forward Graph", "diagram-3-fill")
	content += routeDAGChartHTML(tr)
	content += routeLegendHTML()
	content += ui.CardEnd()

	// Message + response
	content += ui.CardStart("User Message", "chat-left-text")
	content += preBlock(tr.Message)
	content += ui.CardEnd()

	content += ui.CardStart("Final Response", "reply")
	content += preBlock(tr.Response)
	content += ui.CardEnd()

	// Ordered steps (also the no-JS fallback for the graph)
	content += ui.CardStartWithCount("Steps", "list-ol", tr.NodeCount())
	content += routeStepsTable(tr)
	content += ui.CardEnd()

	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Routing DAG: "+tr.TraceID) + ui.NavbarAndBody(routesNavPath, content) + ui.Footer(), nil
}

// ---------------------------------------------------------------------------
// Graph rendering (ECharts, server-computed layered layout)
// ---------------------------------------------------------------------------

type dagChartNode struct {
	Name     string  `json:"name"`     // node id — unique identity used by links
	Disp     string  `json:"disp"`     // short visible label
	Category int     `json:"category"` // index into categories
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Tooltip  string  `json:"tooltip"` // pre-escaped HTML
	Border   string  `json:"border"`  // status border color, "" = none
}

type dagChartLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label"`
}

type dagChartCategory struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type dagChartPayload struct {
	Nodes      []dagChartNode     `json:"nodes"`
	Links      []dagChartLink     `json:"links"`
	Categories []dagChartCategory `json:"categories"`
}

// routeNodeCategories is the fixed, ordered legend; the index is stored on each
// node so the chart, legend, and step badges share one color scheme.
var routeNodeCategories = []dagChartCategory{
	{"User message", "#6c757d"},
	{"Decision", "#0d6efd"},
	{"Tool call", "#6f42c1"},
	{"Agent dispatch", "#198754"},
	{"Escalation", "#fd7e14"},
	{"Plan", "#20c997"},
	{"Response", "#d63384"},
}

func routeNodeCategoryIndex(t model.RouteNodeType) int {
	switch t {
	case model.RouteNodeUserMessage:
		return 0
	case model.RouteNodeDecision:
		return 1
	case model.RouteNodeToolCall:
		return 2
	case model.RouteNodeAgentDispatch:
		return 3
	case model.RouteNodeEscalation:
		return 4
	case model.RouteNodePlan:
		return 5
	case model.RouteNodeResponse:
		return 6
	default:
		return 0
	}
}

func routeNodeEmoji(t model.RouteNodeType) string {
	switch t {
	case model.RouteNodeUserMessage:
		return "👤"
	case model.RouteNodeDecision:
		return "🧠"
	case model.RouteNodeToolCall:
		return "🔧"
	case model.RouteNodeAgentDispatch:
		return "🤖"
	case model.RouteNodeEscalation:
		return "⏫"
	case model.RouteNodePlan:
		return "📑"
	case model.RouteNodeResponse:
		return "💬"
	default:
		return "•"
	}
}

// routeNodeDisplay is the short text shown under a node in the graph.
func routeNodeDisplay(n *model.RouteNode) string {
	text := n.Label
	if n.Agent != "" {
		text = n.Agent
	}
	return routeNodeEmoji(n.Type) + " " + truncateRunes(text, 22)
}

// routeDAGChartHTML emits the chart container, the data as a JSON island, and the
// ECharts init script. The data is embedded in a non-executable
// <script type="application/json"> block; json.Marshal HTML-escapes <,>,& so a
// "</script>" inside any user-derived label cannot break out.
func routeDAGChartHTML(tr *model.RouteTrace) string {
	payload := buildDAGPayload(tr)
	raw, err := json.Marshal(payload)
	if err != nil {
		return components.InfoAlert("Failed to render graph data.")
	}

	var b strings.Builder
	b.WriteString(`<div id="route-dag" style="width:100%;height:540px;"></div>`)
	b.WriteString(`<script type="application/json" id="route-dag-data">`)
	b.Write(raw)
	b.WriteString(`</script>`)
	b.WriteString(`<script src="` + echartsCDN + `"></script>`)
	b.WriteString(routeDAGInitScript)
	return b.String()
}

// buildDAGPayload computes a left-to-right layered layout (x = longest-path depth
// from the root, y = stacking index within the depth) and the ECharts payload.
func buildDAGPayload(tr *model.RouteTrace) dagChartPayload {
	const dx, dy = 235.0, 120.0

	incoming := make(map[string][]string, len(tr.Nodes))
	for _, e := range tr.Edges {
		incoming[e.To] = append(incoming[e.To], e.From)
	}

	depth := make(map[string]int, len(tr.Nodes))
	rows := make(map[int]int)
	nodes := make([]dagChartNode, 0, len(tr.Nodes))
	// Nodes are appended in build order, so every edge's source precedes its
	// target — depth[source] is always known when we reach the target.
	for i := range tr.Nodes {
		n := &tr.Nodes[i]
		d := 0
		for _, from := range incoming[n.ID] {
			if depth[from]+1 > d {
				d = depth[from] + 1
			}
		}
		depth[n.ID] = d
		row := rows[d]
		rows[d]++

		nodes = append(nodes, dagChartNode{
			Name:     n.ID,
			Disp:     routeNodeDisplay(n),
			Category: routeNodeCategoryIndex(n.Type),
			X:        float64(d) * dx,
			Y:        float64(row) * dy,
			Tooltip:  routeNodeTooltip(n),
			Border:   routeStatusBorder(n.Status),
		})
	}

	links := make([]dagChartLink, 0, len(tr.Edges))
	for _, e := range tr.Edges {
		links = append(links, dagChartLink{Source: e.From, Target: e.To, Label: e.Label})
	}

	return dagChartPayload{Nodes: nodes, Links: links, Categories: routeNodeCategories}
}

// routeNodeTooltip builds the (HTML-escaped) hover card for a node.
func routeNodeTooltip(n *model.RouteNode) string {
	esc := template.HTMLEscapeString
	var parts []string
	parts = append(parts, "<strong>"+esc(n.Label)+"</strong>")
	parts = append(parts, "type: "+esc(string(n.Type)))
	if n.Agent != "" {
		parts = append(parts, "agent: "+esc(n.Agent))
	}
	if n.Tool != "" {
		parts = append(parts, "tool: "+esc(n.Tool))
	}
	if n.Model != "" {
		parts = append(parts, "model: "+esc(n.Model))
	}
	if n.Status != "" {
		parts = append(parts, "status: "+esc(string(n.Status)))
	}
	if n.Tokens > 0 {
		parts = append(parts, fmt.Sprintf("tokens: %d", n.Tokens))
	}
	if n.DurationMs > 0 {
		parts = append(parts, "took: "+debuger.FormatDurationMs(n.DurationMs))
	}
	if n.Detail != "" {
		parts = append(parts, "<span style=\"color:#aaa\">"+esc(truncateRunes(n.Detail, 240))+"</span>")
	}
	return strings.Join(parts, "<br>")
}

// routeDAGInitScript initialises the ECharts graph from the JSON island. It
// retries until the ECharts CDN script has loaded.
const routeDAGInitScript = `<script>
(function () {
  function init() {
    if (typeof echarts === 'undefined') { setTimeout(init, 120); return; }
    var el = document.getElementById('route-dag');
    var dataEl = document.getElementById('route-dag-data');
    if (!el || !dataEl) { return; }
    var payload;
    try { payload = JSON.parse(dataEl.textContent); } catch (e) { return; }
    var chart = echarts.init(el);
    var option = {
      tooltip: {
        confine: true,
        formatter: function (p) {
          if (p.dataType === 'edge') { return p.data.label || ''; }
          return p.data.tooltip || p.data.disp || '';
        }
      },
      legend: [{ data: payload.categories.map(function (c) { return c.name; }), top: 6 }],
      animationDuration: 400,
      series: [{
        type: 'graph',
        layout: 'none',
        roam: true,
        focusNodeAdjacency: true,
        symbolSize: 46,
        edgeSymbol: ['none', 'arrow'],
        edgeSymbolSize: 9,
        label: { show: true, position: 'bottom', fontSize: 11, color: '#333', formatter: function (p) { return p.data.disp; } },
        edgeLabel: { show: true, fontSize: 10, color: '#888', formatter: function (p) { return p.data.label || ''; } },
        lineStyle: { color: '#bbb', width: 1.6, curveness: 0.06 },
        categories: payload.categories.map(function (c) { return { name: c.name, itemStyle: { color: c.color } }; }),
        data: payload.nodes.map(function (n) {
          var item = { name: n.name, disp: n.disp, category: n.category, x: n.x, y: n.y, tooltip: n.tooltip };
          if (n.border) { item.itemStyle = { borderColor: n.border, borderWidth: 3 }; }
          return item;
        }),
        links: payload.links.map(function (l) { return { source: l.source, target: l.target, label: l.label }; })
      }]
    };
    chart.setOption(option);
    // A percentage-width container may not have its final width at init time
    // (late layout, fonts, scrollbars). Re-measure after paint and whenever the
    // container resizes so the graph always fills the card.
    var fit = function () { chart.resize(); };
    window.addEventListener('resize', fit);
    if (typeof requestAnimationFrame === 'function') { requestAnimationFrame(fit); }
    setTimeout(fit, 200);
    if (typeof ResizeObserver === 'function') { new ResizeObserver(fit).observe(el); }
  }
  if (document.readyState === 'loading') { document.addEventListener('DOMContentLoaded', init); } else { init(); }
})();
</script>`

// routeLegendHTML renders the static color legend below the chart.
func routeLegendHTML() string {
	var b strings.Builder
	b.WriteString(`<div class="mt-2 small text-muted d-flex flex-wrap gap-3">`)
	for _, c := range routeNodeCategories {
		b.WriteString(fmt.Sprintf(`<span><span style="display:inline-block;width:12px;height:12px;border-radius:3px;background:%s;margin-right:4px;vertical-align:middle;"></span>%s</span>`,
			c.Color, template.HTMLEscapeString(c.Name)))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ---------------------------------------------------------------------------
// Steps table + small helpers
// ---------------------------------------------------------------------------

func routeStepsTable(tr *model.RouteTrace) string {
	if len(tr.Nodes) == 0 {
		return components.InfoAlert("No steps recorded.")
	}
	columns := []components.ColumnConfig{
		{Header: "#", NoWrap: true},
		{Header: "Type", NoWrap: true},
		{Header: "Label"},
		{Header: "Target / Tool", NoWrap: true},
		{Header: "Status", Center: true, NoWrap: true},
		{Header: "Tokens", Center: true, NoWrap: true},
		{Header: "Duration", Center: true, NoWrap: true},
		{Header: "Detail"},
	}
	out := components.TableStartWithConfig(columns, components.TableConfig{Hover: true, Small: true, Responsive: true, AlignMiddle: true})
	for i := range tr.Nodes {
		n := &tr.Nodes[i]
		target := n.Agent
		if target == "" {
			target = n.Tool
		}
		targetCell := `<span class="text-muted">—</span>`
		if target != "" {
			targetCell = components.InlineCode(template.HTMLEscapeString(target))
		}
		tokens := "—"
		if n.Tokens > 0 {
			tokens = fmt.Sprintf("%d", n.Tokens)
		}
		duration := "—"
		if n.DurationMs > 0 {
			duration = debuger.FormatDurationMs(n.DurationMs)
		}
		detail := `<span class="text-muted">—</span>`
		if n.Detail != "" {
			detail = fmt.Sprintf(`<span class="text-muted">%s</span>`, template.HTMLEscapeString(truncateRunes(n.Detail, 160)))
		}
		out += fmt.Sprintf(`<tr>
            <td class="text-nowrap">%s</td>
            <td class="text-nowrap">%s</td>
            <td>%s</td>
            <td class="text-nowrap">%s</td>
            <td class="text-center">%s</td>
            <td class="text-center">%s</td>
            <td class="text-center text-nowrap">%s</td>
            <td>%s</td>
        </tr>`,
			template.HTMLEscapeString(n.ID),
			routeTypeBadge(n.Type),
			template.HTMLEscapeString(n.Label),
			targetCell,
			routeStatusBadge(string(n.Status)),
			tokens,
			duration,
			detail,
		)
	}
	out += components.TableEnd(true)
	return out
}

// routeTypeBadge renders a node type as a colored pill matching the legend.
func routeTypeBadge(t model.RouteNodeType) string {
	idx := routeNodeCategoryIndex(t)
	cat := routeNodeCategories[idx]
	return fmt.Sprintf(`<span class="badge" style="background:%s">%s %s</span>`,
		cat.Color, routeNodeEmoji(t), template.HTMLEscapeString(cat.Name))
}

// routeStatusBadge renders a trace/node status. Empty status is treated as ok.
func routeStatusBadge(status string) string {
	switch status {
	case "", "ok":
		return components.Badge("ok", "success")
	case "error":
		return components.Badge("error", "danger")
	case "blocked":
		return components.Badge("blocked", "warning")
	case "skipped":
		return components.Badge("skipped", "secondary")
	default:
		return components.Badge(status, "secondary")
	}
}

// routeStatusBorder is the node border color used to flag non-ok nodes.
func routeStatusBorder(status model.RouteNodeStatus) string {
	switch status {
	case model.RouteStatusError:
		return "#dc3545"
	case model.RouteStatusBlocked:
		return "#ffc107"
	case model.RouteStatusSkipped:
		return "#6c757d"
	default:
		return ""
	}
}

func metaRow(label, valueHTML string) string {
	return fmt.Sprintf(`<tr><th class="w-25">%s</th><td>%s</td></tr>`, template.HTMLEscapeString(label), valueHTML)
}

func preBlock(text string) string {
	if strings.TrimSpace(text) == "" {
		return components.InfoAlert("(empty)")
	}
	return `<pre class="bg-light p-3 rounded" style="white-space: pre-wrap; word-wrap: break-word; max-height: 320px; overflow-y: auto;">` +
		template.HTMLEscapeString(text) + `</pre>`
}

// formatCount renders a non-negative count, using an em dash for zero.
func formatCount(n int) string {
	if n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}
