package planning

import (
	"context"
	"fmt"
	"time"
)

const seedUserID = "__seed__"
const seedSessionID = "__seed_session__"
const seedIDPrefix = "seed-"

// SeedConfig controls initial plan seeding. When SeedInitialPlans is true (default),
// EnsureSeedPlans creates template plans with different step-type combinations on first run.
type SeedConfig struct {
	// SeedInitialPlans enables seeding template plans when the store is empty. Default true.
	SeedInitialPlans bool
}

// DefaultSeedConfig returns config with seeding enabled.
func DefaultSeedConfig() SeedConfig {
	return SeedConfig{SeedInitialPlans: true}
}

// EnsureSeedPlans runs on first run: if store has no plans and config allows, creates and saves
// template plans with different permutations of capabilities (llm_call, tool_call, agent_delegate,
// parallel, conditional, collect) so the dashboard has data to display.
func EnsureSeedPlans(ctx context.Context, store PlanStore, config SeedConfig) error {
	if store == nil || !config.SeedInitialPlans {
		return nil
	}
	all, err := store.List(ctx, "")
	if err != nil {
		return err
	}
	// Already have plans (e.g. from previous run or DB load) — skip seed
	if len(all) > 0 {
		return nil
	}
	templates := buildSeedPlans()
	for _, p := range templates {
		if err := store.Save(ctx, p); err != nil {
			return fmt.Errorf("planning: seed save %s: %w", p.ID, err)
		}
	}
	return nil
}

func buildSeedPlans() []*Plan {
	now := time.Now()
	plans := []*Plan{
		// 1: LLM only
		mustSeedPlan(seedIDPrefix+"llm-only", "فقط LLM", []*Step{
			{ID: "s1", Type: StepLLMCall, Name: "LLM", Description: "پردازش متن", Config: StepConfig{Prompt: "خلاصه کن"}, Status: StepCompleted, Result: &StepResult{Output: "خروجی نمونه", TokensUsed: 10}},
		}, now),
		// 2: LLM + tool
		mustSeedPlan(seedIDPrefix+"llm-tool", "LLM و ابزار", []*Step{
			{ID: "s1", Type: StepLLMCall, Name: "LLM", Config: StepConfig{Prompt: "تحلیل"}, Status: StepCompleted, Result: &StepResult{Output: "تحلیل انجام شد", TokensUsed: 20}},
			{ID: "s2", Type: StepToolCall, Name: "Tool", DependsOn: []string{"s1"}, Config: StepConfig{ToolName: "search"}, Status: StepCompleted, Result: &StepResult{Output: "نتیجه جستجو", TokensUsed: 0}},
		}, now),
		// 3: LLM + agent_delegate
		mustSeedPlan(seedIDPrefix+"llm-agent", "LLM و واگذاری به Agent", []*Step{
			{ID: "s1", Type: StepLLMCall, Name: "LLM", Config: StepConfig{Prompt: "تصمیم"}, Status: StepCompleted, Result: &StepResult{Output: "واگذار به agent", TokensUsed: 15}},
			{ID: "s2", Type: StepAgentDelegate, Name: "Agent", DependsOn: []string{"s1"}, Config: StepConfig{AgentType: "researcher", AgentInput: "جستجو"}, Status: StepCompleted, Result: &StepResult{Output: "پاسخ agent", TokensUsed: 50}},
		}, now),
		// 4: parallel
		mustSeedPlan(seedIDPrefix+"parallel", "موازی", []*Step{
			{ID: "s1", Type: StepLLMCall, Name: "LLM-1", Config: StepConfig{Prompt: "کار ۱"}, Status: StepCompleted, Result: &StepResult{Output: "خروجی ۱", TokensUsed: 10}},
			{ID: "s2", Type: StepToolCall, Name: "Tool-1", Config: StepConfig{ToolName: "fetch"}, Status: StepCompleted, Result: &StepResult{Output: "داده ۱", TokensUsed: 0}},
		}, now),
		// 5: conditional
		mustSeedPlan(seedIDPrefix+"conditional", "شرطی", []*Step{
			{ID: "s1", Type: StepConditional, Name: "Branch", Config: StepConfig{}, Condition: &Condition{Field: "status", Operator: "eq", Value: "ok"}, Status: StepCompleted, Result: &StepResult{Output: "شاخه انتخاب شده", TokensUsed: 0}},
		}, now),
		// 6: LLM + tool + agent
		mustSeedPlan(seedIDPrefix+"llm-tool-agent", "LLM و ابزار و Agent", []*Step{
			{ID: "s1", Type: StepLLMCall, Name: "LLM", Config: StepConfig{Prompt: "برنامه‌ریزی"}, Status: StepCompleted, Result: &StepResult{Output: "برنامه", TokensUsed: 25}},
			{ID: "s2", Type: StepToolCall, Name: "Tool", DependsOn: []string{"s1"}, Config: StepConfig{ToolName: "web_search"}, Status: StepCompleted, Result: &StepResult{Output: "جستجو", TokensUsed: 0}},
			{ID: "s3", Type: StepAgentDelegate, Name: "Agent", DependsOn: []string{"s2"}, Config: StepConfig{AgentType: "coder", AgentInput: "خلاصه"}, Status: StepCompleted, Result: &StepResult{Output: "خلاصه نهایی", TokensUsed: 40}},
		}, now),
		// 7: collect
		mustSeedPlan(seedIDPrefix+"collect", "جمع‌آوری", []*Step{
			{ID: "s1", Type: StepCollect, Name: "Collect", Config: StepConfig{}, DependsOn: []string{}, Status: StepCompleted, Result: &StepResult{Output: "جمع‌بندی نتایج", TokensUsed: 5}},
		}, now),
	}
	return plans
}

func mustSeedPlan(id, input string, steps []*Step, t time.Time) *Plan {
	completed := t
	return &Plan{
		ID:          id,
		UserID:      seedUserID,
		SessionID:   seedSessionID,
		Input:       input,
		Steps:       steps,
		Status:      PlanCompleted,
		CreatedAt:   t,
		UpdatedAt:   t,
		CompletedAt: &completed,
		Output:      "پلن نمونه برای نمایش در داشبورد.",
		Version:     1,
	}
}
