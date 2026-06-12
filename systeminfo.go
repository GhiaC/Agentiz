package agentize

import (
	"fmt"
	"runtime"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/filestore"
)

// SystemInfo reports the storage backends and runtime configuration the agent is
// wired to. It powers the "System Info" panel on the debug dashboard so an
// operator can quickly trace which database (MongoDB vs SQLite) and file store
// are in use. Safe to call at any time; values are best-effort snapshots.
//
// The database backend describes itself via store.Store.BackendInfo(), so any
// backend — including future ones — is reported without changes here.
func (ag *Agentize) SystemInfo() debuger.SystemInfo {
	st := ag.GetSessionStore()
	info := debuger.SystemInfo{
		Version:         Version(),
		Database:        st.BackendInfo(),
		FileStore:       describeFileStore(ag.engine.Files),
		RegisteredTools: len(ag.GetRegisteredTools()),
		PlanningEnabled: ag.GetPlanStore() != nil,
	}

	// Count recorded user files (uploaded + generated), best-effort.
	if files, err := st.GetAllUserFiles(); err == nil {
		info.TotalDocuments = len(files)
	}

	imageEditor := "disabled"
	if ag.engine.ImageEditor != nil {
		imageEditor = "configured"
	}
	ag.schedulerMu.RLock()
	schedulerRunning := ag.scheduler != nil
	ag.schedulerMu.RUnlock()

	info.More = []debuger.InfoKV{
		{Key: "Go runtime", Value: runtime.Version()},
		{Key: "Platform", Value: runtime.GOOS + "/" + runtime.GOARCH},
		{Key: "Knowledge nodes", Value: fmt.Sprintf("%d", len(ag.GetAllNodes()))},
		{Key: "Scheduler", Value: boolLabel(schedulerRunning, "running", "stopped")},
		{Key: "Image editor", Value: imageEditor},
	}

	// Count recorded routing DAGs (one per Core-processed message), best-effort.
	if traces, err := st.GetAllRouteTraces(); err == nil {
		info.More = append(info.More, debuger.InfoKV{Key: "Route traces", Value: fmt.Sprintf("%d", len(traces))})
	}

	// Application-supplied extra rows (e.g. config provenance).
	if ag.extraSystemInfoProvider != nil {
		info.More = append(info.More, ag.extraSystemInfoProvider()...)
	}
	return info
}

// describeFileStore identifies the concrete file (byte) store backend. Unlike the
// database, the file store is not part of store.Store, so it is detected here.
func describeFileStore(fs filestore.FileStore) debuger.BackendInfo {
	switch v := fs.(type) {
	case nil:
		return debuger.BackendInfo{Type: "None (file features disabled)"}
	case *filestore.LocalFileStore:
		return debuger.BackendInfo{Type: "Local Disk", Location: v.BaseDir()}
	default:
		return debuger.BackendInfo{Type: fmt.Sprintf("%T", fs)}
	}
}

// boolLabel returns yes when b is true, otherwise no.
func boolLabel(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
