package model

import (
	"strconv"
	"sync"
	"time"
)

// RouteNodeType classifies a vertex in a Core routing DAG (RouteTrace).
type RouteNodeType string

const (
	// RouteNodeUserMessage is the root: the user message that entered the Core.
	RouteNodeUserMessage RouteNodeType = "user_message"
	// RouteNodeDecision is one Core LLM turn that decided what to do next.
	RouteNodeDecision RouteNodeType = "decision"
	// RouteNodeToolCall is a non-routing tool the Core executed (web_search,
	// update_status, create_session, ...).
	RouteNodeToolCall RouteNodeType = "tool_call"
	// RouteNodeAgentDispatch is a forward of the message to a worker agent
	// (call_agent_*) — the Core's primary "routing" decision.
	RouteNodeAgentDispatch RouteNodeType = "agent_dispatch"
	// RouteNodeEscalation is a forward to a higher-tier agent after the first
	// agent replied with an ESCALATE: result.
	RouteNodeEscalation RouteNodeType = "escalation"
	// RouteNodeResponse is the terminal answer returned to the user.
	RouteNodeResponse RouteNodeType = "response"
)

// RouteNodeStatus is the outcome recorded on a node.
type RouteNodeStatus string

const (
	// RouteStatusOK marks a node that completed successfully.
	RouteStatusOK RouteNodeStatus = "ok"
	// RouteStatusError marks a node whose action failed.
	RouteStatusError RouteNodeStatus = "error"
	// RouteStatusBlocked marks a node blocked before execution (e.g. by a quota
	// callback).
	RouteStatusBlocked RouteNodeStatus = "blocked"
	// RouteStatusSkipped marks a node the Core deliberately did not execute — a
	// redundant call_agent_* the LLM emitted after the Core had already
	// dispatched this turn (see RouteTraceBuilder.SkipDispatch).
	RouteStatusSkipped RouteNodeStatus = "skipped"
)

// RouteNode is a single vertex in a RouteTrace DAG.
type RouteNode struct {
	ID         string          `json:"id"`
	Type       RouteNodeType   `json:"type"`
	Label      string          `json:"label"`
	Detail     string          `json:"detail,omitempty"`
	Status     RouteNodeStatus `json:"status,omitempty"`
	Agent      string          `json:"agent,omitempty"` // target agent for dispatch/escalation
	Tool       string          `json:"tool,omitempty"`  // tool/function name
	Model      string          `json:"model,omitempty"` // model name for decision nodes
	Tokens     int             `json:"tokens,omitempty"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// RouteEdge is a directed edge From -> To between two RouteNodes.
type RouteEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

// RouteTrace is the persisted DAG of how the Core handled one user message:
// every decision (LLM turn), every tool call, and every forward (dispatch /
// escalation) to a worker agent, terminating in the response sent to the user.
//
// It is built incrementally by a RouteTraceBuilder while the Core processes a
// message, then persisted (store.PutRouteTrace) and rendered on the debug
// dashboard's Routes page.
type RouteTrace struct {
	TraceID   string `json:"trace_id"`
	SessionID string `json:"session_id"` // the Core session that owns this trace
	UserID    string `json:"user_id"`

	Message  string `json:"message"`  // the user message (truncated)
	Response string `json:"response"` // the final response to the user (truncated)

	Nodes []RouteNode `json:"nodes"`
	Edges []RouteEdge `json:"edges"`

	Status      string `json:"status"` // "ok" | "error"
	Error       string `json:"error,omitempty"`
	TotalTokens int    `json:"total_tokens,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// NodeCount returns the number of nodes in the trace.
func (t *RouteTrace) NodeCount() int {
	if t == nil {
		return 0
	}
	return len(t.Nodes)
}

// DispatchedAgents returns the names of agents the Core forwarded to (dispatch
// or escalation nodes), in order. Handy for list/summary views.
func (t *RouteTrace) DispatchedAgents() []string {
	if t == nil {
		return nil
	}
	var agents []string
	for _, n := range t.Nodes {
		// Skipped dispatches were never executed, so they are not "routed to".
		if (n.Type == RouteNodeAgentDispatch || n.Type == RouteNodeEscalation) && n.Agent != "" && n.Status != RouteStatusSkipped {
			agents = append(agents, n.Agent)
		}
	}
	return agents
}

// routeTraceMaxText bounds any free-text stored on a trace (message, response,
// node detail) so a single trace stays compact in the database. Measured in
// runes so multibyte text (e.g. Persian) is never split mid-character.
const routeTraceMaxText = 2000

// truncateRouteText shortens s to routeTraceMaxText runes, appending an ellipsis
// when truncated. Rune-based so it is safe for non-ASCII content.
func truncateRouteText(s string) string {
	r := []rune(s)
	if len(r) <= routeTraceMaxText {
		return s
	}
	return string(r[:routeTraceMaxText]) + "…"
}

// RouteTraceBuilder incrementally assembles a RouteTrace as the Core processes a
// single message. Every method is safe to call on a nil *RouteTraceBuilder (it
// is a no-op), so instrumentation call sites need no nil checks when tracing is
// disabled. Methods are mutex-guarded: within one message the Core runs
// sequentially, but the guard keeps the builder correct if a tool ever records
// from another goroutine.
//
// Linking model (the DAG shape):
//   - The root is the user_message node.
//   - Decision nodes form a spine: root -> decision1 -> decision2 -> ... (each
//     LLM turn feeds the previous turn's tool results back in).
//   - Tool / dispatch nodes hang off the decision that invoked them.
//   - An escalation node hangs off the dispatch it escalated from.
//   - The terminal response node hangs off the final decision, or — when the
//     Core dispatched — off the agent whose answer it returned verbatim.
type RouteTraceBuilder struct {
	mu    sync.Mutex
	trace *RouteTrace

	seq          int
	rootID       string // user_message node id
	spineTailID  string // last decision (or root): parent of the next decision
	currentDecID string // current decision: parent of tools/dispatches
	lastRouteID  string // last dispatch/escalation node: parent of escalation/response
	finalized    bool   // true once a terminal (response/error) node was added
}

// NewRouteTraceBuilder starts a trace for the given Core session and user
// message. Returns nil when session is nil (tracing then becomes a no-op).
func NewRouteTraceBuilder(session *Session, userMessage string) *RouteTraceBuilder {
	if session == nil {
		return nil
	}
	now := time.Now()
	b := &RouteTraceBuilder{
		trace: &RouteTrace{
			TraceID:   session.GenerateRouteTraceID(),
			SessionID: session.SessionID,
			UserID:    session.UserID,
			Message:   truncateRouteText(userMessage),
			Status:    "ok",
			CreatedAt: now,
		},
	}
	b.rootID = b.addNodeLocked(RouteNode{
		Type:   RouteNodeUserMessage,
		Label:  "User message",
		Detail: truncateRouteText(userMessage),
		Status: RouteStatusOK,
	})
	b.spineTailID = b.rootID
	return b
}

// addNodeLocked appends a node and returns its generated id. The caller must
// hold b.mu (or be the constructor, before the builder is shared).
func (b *RouteTraceBuilder) addNodeLocked(n RouteNode) string {
	b.seq++
	n.ID = "n" + strconv.Itoa(b.seq)
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	b.trace.Nodes = append(b.trace.Nodes, n)
	return n.ID
}

func (b *RouteTraceBuilder) addEdgeLocked(from, to, label string) {
	if from == "" || to == "" {
		return
	}
	b.trace.Edges = append(b.trace.Edges, RouteEdge{From: from, To: to, Label: label})
}

// Decision records one Core LLM turn and links it onto the decision spine. The
// recorded tokens are added to the trace's running total.
func (b *RouteTraceBuilder) Decision(label, model string, tokens int, durationMs int64, status RouteNodeStatus, detail string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	id := b.addNodeLocked(RouteNode{
		Type:       RouteNodeDecision,
		Label:      label,
		Detail:     truncateRouteText(detail),
		Model:      model,
		Tokens:     tokens,
		DurationMs: durationMs,
		Status:     status,
	})
	b.addEdgeLocked(b.spineTailID, id, "")
	b.spineTailID = id
	b.currentDecID = id
	if tokens > 0 {
		b.trace.TotalTokens += tokens
	}
}

// Tool records a non-routing tool execution under the current decision.
func (b *RouteTraceBuilder) Tool(nodeType RouteNodeType, toolName, label, detail string, status RouteNodeStatus, durationMs int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	parent := b.currentDecID
	if parent == "" {
		parent = b.spineTailID
	}
	id := b.addNodeLocked(RouteNode{
		Type:       nodeType,
		Label:      label,
		Tool:       toolName,
		Detail:     truncateRouteText(detail),
		Status:     status,
		DurationMs: durationMs,
	})
	b.addEdgeLocked(parent, id, "")
}

// Dispatch records a forward of the message to a worker agent under the current
// decision — the Core's routing decision.
func (b *RouteTraceBuilder) Dispatch(agent, label, detail string, status RouteNodeStatus, durationMs int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	parent := b.currentDecID
	if parent == "" {
		parent = b.spineTailID
	}
	id := b.addNodeLocked(RouteNode{
		Type:       RouteNodeAgentDispatch,
		Label:      label,
		Agent:      agent,
		Detail:     truncateRouteText(detail),
		Status:     status,
		DurationMs: durationMs,
	})
	b.addEdgeLocked(parent, id, "routes to")
	b.lastRouteID = id
}

// SkipDispatch records a worker-agent call the Core deliberately did NOT execute
// because it had already dispatched earlier in the same turn. The Core dispatches
// only: once it forwards a message to one agent, that agent's answer is returned
// to the user verbatim and never re-enters the Core's LLM, so any additional
// call_agent_* calls the LLM emitted in the same turn would have their (expensive)
// results discarded. The Core skips them and records this node so the skip stays
// visible on the DAG.
//
// Unlike Dispatch it does not advance lastRouteID — the response still links from
// the agent that actually ran — and it records no tokens (none were consumed).
func (b *RouteTraceBuilder) SkipDispatch(agent, label, detail string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	parent := b.currentDecID
	if parent == "" {
		parent = b.spineTailID
	}
	id := b.addNodeLocked(RouteNode{
		Type:   RouteNodeAgentDispatch,
		Label:  label,
		Agent:  agent,
		Detail: truncateRouteText(detail),
		Status: RouteStatusSkipped,
	})
	b.addEdgeLocked(parent, id, "skipped")
}

// Escalate records a forward to a higher-tier agent, linked from the dispatch it
// escalated from.
func (b *RouteTraceBuilder) Escalate(agent, label, detail string, status RouteNodeStatus, durationMs int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	parent := b.lastRouteID
	if parent == "" {
		parent = b.currentDecID
	}
	id := b.addNodeLocked(RouteNode{
		Type:       RouteNodeEscalation,
		Label:      label,
		Agent:      agent,
		Detail:     truncateRouteText(detail),
		Status:     status,
		DurationMs: durationMs,
	})
	b.addEdgeLocked(parent, id, "escalates")
	b.lastRouteID = id
}

// Response records the terminal answer to the user. When fromDispatch is true it
// links from the agent the Core routed to (the Core returns that answer
// verbatim); otherwise it links from the final decision. Marks the trace
// finalized; later node additions are ignored.
func (b *RouteTraceBuilder) Response(text string, fromDispatch bool, status RouteNodeStatus) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	parent := b.currentDecID
	label := "answers"
	if fromDispatch && b.lastRouteID != "" {
		parent = b.lastRouteID
		label = "returns"
	}
	if parent == "" {
		parent = b.spineTailID
	}
	id := b.addNodeLocked(RouteNode{
		Type:   RouteNodeResponse,
		Label:  "Response to user",
		Detail: truncateRouteText(text),
		Status: status,
	})
	b.addEdgeLocked(parent, id, label)
	b.trace.Response = truncateRouteText(text)
	if status == RouteStatusError {
		b.trace.Status = "error"
	}
	b.finalized = true
}

// Fail records a terminal error node and marks the trace failed. No-op if a
// terminal node was already recorded.
func (b *RouteTraceBuilder) Fail(errMsg string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalized {
		return
	}
	parent := b.currentDecID
	if parent == "" {
		parent = b.spineTailID
	}
	id := b.addNodeLocked(RouteNode{
		Type:   RouteNodeResponse,
		Label:  "Error",
		Detail: truncateRouteText(errMsg),
		Status: RouteStatusError,
	})
	b.addEdgeLocked(parent, id, "error")
	b.trace.Status = "error"
	b.trace.Error = truncateRouteText(errMsg)
	b.finalized = true
}

// Build stamps the total wall-clock duration and returns the assembled trace.
// Safe on a nil builder (returns nil).
func (b *RouteTraceBuilder) Build(total time.Duration) *RouteTrace {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if total > 0 {
		b.trace.DurationMs = total.Milliseconds()
	}
	return b.trace
}
