package agentize

import (
	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/core"
)

// ShareFileManagerWithAgents wires Agentize's byte store into every current and
// future worker Engine registered with manager. Worker Engines must use the same
// session metadata store as Agentize so files remain one user-scoped collection
// across all agent sessions.
func (ag *Agentize) ShareFileManagerWithAgents(manager *agentmanager.AgentManager) {
	if ag == nil || manager == nil {
		return
	}
	manager.SetFileStore(ag.engine.Files)
}

// ShareFileManagerWithCore completes file wiring for a Core-based chatbot:
// worker agents receive the shared byte store and inbound Core images are
// recorded in the same user-scoped file collection.
func (ag *Agentize) ShareFileManagerWithCore(handler *core.CoreHandler) {
	if ag == nil || handler == nil {
		return
	}
	ag.ShareFileManagerWithAgents(handler.GetAgents())
	handler.SetFileRecorder(ag.RecordUserFile)
}
