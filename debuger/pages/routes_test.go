package pages

import (
	"os"
	"strings"
	"testing"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

// TestDumpRouteDAGHTML writes a rich sample routing-DAG detail page to the path
// in AGENTIZE_DUMP_DAG (skipped otherwise). Used to eyeball the ECharts graph in
// a browser; not part of normal CI.
func TestDumpRouteDAGHTML(t *testing.T) {
	out := os.Getenv("AGENTIZE_DUMP_DAG")
	if out == "" {
		t.Skip("set AGENTIZE_DUMP_DAG=/path/to.html to dump a sample page")
	}

	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	handler, err := debuger.NewDebugHandler(sqliteStore)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// A trace that exercises every node type: decision -> (tool + plan), a second
	// decision that dispatches and then escalates, ending in the agent's answer.
	sess := model.NewSessionWithType("user-42", model.AgentTypeCore)
	b := model.NewRouteTraceBuilder(sess, "مقایسه قیمت طلا و دلار را برام تحلیل کن و یک جدول بده")
	b.Decision("Decision 1", "openai/gpt-5-nano", 320, 540, model.RouteStatusOK, "finish_reason=tool_calls · tool_calls=2")
	b.Tool(model.RouteNodeToolCall, "web_search", "جستجوی وب", `{"query":"gold vs usd price"}`, model.RouteStatusOK, 880)
	b.Tool(model.RouteNodePlan, "execute_plan", "Execute plan", `{"message":"compare then tabulate"}`, model.RouteStatusOK, 1500)
	b.Decision("Decision 2", "openai/gpt-5-nano", 410, 600, model.RouteStatusOK, "finish_reason=tool_calls · tool_calls=1")
	b.Dispatch("low", "هوش سطح پایین", "compare gold and usd", model.RouteStatusOK, 1300)
	b.Escalate("high", "هوش سطح بالا", "compare gold and usd (escalated)", model.RouteStatusOK, 2600)
	b.Response("بر اساس داده‌ها، جدول مقایسه طلا و دلار به شرح زیر است...", true, model.RouteStatusOK)
	tr := b.Build(0)
	if err := sqliteStore.PutRouteTrace(tr); err != nil {
		t.Fatalf("put: %v", err)
	}

	html, err := RenderRouteDetail(handler, tr.TraceID)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %d bytes to %s", len(html), out)
}

// TestRenderRoutes verifies the routing-DAG list and detail pages render the
// empty state, a populated trace, the interactive graph wiring, and that
// user-derived text is HTML-escaped (no XSS).
func TestRenderRoutes(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	handler, err := debuger.NewDebugHandler(sqliteStore)
	if err != nil {
		t.Fatalf("failed to create debug handler: %v", err)
	}

	// Empty state.
	html, err := RenderRoutes(handler, 1)
	if err != nil {
		t.Fatalf("RenderRoutes (empty) failed: %v", err)
	}
	if !strings.Contains(html, "No routing traces") {
		t.Errorf("expected empty-state message, got:\n%s", html)
	}

	// Build a realistic dispatch trace with a deliberately malicious message.
	sess := model.NewSessionWithType("user-1", model.AgentTypeCore)
	b := model.NewRouteTraceBuilder(sess, "<script>alert('xss')</script>")
	b.Decision("Decision 1", "test-model", 50, 12, model.RouteStatusOK, "finish_reason=tool_calls")
	b.Dispatch("researcher", "Research agent", "find papers", model.RouteStatusOK, 120)
	b.Response("here are the papers", true, model.RouteStatusOK)
	tr := b.Build(0)
	if err := sqliteStore.PutRouteTrace(tr); err != nil {
		t.Fatalf("PutRouteTrace failed: %v", err)
	}

	// List shows the dispatched agent.
	html, err = RenderRoutes(handler, 1)
	if err != nil {
		t.Fatalf("RenderRoutes (populated) failed: %v", err)
	}
	if !strings.Contains(html, "researcher") {
		t.Errorf("list page missing dispatched agent:\n%s", html)
	}
	if strings.Contains(html, "<script>alert('xss')</script>") {
		t.Errorf("list page did not escape user message (XSS risk)")
	}

	// Detail renders the graph container, the JSON data island, the ECharts
	// init, and the steps — with the message escaped.
	detail, err := RenderRouteDetail(handler, tr.TraceID)
	if err != nil {
		t.Fatalf("RenderRouteDetail failed: %v", err)
	}
	for _, want := range []string{
		`id="route-dag"`,
		`id="route-dag-data"`,
		"echarts",
		"Research agent",
		"Decision &amp; Forward Graph",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	// The JSON island must not contain a raw breakout sequence; json.Marshal
	// escapes "<" to < so "</script>" inside the message is neutralised.
	if strings.Contains(detail, "<script>alert('xss')</script>") {
		t.Errorf("detail page leaked unescaped user content (XSS risk)")
	}

	// Unknown trace id is an error, not a panic.
	if _, err := RenderRouteDetail(handler, "does-not-exist"); err == nil {
		t.Errorf("expected error for unknown trace id")
	}
}
