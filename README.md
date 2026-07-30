# Agentize

> Build intelligent agentic services from hierarchical knowledge trees.

Agentize is a Go **library** for building agentic services. You give it a
filesystem-based knowledge tree and an LLM, and it manages users, sessions,
message processing, tool calls, multi-agent routing, summarization, persistence,
and an operational dashboard. It is embedded into a host application (it is *not*
a standalone CLI/server).

## ✨ Features

- 🌳 **Knowledge-tree navigation** — hierarchical nodes with automatic discovery and tool accumulation
- 🔐 **Node RBAC** — role/group/user permissions with inheritance (see [docs/AUTH_EXAMPLES.md](docs/AUTH_EXAMPLES.md))
- 🤖 **LLM message processing** — single-agent or multi-agent (Core router + worker agents)
- 💬 **Sessions & summarization** — persistent sessions with a background rolling-window summarizer
- 🗄️ **Pluggable storage** — SQLite (default) or MongoDB behind one `store.Store` interface
- 📎 **User files & image editing** — record uploads, `manage_files` tool, optional OpenRouter image edits
- 🌐 **Isolated browser automation** — optional Dockerized `browser-use` sidecar with asynchronous, session-owned jobs, load tracing, and user-deliverable screenshots
- ✅ **Human-approved tools** — every tool call can pause for a durable approve/reject decision before execution
- 🔀 **Deterministic workflows** — durable Core-tool DAGs without a planner LLM, including scheduled state machines
- ⏱️ **Memory-isolated schedules** — every prompt/workflow schedule owns a dedicated persistent session
- 📊 **Observability** — ~30 Prometheus metrics + two Grafana dashboards (see [docs/METRICS.md](docs/METRICS.md))
- 🛠️ **Debug dashboard** — users, sessions, browser loads/screenshots, schedules, workflow DAGs, messages, files, tool calls, routing DAGs, and summaries — behind admin auth

## 🚀 Installation

```bash
go get github.com/ghiac/agentize
```

## Quick start (1 — minimal single agent)

```go
package main

import (
	"context"
	"fmt"

	"github.com/ghiac/agentize"
	"github.com/ghiac/agentize/engine"
)

func main() {
	// Loads every node under ./knowledge (SQLite store + local file store by default).
	ag, err := agentize.New("./knowledge")
	if err != nil {
		panic(err)
	}

	// Configure the LLM. This also starts the summarization scheduler.
	if err := ag.UseLLMConfig(engine.LLMConfig{
		APIKey: "sk-...",          // or your OpenRouter/compatible key
		Model:  "openai/gpt-4o",
		// BaseURL: "https://openrouter.ai/api/v1", // optional
	}); err != nil {
		panic(err)
	}

	// One session per user, then process messages against it.
	session, err := ag.CreateSession("user-123")
	if err != nil {
		panic(err)
	}

	resp, tokens, err := ag.ProcessMessage(context.Background(), session.SessionID, "Hello!")
	if err != nil {
		panic(err)
	}
	fmt.Printf("reply=%q tokens=%d\n", resp, tokens)
}
```

## 2 — Persistence + scheduler (SQLite or MongoDB)

Open a store explicitly and pass it in. Switching backends is a one-line config
change.

```go
import (
	"github.com/ghiac/agentize"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/store"
)

// SQLite (default backend)
s, err := store.Open(store.Config{
	Backend:    "sqlite",
	SQLitePath: "./data/sessions.db", // ":memory:" for ephemeral
})

// …or MongoDB
// s, err := store.Open(store.Config{Backend: "mongodb", MongoURI: "mongodb://localhost:27017"})
if err != nil {
	panic(err)
}
defer s.Close()

ag, err := agentize.NewWithOptions("./knowledge", &agentize.Options{
	SessionStore: s,
	FileStoreDir: "./data/files",
})
if err != nil {
	panic(err)
}

_ = ag.UseLLMConfig(engine.LLMConfig{APIKey: "sk-...", Model: "openai/gpt-4o"})
// The background summarizer is now running; stop it on shutdown:
defer ag.StopScheduler()
```

The scheduler is tuned via env vars (all optional):

| Variable | Default | Meaning |
|---|---|---|
| `AGENTIZE_SCHEDULER_ENABLED` | `true` | Master switch |
| `AGENTIZE_SCHEDULER_CHECK_INTERVAL_MINUTES` | `5` | How often to scan sessions |
| `AGENTIZE_SCHEDULER_FIRST_THRESHOLD` | `5` | Messages before the first summarization |
| `AGENTIZE_SCHEDULER_SUBSEQUENT_MESSAGE_THRESHOLD` | `25` | Messages before re-summarizing |
| `AGENTIZE_SCHEDULER_SUMMARY_MODEL` | `openai/gpt-5-nano` | Model used for summaries |

## 3 — Callbacks / billing + debug routes with admin auth

Implement `engine.Callback` to meter usage and gate actions, mount the
`/agentize` routes on your Gin router, and protect them with admin credentials.

```go
import (
	"context"
	"fmt"

	"github.com/ghiac/agentize"
	"github.com/ghiac/agentize/engine"
	"github.com/gin-gonic/gin"
)

type billing struct{ /* your credit store */ }

// BeforeAction may return an error to BLOCK the call (e.g. quota exceeded).
func (b *billing) BeforeAction(ctx context.Context, e *engine.UsageEvent) error {
	return nil
}

// AfterAction records actual usage (tokens, model, duration).
func (b *billing) AfterAction(ctx context.Context, e *engine.UsageEvent) {
	fmt.Printf("usage user=%s model=%s in=%d out=%d\n",
		e.UserID, e.Model, e.InputTokens, e.OutputTokens)
}

func main() {
	ag, _ := agentize.New("./knowledge")
	_ = ag.UseLLMConfig(engine.LLMConfig{APIKey: "sk-...", Model: "openai/gpt-4o"})

	// Meter/gate every LLM, tool and agent-routing call.
	ag.GetEngine().Callback = &billing{}

	// Protect the dashboard + metrics. Without credentials a warning is logged
	// and the pages are open — never do that on an untrusted network.
	ag.SetAdminCredentials("admin", "change-me") // or AGENTIZE_ADMIN_USERNAME/PASSWORD

	router := gin.New()
	ag.RegisterRoutes(router) // mounts /agentize, /agentize/metrics, /agentize/debug/*, /agentize/health
	_ = router.Run(":8080")
}
```

Routes mounted by `RegisterRoutes` (all but `/agentize/health` and the login
endpoints require a signed-in admin once credentials are set):

| Route | Purpose |
|---|---|
| `GET /agentize/health` | Liveness (always open) |
| `GET /agentize/metrics` | Prometheus metrics (dedicated registry) |
| `GET /agentize` | Index |
| `GET /agentize/debug/*` | Users, sessions, schedules, workflow DAGs, messages, files, tool calls, routing DAGs, summarization logs |
| `GET /agentize/docs`, `/agentize/graph` | Generated docs + knowledge-graph visualization |

See [docs/SECURITY.md](docs/SECURITY.md) for the threat model and
[docs/OPERATIONS.md](docs/OPERATIONS.md) for running it in production.

The optional autonomous browser module is documented in
[docs/BROWSER_USE.md](docs/BROWSER_USE.md).

## 4 — Multi-agent (Core router + worker agents)

For routing between specialized worker agents (e.g. a cheap "low" agent and a
capable "high" agent), compose an `AgentManager` and a `CoreHandler`. Each worker
is an `*engine.Engine`; the Core LLM decides which to delegate to.

```go
import (
	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/core"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/review"
)

sessionHandler := model.NewSessionHandler(store, model.DefaultSessionHandlerConfig())
agents := agentmanager.New(sessionHandler)

// Register each worker agent (engine construction is detailed in CORE_AGENT.md).
_ = agents.Register(agentmanager.AgentConfig{
	Name:      "high",
	Model:     "openai/gpt-4o",
	AgentType: model.AgentTypeHigh,
	CostTier:  agentmanager.CostTierHigh,
}, highEngine)

cfg := core.DefaultCoreHandlerConfig()
cfg.CoreLLMConfig = engine.LLMConfig{APIKey: "sk-...", Model: "openai/gpt-5-nano"}
ch := core.NewCoreHandler(sessionHandler, agents, cfg)
_ = ch.UseLLMConfig(cfg.CoreLLMConfig)
ch.SetCallback(myBillingCallback) // propagates to every worker engine
approvals := review.New(store, nil) // set a Notifier so your UI can ask the user
ch.SetToolApprovalManager(approvals) // gates Core + current/future worker tools

reply, err := ch.ProcessMessage(context.Background(), "user-123", "Help me research X")
```

The full multi-agent wiring (engine fields, knowledge sets, web search, approvals)
is documented in [docs/CORE_AGENT.md](docs/CORE_AGENT.md) and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## 📁 Knowledge-tree structure

```
knowledge/
  root/
    node.md          # Markdown content / instructions
    node.yaml        # Node metadata, routing, and auth
    tools.json       # Tools available at this level
    next/            # Child nodes
      node.md
      node.yaml
      tools.json
```

### `node.yaml`

```yaml
id: "root"
title: "Root Node"
description: "Entry point for the knowledge tree"

# RBAC — see docs/AUTH_EXAMPLES.md
auth:
  inherit: true
  default: { perms: "r" }
  roles:
    admin: { perms: "rwx" }

routing:
  mode: "sequential"   # or "parallel" / "conditional"

memory:
  persist: ["summary", "facts"]
```

### `tools.json`

```json
{
  "tools": [
    {
      "name": "search_docs",
      "description": "Search in documentation",
      "input_schema": {
        "type": "object",
        "properties": { "q": { "type": "string" } },
        "required": ["q"]
      }
    }
  ]
}
```

## ⚙️ Configuration env vars

| Variable | Effect |
|---|---|
| `AGENTIZE_ADMIN_USERNAME` / `AGENTIZE_ADMIN_PASSWORD` | Protect the `/agentize` dashboard + metrics |
| `AGENTIZE_LOG_LEVEL` | `debug`\|`info`\|`warn`\|`error` (default `info`) |
| `AGENTIZE_LOG_FORMAT` | `text` (default) or `json` |
| `AGENTIZE_METRICS_DEFAULT_REGISTRY` | `1` exposes the full global Prometheus registry |
| `AGENTIZE_SCHEDULER_*` | Summarization scheduler tuning (see above) |

## 🧪 Testing

```bash
make test         # go test ./...
make test-race    # go test -race ./...   (matches CI)
make test-full    # format + vet + build + tests + coverage (scripts/test.sh)
```

CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs `gofmt`,
`go vet`, `go build`, and `go test -race -coverprofile` on every push and PR.
The MongoDB store conformance suite runs against a real MongoDB via
testcontainers and self-skips when Docker is unavailable.

## 📚 Documentation

| Doc | Topic |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System architecture |
| [docs/CORE_AGENT.md](docs/CORE_AGENT.md) | Core router + system-prompt assembly |
| [docs/REVIEWS.md](docs/REVIEWS.md) | Human approval for tool calls |
| [docs/WORKFLOWS.md](docs/WORKFLOWS.md) | Deterministic Core workflow DAGs and scheduled state machines |
| [docs/TASK_SCHEDULER.md](docs/TASK_SCHEDULER.md) | Dedicated-session prompt and workflow schedules |
| [docs/ROUTING_DAG.md](docs/ROUTING_DAG.md) | Routing-decision traces |
| [docs/METRICS.md](docs/METRICS.md) | Prometheus metrics + Grafana dashboards |
| [docs/SECURITY.md](docs/SECURITY.md) | Threat model, debug-route exposure, PII |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Backup, scaling, alerts, graceful shutdown |
| [docs/AUTH_EXAMPLES.md](docs/AUTH_EXAMPLES.md) | Node RBAC examples (EN / [FA](docs/AUTH_EXAMPLES_FA.md)) |
| [CHANGELOG.md](CHANGELOG.md) | Changes & deprecations |

## 🏗️ Repository layout

```
agentize/
├── model/        # Core data structures (Node, Session, Tool, Auth, User, Message)
├── fsrepo/       # Filesystem repository for loading nodes
├── core/         # Core router (multi-agent orchestration)
├── agentmanager/ # Worker-agent registry
├── engine/       # Per-agent engine (LLM loop, tools, scheduler)
├── review/       # Durable, UI-agnostic human approvals
├── store/        # Persistence (SQLite / MongoDB) behind one interface
├── filestore/    # Pluggable byte storage for user files
├── metrics/      # Prometheus instrumentation
├── debuger/      # Debug dashboard pages
├── documents/    # Documentation generation
└── visualize/    # ECharts knowledge-graph visualization
```
