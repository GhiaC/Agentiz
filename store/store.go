package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
)

// Store is the single, complete persistence interface for Agentize. Every
// backend — SQLite (SQLiteStore), MongoDB (MongoDBStore), and the cached wrapper
// (DBStore) — implements ALL of it, enforced at compile time below.
//
// The behavior of each method is part of the contract: the backends MUST be
// observationally identical so that switching between SQLite and MongoDB is
// transparent. The conformance test suite (conformance_test.go) runs the same
// assertions against every backend to guarantee this.
//
// Contract that ALL backends guarantee:
//   - Optional "get by id" lookups — GetUser, GetCoreSession, GetUserFile,
//     GetToolCallByID, GetToolCallByToolID — return (nil, nil) when the entity
//     does not exist; they do NOT return an error for "not found".
//   - Get(sessionID) returns an error when the session does not exist, because a
//     session is required by its callers (this is the one deliberate exception
//     to the rule above).
//   - Put* methods are upserts: insert when absent, replace when present, keyed
//     by the entity's primary id.
//   - List/Get*By* orderings are fixed and documented on each method; both
//     backends sort identically.
//   - Timestamps round-trip at one-second precision on every backend.
//   - Visited-node tracking is user-scoped and in-memory only (never persisted),
//     identically on every backend.
type Store interface {
	// Sessions: Get, Put, Delete, List, GetNextSessionSeq.
	model.SessionStore
	// Read/aggregate operations and DeleteUserData used by the debug UI.
	debuger.DebugStore

	// --- Core (per-user singleton) sessions ---

	// GetCoreSession returns the user's core session, or (nil, nil) if none.
	GetCoreSession(userID string) (*model.Session, error)
	// PutCoreSession upserts the user's single core session.
	PutCoreSession(session *model.Session) error

	// --- Writers not covered by DebugStore ---

	PutUser(user *model.User) error
	GetOrCreateUser(userID string) (*model.User, error)
	PutMessage(message *model.Message) error
	AddOpenedFile(openedFile *model.OpenedFile) error
	// CloseOpenedFile marks the currently-open record for (sessionID, filePath)
	// closed. It is a no-op when the file is not currently open.
	CloseOpenedFile(sessionID string, filePath string) error
	// GetCurrentlyOpenedFilesBySession returns still-open files for a session,
	// oldest first.
	GetCurrentlyOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error)
	PutUserFile(f *model.UserFile) error
	// GetUserFile returns the file by id, or (nil, nil) when not found.
	GetUserFile(fileID string) (*model.UserFile, error)
	DeleteUserFile(fileID string) error
	PutToolCall(toolCall *model.ToolCall) error
	UpdateToolCallResponse(toolID string, response string, execErr error) error

	// PutRouteTrace upserts a Core routing-decision DAG for one user message.
	// The read side lives on debuger.DebugStore (GetRouteTrace* / GetAllRouteTraces).
	PutRouteTrace(trace *model.RouteTrace) error

	// --- Visited-node tracking (user-scoped, in-memory, not persisted) ---

	AddVisitedNode(userID string, nodeDigest *model.NodeDigest)
	GetVisitedNodes(userID string) map[string]*model.NodeDigest
	GetVisitedNodePaths(userID string) []string
	HasVisitedNode(userID string, nodePath string) bool
	ClearVisitedNodes(userID string)

	// --- Lifecycle & introspection ---

	// Close releases backend resources (DB handle / client connection).
	Close() error
	// BackendInfo describes the backend for diagnostics and the debug dashboard.
	BackendInfo() debuger.BackendInfo
}

// Compile-time guarantee that every backend implements the full Store contract.
// If a backend ever drifts (a missing or mis-typed method), the build fails here
// instead of surfacing as a runtime type-assertion failure.
var (
	_ Store = (*SQLiteStore)(nil)
	_ Store = (*MongoDBStore)(nil)
	_ Store = (*DBStore)(nil)
)

// Config selects and configures a storage backend for Open.
type Config struct {
	// Backend is "sqlite" (default) or "mongodb".
	Backend string
	// SQLitePath is the SQLite database file path. Empty defaults to
	// "./data/sessions.db"; use ":memory:" for an ephemeral in-memory store.
	SQLitePath string
	// MongoURI is the MongoDB connection URI (required for the mongodb backend).
	MongoURI string
	// MongoDatabase overrides the MongoDB database name (default "agentize").
	MongoDatabase string
}

// Open creates a Store for the configured backend. It is the single entry point
// an application should use so that switching databases is a one-line config
// change rather than a code change.
func Open(cfg Config) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Backend)) {
	case "", "sqlite":
		path := cfg.SQLitePath
		if path == "" {
			path = "./data/sessions.db"
		}
		return NewDBStoreWithPath(path)
	case "mongodb", "mongo":
		if cfg.MongoURI == "" {
			return nil, fmt.Errorf("store: MongoURI is required for the mongodb backend")
		}
		mcfg := DefaultMongoDBStoreConfig()
		mcfg.URI = cfg.MongoURI
		if cfg.MongoDatabase != "" {
			mcfg.Database = cfg.MongoDatabase
		}
		return NewMongoDBStore(mcfg)
	default:
		return nil, fmt.Errorf("store: unknown backend %q (want \"sqlite\" or \"mongodb\")", cfg.Backend)
	}
}

// sortOpenedFilesByOpenedAtAsc orders opened files oldest-first, the ordering
// the SQLite backend produces via "ORDER BY opened_at ASC". Both backends sort
// identically so callers see the same order regardless of database.
func sortOpenedFilesByOpenedAtAsc(files []*model.OpenedFile) {
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].OpenedAt.Before(files[j].OpenedAt)
	})
}

// sqlitePathLabel makes a SQLite path human-friendly for display.
func sqlitePathLabel(path string) string {
	if path == "" || path == ":memory:" {
		return "in-memory"
	}
	return path
}

// redactURI strips any "user:pass@" credentials from a connection URI so the
// backend's location can be displayed without leaking secrets. It isolates the
// authority (between "://" and the first "/") and replaces userinfo with "***".
func redactURI(uri string) string {
	if uri == "" {
		return ""
	}
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri
	}
	scheme := uri[:i+3]
	rest := uri[i+3:]
	authority := rest
	tail := ""
	if slash := strings.Index(rest, "/"); slash >= 0 {
		authority = rest[:slash]
		tail = rest[slash:]
	}
	if at := strings.Index(authority, "@"); at >= 0 {
		authority = "***@" + authority[at+1:]
	}
	return scheme + authority + tail
}
