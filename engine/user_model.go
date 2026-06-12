package engine

import "strings"

// SetUserModel sets a per-user model override that takes precedence over the
// engine-configured model (llmConfig.Model) for subsequent requests from this user.
// Pass model="" to clear the override (revert to the engine default). Concurrency-safe.
//
// This is the runtime hook that lets a downstream application switch a user's model
// live — without reconfiguring the engine or restarting — while the engine, its
// knowledge tree, and its tool set stay fixed.
func (e *Engine) SetUserModel(userID, model string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	model = strings.TrimSpace(model)
	e.userModelOverridesMu.Lock()
	defer e.userModelOverridesMu.Unlock()
	if model == "" {
		delete(e.userModelOverrides, userID)
		return
	}
	if e.userModelOverrides == nil {
		e.userModelOverrides = make(map[string]string)
	}
	e.userModelOverrides[userID] = model
}

// UserModelOverride returns the per-user model override for userID, or "" when none
// is set (in which case the engine-configured model is used). Concurrency-safe.
func (e *Engine) UserModelOverride(userID string) string {
	e.userModelOverridesMu.RLock()
	defer e.userModelOverridesMu.RUnlock()
	return e.userModelOverrides[strings.TrimSpace(userID)]
}
