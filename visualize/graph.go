package visualize

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/components"
	"github.com/go-echarts/go-echarts/v2/opts"

	"github.com/ghiac/agentize/model"
)

// GraphVisualizer renders the knowledge tree as an interactive graph.
type GraphVisualizer struct {
	nodes           map[string]*model.Node
	visibilityCache map[string]bool
}

// NewGraphVisualizer creates a new graph visualizer instance.
func NewGraphVisualizer(nodes map[string]*model.Node) *GraphVisualizer {
	return &GraphVisualizer{nodes: nodes}
}

type graphPayload struct {
	nodes      []opts.GraphNode
	links      []opts.GraphLink
	categories []*opts.GraphCategory
	summary    graphSummary
	nodeMeta   map[string]nodeData
}

type graphSummary struct {
	nodes int
	tools int
}

// GenerateGraph builds the go-echarts graph component.
func (gv *GraphVisualizer) GenerateGraph(title string) *charts.Graph {
	graph, _ := gv.graphWithPayload(title)
	return graph
}

func (gv *GraphVisualizer) graphWithPayload(title string) (*charts.Graph, graphPayload) {
	payload := gv.buildGraphPayload()
	graph := gv.buildGraph(title, payload)
	return graph, payload
}

func (gv *GraphVisualizer) buildGraph(title string, payload graphPayload) *charts.Graph {
	graph := charts.NewGraph()
	graph.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title:    title,
			Subtitle: fmt.Sprintf("%d nodes • %d tools", payload.summary.nodes, payload.summary.tools),
		}),
		charts.WithTooltipOpts(opts.Tooltip{Show: opts.Bool(true)}),
		charts.WithLegendOpts(opts.Legend{Show: opts.Bool(true)}),
		charts.WithInitializationOpts(opts.Initialization{
			Width:  "1200px",
			Height: "800px",
		}),
	)

	if len(payload.nodes) == 0 {
		return graph
	}

	graph.AddSeries(
		"knowledge-tree",
		payload.nodes,
		payload.links,
		charts.WithGraphChartOpts(opts.GraphChart{
			Layout:             "force",
			Roam:               opts.Bool(true),
			FocusNodeAdjacency: opts.Bool(true),
			Force: &opts.GraphForce{
				Repulsion:  1200,
				Gravity:    0.1,
				EdgeLength: 200,
			},
			Categories: payload.categories,
		}),
		charts.WithLabelOpts(opts.Label{
			Show: opts.Bool(true),
		}),
		charts.WithLineStyleOpts(opts.LineStyle{
			Curveness: 0.25,
			Width:     2,
		}),
	)

	return graph
}

// SaveToFile renders the graph and augments it with the modal + JS handlers.
func (gv *GraphVisualizer) SaveToFile(filename, title string) error {
	graph, payload := gv.graphWithPayload(title)

	page := components.NewPage()
	page.AddCharts(graph)

	tmpFile, err := os.CreateTemp("", "graph-*.html")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFileName := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpFileName)

	tmpOutput, err := os.Create(tmpFileName)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	if err := page.Render(tmpOutput); err != nil {
		tmpOutput.Close()
		return fmt.Errorf("failed to render graph page: %w", err)
	}
	tmpOutput.Close()

	renderedContent, err := os.ReadFile(tmpFileName)
	if err != nil {
		return fmt.Errorf("failed to read rendered content: %w", err)
	}

	modalHTML, err := gv.generateModalHTML(payload.nodeMeta)
	if err != nil {
		return fmt.Errorf("failed to build modal markup: %w", err)
	}

	finalContent := string(renderedContent)
	bodyCloseIdx := strings.LastIndex(finalContent, "</body>")
	if bodyCloseIdx == -1 {
		finalContent += modalHTML
	} else {
		finalContent = finalContent[:bodyCloseIdx] + modalHTML + finalContent[bodyCloseIdx:]
	}

	return os.WriteFile(filename, []byte(finalContent), 0o644)
}

func (gv *GraphVisualizer) buildGraphPayload() graphPayload {
	payload := graphPayload{
		categories: gv.createCategories(),
		nodeMeta:   make(map[string]nodeData),
	}

	if len(gv.nodes) == 0 {
		return payload
	}

	gv.visibilityCache = make(map[string]bool)

	paths := gv.sortedPaths()
	children := gv.buildChildrenIndex(paths)

	nameRegistry := &nameAllocator{}
	nodeNames := make(map[string]string, len(paths))
	visibleTools := make(map[string][]model.Tool)

	for _, path := range paths {
		node := gv.nodes[path]
		if node == nil || !gv.isPathVisible(path) {
			continue
		}

		displayName := nameRegistry.Unique(gv.displayNameForNode(node))
		nodeNames[path] = displayName

		tools := gv.collectVisibleTools(node)
		visibleTools[path] = tools

		payload.nodes = append(payload.nodes, gv.buildGraphNode(displayName, node, len(tools)))
		payload.nodeMeta[displayName] = gv.buildNodeMeta(node, tools)
		payload.summary.nodes++
	}

	toolNames := make(map[string][]string)
	for _, path := range paths {
		if _, ok := nodeNames[path]; !ok {
			continue
		}
		tools := visibleTools[path]
		if len(tools) == 0 {
			continue
		}

		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			toolName := nameRegistry.Unique(tool.Name)
			names = append(names, toolName)
			payload.nodes = append(payload.nodes, gv.buildToolGraphNode(toolName))
			payload.summary.tools++
		}
		toolNames[path] = names
	}

	for _, path := range paths {
		sourceName := nodeNames[path]
		if sourceName == "" {
			continue
		}

		for _, childPath := range children[path] {
			targetName := nodeNames[childPath]
			if targetName == "" {
				continue
			}
			payload.links = append(payload.links, opts.GraphLink{
				Source: sourceName,
				Target: targetName,
				Value:  1,
				LineStyle: &opts.LineStyle{
					Width:     2,
					Curveness: 0.2,
				},
			})
		}

		for _, toolName := range toolNames[path] {
			payload.links = append(payload.links, opts.GraphLink{
				Source: sourceName,
				Target: toolName,
				Value:  0.5,
				LineStyle: &opts.LineStyle{
					Width:     1,
					Curveness: 0.05,
					Type:      "dashed",
				},
			})
		}
	}

	return payload
}

func (gv *GraphVisualizer) sortedPaths() []string {
	paths := make([]string, 0, len(gv.nodes))
	for path := range gv.nodes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (gv *GraphVisualizer) buildChildrenIndex(paths []string) map[string][]string {
	children := make(map[string][]string)
	for _, path := range paths {
		parent := gv.parentPath(path)
		if parent == "" {
			continue
		}
		children[parent] = append(children[parent], path)
	}
	for _, list := range children {
		sort.Strings(list)
	}
	return children
}

func (gv *GraphVisualizer) parentPath(path string) string {
	if path == "" || path == "root" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		if idx == 0 {
			return ""
		}
		return path[:idx]
	}
	return "root"
}

func (gv *GraphVisualizer) displayNameForNode(node *model.Node) string {
	if node == nil {
		return ""
	}
	if title := strings.TrimSpace(node.Title); title != "" {
		return title
	}
	if id := strings.TrimSpace(node.ID); id != "" {
		return id
	}
	return gv.getNodeName(node.Path)
}

func (gv *GraphVisualizer) collectVisibleTools(node *model.Node) []model.Tool {
	if node == nil || len(node.Tools) == 0 {
		return nil
	}
	tools := make([]model.Tool, 0, len(node.Tools))
	for _, tool := range node.Tools {
		if tool.Status == model.ToolStatusHidden {
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

func (gv *GraphVisualizer) buildGraphNode(name string, node *model.Node, toolCount int) opts.GraphNode {
	category := gv.getNodeCategory(node)
	return opts.GraphNode{
		Name:       name,
		Value:      float32(toolCount + 1),
		Category:   category,
		SymbolSize: gv.calculateNodeSize(node),
		ItemStyle:  gv.getNodeStyle(category),
	}
}

func (gv *GraphVisualizer) buildToolGraphNode(name string) opts.GraphNode {
	return opts.GraphNode{
		Name:       name,
		Value:      1,
		Category:   4,
		SymbolSize: 18,
		ItemStyle:  gv.getNodeStyle(4),
	}
}

func (gv *GraphVisualizer) buildNodeMeta(node *model.Node, tools []model.Tool) nodeData {
	meta := nodeData{
		Path:        node.Path,
		ID:          node.ID,
		Title:       gv.displayNameForNode(node),
		Description: node.Description,
		Content:     node.Content,
		CanAdvance:  gv.canAdvance(node),
		Tools:       make([]toolData, 0, len(tools)),
		Auth:        gv.buildAuthData(node),
	}

	for _, tool := range tools {
		meta.Tools = append(meta.Tools, toolData{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Status:      string(tool.Status),
		})
	}

	return meta
}

func (gv *GraphVisualizer) buildAuthData(node *model.Node) map[string]interface{} {
	authData := map[string]interface{}{
		"users": make([]map[string]interface{}, 0),
	}
	if node.Auth.Users == nil {
		return authData
	}

	userIDs := make([]string, 0, len(node.Auth.Users))
	for id := range node.Auth.Users {
		userIDs = append(userIDs, id)
	}
	sort.Strings(userIDs)

	for _, id := range userIDs {
		perms := node.Auth.Users[id]
		if perms == nil {
			continue
		}
		authData["users"] = append(authData["users"].([]map[string]interface{}), map[string]interface{}{
			"user_id":          id,
			"can_edit":         perms.HasPermission(model.PermWrite),
			"can_read":         perms.HasPermission(model.PermRead),
			"can_access_next":  perms.HasPermission(model.PermExecute),
			"can_see":          perms.HasPermission(model.PermSee),
			"visible_in_docs":  perms.HasPermission(model.PermVisibleDocs),
			"visible_in_graph": perms.HasPermission(model.PermVisibleGraph),
		})
	}

	return authData
}

func (gv *GraphVisualizer) canAdvance(node *model.Node) bool {
	if node == nil || len(node.Auth.Users) == 0 {
		return true
	}
	for _, perms := range node.Auth.Users {
		if perms != nil && perms.HasPermission(model.PermExecute) {
			return true
		}
	}
	return false
}

func (gv *GraphVisualizer) isPathVisible(path string) bool {
	if gv.visibilityCache == nil {
		gv.visibilityCache = make(map[string]bool)
	}
	if visible, ok := gv.visibilityCache[path]; ok {
		return visible
	}
	node := gv.nodes[path]
	if node == nil {
		gv.visibilityCache[path] = false
		return false
	}

	visible := gv.evaluateVisibility(node)
	gv.visibilityCache[path] = visible
	return visible
}

func (gv *GraphVisualizer) evaluateVisibility(node *model.Node) bool {
	if node.Auth.Default != nil {
		if node.Auth.Default.VisibleGraph != nil {
			return *node.Auth.Default.VisibleGraph
		}
		if node.Auth.Default.Perms != "" && strings.ContainsRune(node.Auth.Default.Perms, model.PermVisibleGraph) {
			return true
		}
	}
	if node.Auth.Inherit && node.Path != "root" {
		parent := gv.parentPath(node.Path)
		if parent != "" {
			return gv.isPathVisible(parent)
		}
	}
	return true
}

func (gv *GraphVisualizer) getNodeName(path string) string {
	if path == "" {
		return ""
	}
	if path == "root" {
		return "root"
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func (gv *GraphVisualizer) getNodeCategory(node *model.Node) int {
	if node == nil {
		return 1
	}
	if node.Path == "root" {
		return 0
	}
	if !gv.canAdvance(node) {
		return 2
	}
	if len(node.Tools) >= 3 {
		return 3
	}
	return 1
}

func (gv *GraphVisualizer) createCategories() []*opts.GraphCategory {
	return []*opts.GraphCategory{
		{
			Name: "Root",
			ItemStyle: &opts.ItemStyle{
				Color: "#5470c6",
			},
		},
		{
			Name: "Intermediate",
			ItemStyle: &opts.ItemStyle{
				Color: "#91cc75",
			},
		},
		{
			Name: "Leaf",
			ItemStyle: &opts.ItemStyle{
				Color: "#fac858",
			},
		},
		{
			Name: "Tool Rich",
			ItemStyle: &opts.ItemStyle{
				Color: "#ee6666",
			},
		},
		{
			Name: "Tool",
			ItemStyle: &opts.ItemStyle{
				Color: "#73c0de",
			},
		},
	}
}

func (gv *GraphVisualizer) calculateNodeSize(node *model.Node) float32 {
	if node == nil {
		return 30
	}
	base := float32(30)
	toolBoost := float32(len(node.Tools)) * 5

	if node.Path == "root" {
		return base + toolBoost + 20
	}
	if !gv.canAdvance(node) {
		return base + toolBoost
	}
	return base + toolBoost + 10
}

func (gv *GraphVisualizer) getNodeStyle(category int) *opts.ItemStyle {
	colors := []string{
		"#5470c6",
		"#91cc75",
		"#fac858",
		"#ee6666",
		"#73c0de",
	}
	if category < 0 || category >= len(colors) {
		category = 1
	}

	border := float32(2)
	if category == 4 {
		border = 1
	}

	return &opts.ItemStyle{
		Color:       colors[category],
		BorderColor: "#fff",
		BorderWidth: border,
	}
}

func (gv *GraphVisualizer) generateModalHTML(meta map[string]nodeData) (string, error) {
	if meta == nil {
		meta = map[string]nodeData{}
	}
	payload, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}

	data := strings.ReplaceAll(string(payload), "</script>", "<\\/script>")

	var builder strings.Builder
	builder.WriteString(`<style>
.node-modal {
	display: none;
	position: fixed;
	z-index: 10000;
	left: 0;
	top: 0;
	width: 100%;
	height: 100%;
	background-color: rgba(0,0,0,0.5);
	overflow: auto;
}
.node-modal-content {
	background-color: #fefefe;
	margin: 5% auto;
	padding: 20px;
	border: 1px solid #888;
	border-radius: 8px;
	width: 80%;
	max-width: 900px;
	max-height: 90%;
	overflow-y: auto;
	box-shadow: 0 4px 6px rgba(0,0,0,0.1);
}
.node-modal-header {
	display: flex;
	justify-content: space-between;
	align-items: center;
	margin-bottom: 16px;
	padding-bottom: 10px;
	border-bottom: 2px solid #eee;
}
.node-modal-title {
	font-size: 24px;
	font-weight: bold;
	color: #333;
	margin: 0;
}
.node-modal-close {
	color: #888;
	font-size: 28px;
	font-weight: bold;
	cursor: pointer;
	line-height: 20px;
}
.node-modal-close:hover,
.node-modal-close:focus {
	color: #000;
	text-decoration: none;
}
.node-modal-section {
	margin-bottom: 18px;
}
.node-modal-section-title {
	font-size: 18px;
	font-weight: bold;
	color: #5470c6;
	margin-bottom: 8px;
}
.node-modal-section-content {
	color: #555;
	line-height: 1.6;
	white-space: pre-wrap;
	word-break: break-word;
}
.node-modal-tools {
	display: grid;
	grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
	gap: 12px;
}
.node-modal-tool {
	background-color: #f5f5f5;
	padding: 10px;
	border-radius: 4px;
	border-left: 3px solid #73c0de;
}
.node-modal-tool-name {
	font-weight: bold;
	color: #333;
	margin-bottom: 4px;
}
.node-modal-tool-desc {
	font-size: 14px;
	color: #666;
}
.node-modal-content-text {
	background-color: #f9f9f9;
	padding: 15px;
	border-radius: 4px;
	max-height: 320px;
	overflow-y: auto;
	font-family: 'Courier New', monospace;
	font-size: 13px;
	line-height: 1.5;
}
</style>
<div id="nodeModal" class="node-modal">
	<div class="node-modal-content">
		<div class="node-modal-header">
			<h2 class="node-modal-title" id="modalTitle">Node Details</h2>
			<span class="node-modal-close" id="modalClose">&times;</span>
		</div>
		<div id="modalBody"></div>
	</div>
</div>
<script>
const nodeData = `)
	builder.WriteString(data)
	builder.WriteString(`;

(function () {
	const modal = document.getElementById('nodeModal');
	const modalTitle = document.getElementById('modalTitle');
	const modalBody = document.getElementById('modalBody');
	const modalClose = document.getElementById('modalClose');

	function escapeHtml(text) {
		if (text === undefined || text === null) {
			return '';
		}
		const div = document.createElement('div');
		div.textContent = text;
		return div.innerHTML;
	}

	function formatJson(obj) {
		try {
			return JSON.stringify(obj, null, 2);
		} catch (e) {
			return '';
		}
	}

	function closeModal() {
		modal.style.display = 'none';
	}

	function showNodeDetails(name) {
		const data = nodeData[name];
		if (!data) {
			console.warn('No graph metadata for node:', name);
			return;
		}

		modalTitle.textContent = data.title || data.id || name;

		let html = '';

		if (data.path) {
			html += '<div class="node-modal-section">';
			html += '<div class="node-modal-section-title">Path</div>';
			html += '<div class="node-modal-section-content">' + escapeHtml(data.path) + '</div>';
			html += '</div>';
		}

		if (data.description) {
			html += '<div class="node-modal-section">';
			html += '<div class="node-modal-section-title">Description</div>';
			html += '<div class="node-modal-section-content">' + escapeHtml(data.description) + '</div>';
			html += '</div>';
		}

		if (data.auth) {
			html += '<div class="node-modal-section">';
			html += '<div class="node-modal-section-title">Auth</div>';
			html += '<div class="node-modal-content-text">' + escapeHtml(formatJson(data.auth)) + '</div>';
			html += '</div>';
		}

		if (Array.isArray(data.tools) && data.tools.length > 0) {
			html += '<div class="node-modal-section">';
			html += '<div class="node-modal-section-title">Tools (' + data.tools.length + ')</div>';
			html += '<div class="node-modal-tools">';
			data.tools.forEach(function(tool) {
				html += '<div class="node-modal-tool">';
				html += '<div class="node-modal-tool-name">' + escapeHtml(tool.name || '') + '</div>';
				if (tool.description) {
					html += '<div class="node-modal-tool-desc">' + escapeHtml(tool.description) + '</div>';
				}
				if (tool.status && tool.status !== 'active') {
					html += '<div style="color:#ee6666;font-size:12px;margin-top:6px;">Status: ' + escapeHtml(tool.status) + '</div>';
				}
				html += '</div>';
			});
			html += '</div></div>';
		}

		if (data.content) {
			html += '<div class="node-modal-section">';
			html += '<div class="node-modal-section-title">Content</div>';
			html += '<div class="node-modal-content-text">' + escapeHtml(data.content) + '</div>';
			html += '</div>';
		}

		modalBody.innerHTML = html;
		modal.style.display = 'block';
	}

	modalClose.addEventListener('click', closeModal);
	window.addEventListener('click', function (event) {
		if (event.target === modal) {
			closeModal();
		}
	});

	function attachChartHandler() {
		if (typeof echarts === 'undefined') {
			setTimeout(attachChartHandler, 250);
			return;
		}
		const containers = document.querySelectorAll('[id^="chart"], div[id*="chart"]');
		for (const container of containers) {
			const instance = echarts.getInstanceByDom(container);
			if (instance) {
				instance.off('click');
				instance.on('click', function (params) {
					if (params && params.data && params.data.name) {
						showNodeDetails(params.data.name);
					}
				});
				return;
			}
		}
		setTimeout(attachChartHandler, 300);
	}

	function attachSvgFallback() {
		const svg = document.querySelector('svg');
		if (!svg) {
			setTimeout(attachSvgFallback, 800);
			return;
		}
		svg.addEventListener('click', function (event) {
			if (!event.target || event.target.tagName !== 'text') {
				return;
			}
			const label = event.target.textContent.trim();
			if (nodeData[label]) {
				showNodeDetails(label);
			}
		}, true);
	}

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', function () {
			attachChartHandler();
			setTimeout(attachSvgFallback, 800);
		});
	} else {
		attachChartHandler();
		setTimeout(attachSvgFallback, 800);
	}
})();
</script>
`)

	return builder.String(), nil
}

type nodeData struct {
	Path        string                 `json:"path"`
	ID          string                 `json:"id"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Content     string                 `json:"content"`
	CanAdvance  bool                   `json:"can_advance"`
	Tools       []toolData             `json:"tools"`
	Auth        map[string]interface{} `json:"auth"`
}

type toolData struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	Status      string                 `json:"status"`
}

type nameAllocator struct {
	counts map[string]int
}

func (a *nameAllocator) Unique(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "node"
	}
	if a.counts == nil {
		a.counts = make(map[string]int)
	}
	count := a.counts[base]
	if count == 0 {
		a.counts[base] = 1
		return base
	}
	count++
	a.counts[base] = count
	return fmt.Sprintf("%s (%d)", base, count)
}
