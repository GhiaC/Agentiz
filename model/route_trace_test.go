package model

import (
	"strings"
	"testing"
	"time"
)

// findNode returns the first node of the given type, or nil.
func findNode(t *RouteTrace, typ RouteNodeType) *RouteNode {
	for i := range t.Nodes {
		if t.Nodes[i].Type == typ {
			return &t.Nodes[i]
		}
	}
	return nil
}

// hasEdge reports whether an edge between two node ids exists.
func hasEdge(t *RouteTrace, from, to string) bool {
	for _, e := range t.Edges {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}

func TestRouteTraceBuilder_DirectAnswer(t *testing.T) {
	s := NewSessionWithType("user-1", AgentTypeCore)
	b := NewRouteTraceBuilder(s, "hello")
	b.Decision("Decision", "gpt-x", 30, 12, RouteStatusOK, "finish_reason=stop")
	b.Response("hi there", false, RouteStatusOK)
	tr := b.Build(50 * time.Millisecond)

	if tr.TraceID != s.SessionID+"-rt0001" {
		t.Errorf("TraceID = %q, want %q", tr.TraceID, s.SessionID+"-rt0001")
	}
	if tr.Status != "ok" {
		t.Errorf("Status = %q, want ok", tr.Status)
	}
	if tr.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", tr.TotalTokens)
	}
	if tr.DurationMs != 50 {
		t.Errorf("DurationMs = %d, want 50", tr.DurationMs)
	}
	// root -> decision -> response
	if len(tr.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(tr.Nodes))
	}
	root := findNode(tr, RouteNodeUserMessage)
	dec := findNode(tr, RouteNodeDecision)
	resp := findNode(tr, RouteNodeResponse)
	if root == nil || dec == nil || resp == nil {
		t.Fatalf("missing nodes: root=%v dec=%v resp=%v", root, dec, resp)
	}
	if !hasEdge(tr, root.ID, dec.ID) || !hasEdge(tr, dec.ID, resp.ID) {
		t.Errorf("expected spine root->decision->response, edges=%+v", tr.Edges)
	}
}

func TestRouteTraceBuilder_DispatchAndEscalate(t *testing.T) {
	s := NewSessionWithType("user-1", AgentTypeCore)
	b := NewRouteTraceBuilder(s, "do something")
	b.Decision("Decision", "gpt-x", 40, 10, RouteStatusOK, "finish_reason=tool_calls")
	b.Dispatch("low", "Low agent", "the message", RouteStatusOK, 100)
	b.Escalate("high", "High agent", "the message", RouteStatusOK, 200)
	b.Response("final answer", true, RouteStatusOK)
	tr := b.Build(0)

	dispatch := findNode(tr, RouteNodeAgentDispatch)
	esc := findNode(tr, RouteNodeEscalation)
	resp := findNode(tr, RouteNodeResponse)
	if dispatch == nil || esc == nil || resp == nil {
		t.Fatalf("missing nodes: dispatch=%v esc=%v resp=%v", dispatch, esc, resp)
	}
	// dispatch -> escalation, and the dispatched answer returns from the escalation node.
	if !hasEdge(tr, dispatch.ID, esc.ID) {
		t.Errorf("expected dispatch->escalation edge, edges=%+v", tr.Edges)
	}
	if !hasEdge(tr, esc.ID, resp.ID) {
		t.Errorf("expected escalation->response edge (dispatched answer), edges=%+v", tr.Edges)
	}
	if got := tr.DispatchedAgents(); len(got) != 2 || got[0] != "low" || got[1] != "high" {
		t.Errorf("DispatchedAgents = %v, want [low high]", got)
	}
}

func TestRouteTraceBuilder_FailAndFinalizeOnce(t *testing.T) {
	s := NewSessionWithType("user-1", AgentTypeCore)
	b := NewRouteTraceBuilder(s, "x")
	b.Decision("Decision", "m", 0, 0, RouteStatusOK, "")
	b.Fail("boom")
	// A second terminal call must be ignored.
	b.Response("ignored", false, RouteStatusOK)
	tr := b.Build(0)

	if tr.Status != "error" || tr.Error != "boom" {
		t.Errorf("Status/Error = %q/%q, want error/boom", tr.Status, tr.Error)
	}
	if n := len(tr.Nodes); n != 3 { // root, decision, error
		t.Errorf("nodes = %d, want 3 (terminal added once)", n)
	}
}

func TestRouteTraceBuilder_NilSafe(t *testing.T) {
	var b *RouteTraceBuilder // nil: tracing disabled
	// None of these should panic.
	b.Decision("d", "m", 1, 1, RouteStatusOK, "x")
	b.Tool(RouteNodeToolCall, "t", "T", "d", RouteStatusOK, 1)
	b.Dispatch("a", "A", "d", RouteStatusOK, 1)
	b.Escalate("a", "A", "d", RouteStatusOK, 1)
	b.Response("r", false, RouteStatusOK)
	b.Fail("e")
	if tr := b.Build(0); tr != nil {
		t.Errorf("nil builder Build = %v, want nil", tr)
	}
}

func TestTruncateRouteText_RuneSafe(t *testing.T) {
	// Build a string longer than the limit out of multibyte runes.
	long := strings.Repeat("ت", routeTraceMaxText+50)
	got := truncateRouteText(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix")
	}
	// Result must remain valid UTF-8 (no rune split): count runes.
	if r := []rune(got); len(r) != routeTraceMaxText+1 {
		t.Errorf("truncated rune count = %d, want %d", len(r), routeTraceMaxText+1)
	}
}
