package agentmanager

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
)

// CostTier represents the cost level of an agent's LLM model.
type CostTier string

const (
	CostTierLow    CostTier = "low"
	CostTierMedium CostTier = "medium"
	CostTierHigh   CostTier = "high"
)

// KnowledgeSet represents a domain of knowledge that can be attached to an agent.
type KnowledgeSet struct {
	Name        string
	Description string
	Model       string // optional override model for this knowledge domain
}

// AgentConfig holds the configuration for registering an agent.
type AgentConfig struct {
	Name         string          // unique key: "researcher", "coder", "assistant"
	DisplayName  string          // human-readable name (e.g., Persian): "هوش سطح بالا"
	Description  string          // what this agent does (injected into Core's system prompt)
	Model        string          // LLM model: "openai/gpt-5-nano"
	AgentType    model.AgentType // maps to session management key
	CostTier     CostTier
	Capabilities []string // e.g., ["coding", "research", "translation"]
	Knowledge    []KnowledgeSet
}

// RegisteredAgent pairs an AgentConfig with its Engine instance.
type RegisteredAgent struct {
	Config AgentConfig
	Engine *engine.Engine
}

// AgentManager is a dynamic registry of agents. It replaces the hardcoded
// userAgentHigh / userAgentLow fields in the old CoreHandler.
type AgentManager struct {
	agents         map[string]*RegisteredAgent
	sessionHandler *model.SessionHandler
	mu             sync.RWMutex
}

// New creates an empty AgentManager. sessionHandler may be nil if session
// context prompts are not needed.
func New(sessionHandler *model.SessionHandler) *AgentManager {
	return &AgentManager{
		agents:         make(map[string]*RegisteredAgent),
		sessionHandler: sessionHandler,
	}
}

// Register adds an agent to the manager. The agent's Config.Name must be
// unique. If a SessionHandler is configured, the agent's display name is
// also registered there for prompt formatting.
func (am *AgentManager) Register(config AgentConfig, eng *engine.Engine) error {
	if config.Name == "" {
		return fmt.Errorf("agent name is required")
	}
	if eng == nil {
		return fmt.Errorf("engine is required for agent %q", config.Name)
	}
	if config.AgentType == "" {
		config.AgentType = model.AgentType(config.Name)
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	if _, exists := am.agents[config.Name]; exists {
		return fmt.Errorf("agent %q is already registered", config.Name)
	}

	am.agents[config.Name] = &RegisteredAgent{
		Config: config,
		Engine: eng,
	}

	if am.sessionHandler != nil {
		am.sessionHandler.RegisterAgentDisplayName(config.AgentType, config.DisplayName)
	}

	return nil
}

// Get returns a registered agent by name.
func (am *AgentManager) Get(name string) (*RegisteredAgent, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	a, ok := am.agents[name]
	return a, ok
}

// GetAll returns all registered agents sorted by name for deterministic ordering.
func (am *AgentManager) GetAll() []*RegisteredAgent {
	am.mu.RLock()
	defer am.mu.RUnlock()

	result := make([]*RegisteredAgent, 0, len(am.agents))
	for _, a := range am.agents {
		result = append(result, a)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Config.Name < result[j].Config.Name
	})
	return result
}

// GetByTier returns agents that match the given cost tier.
func (am *AgentManager) GetByTier(tier CostTier) []*RegisteredAgent {
	am.mu.RLock()
	defer am.mu.RUnlock()

	var result []*RegisteredAgent
	for _, a := range am.agents {
		if a.Config.CostTier == tier {
			result = append(result, a)
		}
	}
	return result
}

// GetCheapest returns the agent with the lowest cost tier. When multiple
// agents share the lowest tier, the first one alphabetically is returned.
func (am *AgentManager) GetCheapest() *RegisteredAgent {
	am.mu.RLock()
	defer am.mu.RUnlock()

	tierOrder := []CostTier{CostTierLow, CostTierMedium, CostTierHigh}
	for _, tier := range tierOrder {
		var candidates []*RegisteredAgent
		for _, a := range am.agents {
			if a.Config.CostTier == tier {
				candidates = append(candidates, a)
			}
		}
		if len(candidates) > 0 {
			sort.Slice(candidates, func(i, j int) bool {
				return candidates[i].Config.Name < candidates[j].Config.Name
			})
			return candidates[0]
		}
	}
	return nil
}

// Names returns sorted agent names.
func (am *AgentManager) Names() []string {
	am.mu.RLock()
	defer am.mu.RUnlock()

	names := make([]string, 0, len(am.agents))
	for name := range am.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Count returns the number of registered agents.
func (am *AgentManager) Count() int {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return len(am.agents)
}

// SetCallback propagates a Callback to every registered engine.
func (am *AgentManager) SetCallback(cb engine.Callback) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	for _, a := range am.agents {
		if a.Engine != nil {
			a.Engine.Callback = cb
		}
	}
}

// IsReady returns true when all registered engines report their DB as ready.
func (am *AgentManager) IsReady() bool {
	am.mu.RLock()
	defer am.mu.RUnlock()

	for _, a := range am.agents {
		if a.Engine != nil && !a.Engine.IsDBReady() {
			return false
		}
	}
	return true
}
