package debuger

// SystemInfo describes the running system's storage backends and runtime
// configuration. It is rendered on the debug dashboard so an operator can see at
// a glance what the agent is wired to — most importantly which database is in
// use (e.g. MongoDB vs SQLite) and where files are stored.
type SystemInfo struct {
	Version         string      // library version
	Database        BackendInfo // session/metadata store (SQLite, MongoDB, ...)
	FileStore       BackendInfo // user-file byte storage (local disk, custom, ...)
	TotalDocuments  int         // number of recorded user files (uploaded + generated)
	RegisteredTools int         // number of tools registered with the agent
	PlanningEnabled bool        // whether the planning layer is active
	More            []InfoKV    // extra detail rows shown under "more info"
}

// BackendInfo describes a single storage backend (a database or a file store).
type BackendInfo struct {
	Type     string // human label, e.g. "SQLite", "MongoDB", "Local Disk"
	Location string // path, host, or directory (credentials redacted)
}

// InfoKV is a label/value pair for display in the system-info panel.
type InfoKV struct {
	Key   string
	Value string
}
