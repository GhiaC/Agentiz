package debuger

import (
	"fmt"

	"github.com/ghiac/agentize/model"
)

// UserBillingHTMLProvider returns HTML fragment for a user's billing/credit summary (optional; used on user detail page).
type UserBillingHTMLProvider func(userID string) (html string, err error)

// CoreSystemPromptProvider returns the Core agent's assembled system-prompt array
// for a user as labeled sections, for the "Core System Prompt" card on the user
// detail page. Wire it to a live Core via Agentize.SetCoreSystemPromptProvider
// (e.g. coreHandler.SystemPromptSectionsFor); when unset, Agentize installs a
// store-only preview as the default.
type CoreSystemPromptProvider func(userID string) ([]model.PromptSection, error)

// DebugHandler provides HTML debugging interface for SessionStore
type DebugHandler struct {
	store                    model.SessionStore
	schedulerConfig          *SchedulerConfig
	userBillingHTMLProvider  UserBillingHTMLProvider
	coreSystemPromptProvider CoreSystemPromptProvider
	// coreSystemPromptIsPreview marks the wired provider as a store-only
	// reconstruction (no live Core), so the page can flag it as a preview.
	coreSystemPromptIsPreview bool
}

// NewDebugHandler creates a new debug handler for a SessionStore
func NewDebugHandler(store model.SessionStore) (*DebugHandler, error) {
	// Check if store implements DebugStore interface
	if _, ok := store.(DebugStore); !ok {
		return nil, fmt.Errorf("store does not implement DebugStore interface")
	}
	return &DebugHandler{store: store}, nil
}

// NewDebugHandlerWithConfig creates a new debug handler with scheduler configuration
func NewDebugHandlerWithConfig(store model.SessionStore, config *SchedulerConfig) (*DebugHandler, error) {
	handler, err := NewDebugHandler(store)
	if err != nil {
		return nil, err
	}
	handler.schedulerConfig = config
	return handler, nil
}

// SetUserBillingHTMLProvider sets the optional provider for user billing HTML on the user detail page.
func (h *DebugHandler) SetUserBillingHTMLProvider(fn UserBillingHTMLProvider) {
	h.userBillingHTMLProvider = fn
}

// GetUserBillingHTML returns the billing HTML for a user if a provider is set.
func (h *DebugHandler) GetUserBillingHTML(userID string) (string, error) {
	if h.userBillingHTMLProvider == nil {
		return "", nil
	}
	return h.userBillingHTMLProvider(userID)
}

// SetCoreSystemPromptProvider sets the provider used by the "Core System Prompt"
// card on the user detail page. isPreview marks it as a store-only
// reconstruction (no live Core) so the page can label it accordingly.
func (h *DebugHandler) SetCoreSystemPromptProvider(fn CoreSystemPromptProvider, isPreview bool) {
	h.coreSystemPromptProvider = fn
	h.coreSystemPromptIsPreview = isPreview
}

// CoreSystemPromptIsPreview reports whether the wired Core system-prompt provider
// is a store-only preview rather than a live Core.
func (h *DebugHandler) CoreSystemPromptIsPreview() bool {
	return h.coreSystemPromptIsPreview
}

// GetCoreSystemPrompt returns the Core's system-prompt sections for a user, or
// (nil, nil) when no provider is wired.
func (h *DebugHandler) GetCoreSystemPrompt(userID string) ([]model.PromptSection, error) {
	if h.coreSystemPromptProvider == nil {
		return nil, nil
	}
	return h.coreSystemPromptProvider(userID)
}

// SetSchedulerConfig sets the scheduler configuration
func (h *DebugHandler) SetSchedulerConfig(config *SchedulerConfig) {
	h.schedulerConfig = config
}

// GetSchedulerConfig returns the scheduler configuration
func (h *DebugHandler) GetSchedulerConfig() *SchedulerConfig {
	return h.schedulerConfig
}

// GetStore returns the underlying store as DebugStore
func (h *DebugHandler) GetStore() DebugStore {
	return h.store.(DebugStore)
}

// GetSessionStore returns the underlying model.SessionStore
func (h *DebugHandler) GetSessionStore() model.SessionStore {
	return h.store
}
