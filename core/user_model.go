package core

// SetUserModelForAgents sets a per-user runtime model override on every managed
// agent engine (high/low/…), so a user's chosen model is used by the work agents.
// It deliberately does NOT change the Core orchestration model or the vision model
// — the Core router stays fixed while the lower agents are the switchable layer.
// Pass model="" to clear the override. Concurrency-safe (delegates to each Engine).
func (ch *CoreHandler) SetUserModelForAgents(userID, model string) {
	agents := ch.GetAgents()
	if agents == nil {
		return
	}
	for _, a := range agents.GetAll() {
		if a.Engine != nil {
			a.Engine.SetUserModel(userID, model)
		}
	}
}

// GetUserModelForAgents returns the per-user model override currently applied to the
// managed agents, or "" when none is set (the user is on the engine-default model).
// It reads from the first agent that has an override set.
func (ch *CoreHandler) GetUserModelForAgents(userID string) string {
	agents := ch.GetAgents()
	if agents == nil {
		return ""
	}
	for _, a := range agents.GetAll() {
		if a.Engine != nil {
			if ov := a.Engine.UserModelOverride(userID); ov != "" {
				return ov
			}
		}
	}
	return ""
}
