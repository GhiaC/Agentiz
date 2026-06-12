package core

import (
	"context"
	"strings"
	"time"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
)

// routeRecorderKey is the context key under which the per-message route-trace
// builder is carried. Carrying it in the context (like the user id) lets the
// LLM loop and the tool layer record into the same DAG without threading a
// builder through every signature.
type routeRecorderKey struct{}

// withRouteRecorder attaches the route-trace builder to ctx. A nil builder is
// fine — recorder lookups then return nil and every record call is a no-op.
func withRouteRecorder(ctx context.Context, b *model.RouteTraceBuilder) context.Context {
	return context.WithValue(ctx, routeRecorderKey{}, b)
}

// routeRecorderFrom returns the builder carried in ctx, or nil when tracing is
// not active. All *model.RouteTraceBuilder methods are nil-safe.
func routeRecorderFrom(ctx context.Context) *model.RouteTraceBuilder {
	b, _ := ctx.Value(routeRecorderKey{}).(*model.RouteTraceBuilder)
	return b
}

// agentDispatchLabel is the human-facing label for a dispatch/escalation node:
// the agent's display name when set, otherwise its registry name.
func agentDispatchLabel(agent *agentmanager.RegisteredAgent) string {
	if agent == nil {
		return "agent"
	}
	if agent.Config.DisplayName != "" {
		return agent.Config.DisplayName
	}
	return agent.Config.Name
}

// routeStatus maps an execution error to a node status.
func routeStatus(err error) model.RouteNodeStatus {
	if err != nil {
		return model.RouteStatusError
	}
	return model.RouteStatusOK
}

// persistRouteTrace finalizes the builder, writes the trace to the store
// (best-effort), and records metrics + a summary log line. It never fails the
// surrounding request: persistence problems are logged and swallowed.
func (ch *CoreHandler) persistRouteTrace(b *model.RouteTraceBuilder, total time.Duration) {
	trace := b.Build(total)
	if trace == nil {
		return
	}
	nodes := trace.NodeCount()

	store := ch.sessionHandler.GetStore()
	writer, ok := store.(interface {
		PutRouteTrace(*model.RouteTrace) error
	})
	if !ok {
		log.Log.Warnf("[CoreHandler] ⚠️  Store %T does not support route traces; skipping persist | TraceID: %s", store, trace.TraceID)
		return
	}
	if err := writer.PutRouteTrace(trace); err != nil {
		metrics.RouteTrace("error", nodes)
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to persist route trace | TraceID: %s | Error: %v", trace.TraceID, err)
		return
	}

	metrics.RouteTrace(trace.Status, nodes)
	dispatched := strings.Join(trace.DispatchedAgents(), ",")
	if dispatched == "" {
		dispatched = "-"
	}
	log.Log.Infof("[CoreHandler] 🧭 Route trace recorded | TraceID: %s | Nodes: %d | Dispatched: %s | Status: %s | Tokens: %d | DurationMs: %d",
		trace.TraceID, nodes, dispatched, trace.Status, trace.TotalTokens, trace.DurationMs)
}
