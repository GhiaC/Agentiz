package store

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
)

// meteredStore wraps a Store and records the latency of each database-backed
// operation as agentize_store_query_duration_seconds{operation,backend}.
//
// It embeds the Store interface, so every method is a straight pass-through; the
// methods below add only a timing observation. The in-memory visited-node
// helpers and the trivial accessors (Close, BackendInfo) are intentionally NOT
// timed — they do no I/O — and fall through to the embedded Store unchanged.
type meteredStore struct {
	Store
	backend string
}

// NewMetered wraps s so its database operations are timed. The backend label is
// derived from s.BackendInfo().Type (e.g. "sqlite", "mongodb"). Wrapping an
// already-metered store returns it unchanged, so instrumentation is never
// doubled. A nil store is returned as-is.
func NewMetered(s Store) Store {
	if s == nil {
		return nil
	}
	if _, ok := s.(*meteredStore); ok {
		return s
	}
	backend := strings.ToLower(strings.TrimSpace(s.BackendInfo().Type))
	if backend == "" {
		backend = "unknown"
	}
	return &meteredStore{Store: s, backend: backend}
}

// observe records one operation's latency. The intended use is
//
//	defer m.observe("Get", time.Now())
//
// where time.Now() is evaluated when the defer statement runs (the start) and
// the observation is taken when the wrapped call returns.
func (m *meteredStore) observe(op string, start time.Time) {
	metrics.StoreQuery(op, m.backend, time.Since(start))
}

// --- Sessions (model.SessionStore) ---

func (m *meteredStore) Get(sessionID string) (*model.Session, error) {
	defer m.observe("Get", time.Now())
	return m.Store.Get(sessionID)
}

func (m *meteredStore) Put(session *model.Session) error {
	defer m.observe("Put", time.Now())
	return m.Store.Put(session)
}

func (m *meteredStore) Delete(sessionID string) error {
	defer m.observe("Delete", time.Now())
	return m.Store.Delete(sessionID)
}

func (m *meteredStore) List(userID string) ([]*model.Session, error) {
	defer m.observe("List", time.Now())
	return m.Store.List(userID)
}

func (m *meteredStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	defer m.observe("GetNextSessionSeq", time.Now())
	return m.Store.GetNextSessionSeq(userID, agentType)
}

// --- Core (per-user singleton) sessions ---

func (m *meteredStore) GetCoreSession(userID string) (*model.Session, error) {
	defer m.observe("GetCoreSession", time.Now())
	return m.Store.GetCoreSession(userID)
}

func (m *meteredStore) PutCoreSession(session *model.Session) error {
	defer m.observe("PutCoreSession", time.Now())
	return m.Store.PutCoreSession(session)
}

// --- Users / messages / files / tool calls / traces (writers) ---

func (m *meteredStore) PutUser(user *model.User) error {
	defer m.observe("PutUser", time.Now())
	return m.Store.PutUser(user)
}

func (m *meteredStore) GetOrCreateUser(userID string) (*model.User, error) {
	defer m.observe("GetOrCreateUser", time.Now())
	return m.Store.GetOrCreateUser(userID)
}

func (m *meteredStore) PutMessage(message *model.Message) error {
	defer m.observe("PutMessage", time.Now())
	return m.Store.PutMessage(message)
}

func (m *meteredStore) PutMessages(messages []*model.Message) error {
	defer m.observe("PutMessages", time.Now())
	return m.Store.PutMessages(messages)
}

func (m *meteredStore) GetMessagesBySessionPage(sessionID string, limit, offset int) ([]*model.Message, error) {
	defer m.observe("GetMessagesBySessionPage", time.Now())
	return m.Store.GetMessagesBySessionPage(sessionID, limit, offset)
}

func (m *meteredStore) AddOpenedFile(openedFile *model.OpenedFile) error {
	defer m.observe("AddOpenedFile", time.Now())
	return m.Store.AddOpenedFile(openedFile)
}

func (m *meteredStore) CloseOpenedFile(sessionID string, filePath string) error {
	defer m.observe("CloseOpenedFile", time.Now())
	return m.Store.CloseOpenedFile(sessionID, filePath)
}

func (m *meteredStore) GetCurrentlyOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	defer m.observe("GetCurrentlyOpenedFilesBySession", time.Now())
	return m.Store.GetCurrentlyOpenedFilesBySession(sessionID)
}

func (m *meteredStore) PutUserFile(f *model.UserFile) error {
	defer m.observe("PutUserFile", time.Now())
	return m.Store.PutUserFile(f)
}

func (m *meteredStore) GetUserFile(fileID string) (*model.UserFile, error) {
	defer m.observe("GetUserFile", time.Now())
	return m.Store.GetUserFile(fileID)
}

func (m *meteredStore) DeleteUserFile(fileID string) error {
	defer m.observe("DeleteUserFile", time.Now())
	return m.Store.DeleteUserFile(fileID)
}

func (m *meteredStore) PutToolCall(toolCall *model.ToolCall) error {
	defer m.observe("PutToolCall", time.Now())
	return m.Store.PutToolCall(toolCall)
}

func (m *meteredStore) UpdateToolCallResponse(toolID string, response string, execErr error) error {
	defer m.observe("UpdateToolCallResponse", time.Now())
	return m.Store.UpdateToolCallResponse(toolID, response, execErr)
}

func (m *meteredStore) PutRouteTrace(trace *model.RouteTrace) error {
	defer m.observe("PutRouteTrace", time.Now())
	return m.Store.PutRouteTrace(trace)
}

// --- Debug reads + aggregates (debuger.DebugStore) ---

func (m *meteredStore) GetAllSessions() (map[string][]*model.Session, error) {
	defer m.observe("GetAllSessions", time.Now())
	return m.Store.GetAllSessions()
}

func (m *meteredStore) GetAllUsers() ([]*model.User, error) {
	defer m.observe("GetAllUsers", time.Now())
	return m.Store.GetAllUsers()
}

func (m *meteredStore) GetAllMessages() ([]*model.Message, error) {
	defer m.observe("GetAllMessages", time.Now())
	return m.Store.GetAllMessages()
}

func (m *meteredStore) GetAllOpenedFiles() ([]*model.OpenedFile, error) {
	defer m.observe("GetAllOpenedFiles", time.Now())
	return m.Store.GetAllOpenedFiles()
}

func (m *meteredStore) GetOpenedFilesByUser(userID string) ([]*model.OpenedFile, error) {
	defer m.observe("GetOpenedFilesByUser", time.Now())
	return m.Store.GetOpenedFilesByUser(userID)
}

func (m *meteredStore) GetAllUserFiles() ([]*model.UserFile, error) {
	defer m.observe("GetAllUserFiles", time.Now())
	return m.Store.GetAllUserFiles()
}

func (m *meteredStore) GetUserFilesByUser(userID string) ([]*model.UserFile, error) {
	defer m.observe("GetUserFilesByUser", time.Now())
	return m.Store.GetUserFilesByUser(userID)
}

func (m *meteredStore) GetUserFilesBySession(sessionID string) ([]*model.UserFile, error) {
	defer m.observe("GetUserFilesBySession", time.Now())
	return m.Store.GetUserFilesBySession(sessionID)
}

func (m *meteredStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	defer m.observe("GetMessagesBySession", time.Now())
	return m.Store.GetMessagesBySession(sessionID)
}

func (m *meteredStore) GetMessagesByUser(userID string) ([]*model.Message, error) {
	defer m.observe("GetMessagesByUser", time.Now())
	return m.Store.GetMessagesByUser(userID)
}

func (m *meteredStore) GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	defer m.observe("GetOpenedFilesBySession", time.Now())
	return m.Store.GetOpenedFilesBySession(sessionID)
}

func (m *meteredStore) GetUser(userID string) (*model.User, error) {
	defer m.observe("GetUser", time.Now())
	return m.Store.GetUser(userID)
}

func (m *meteredStore) GetSession(sessionID string) (*model.Session, error) {
	defer m.observe("GetSession", time.Now())
	return m.Store.GetSession(sessionID)
}

func (m *meteredStore) GetAllToolCalls() ([]*model.ToolCall, error) {
	defer m.observe("GetAllToolCalls", time.Now())
	return m.Store.GetAllToolCalls()
}

func (m *meteredStore) GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error) {
	defer m.observe("GetToolCallsBySession", time.Now())
	return m.Store.GetToolCallsBySession(sessionID)
}

func (m *meteredStore) GetToolCallByID(toolCallID string) (*model.ToolCall, error) {
	defer m.observe("GetToolCallByID", time.Now())
	return m.Store.GetToolCallByID(toolCallID)
}

func (m *meteredStore) GetToolCallByToolID(toolID string) (*model.ToolCall, error) {
	defer m.observe("GetToolCallByToolID", time.Now())
	return m.Store.GetToolCallByToolID(toolID)
}

func (m *meteredStore) PutSummarizationLog(log *model.SummarizationLog) error {
	defer m.observe("PutSummarizationLog", time.Now())
	return m.Store.PutSummarizationLog(log)
}

func (m *meteredStore) GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error) {
	defer m.observe("GetSummarizationLogsBySession", time.Now())
	return m.Store.GetSummarizationLogsBySession(sessionID)
}

func (m *meteredStore) GetAllSummarizationLogs() ([]*model.SummarizationLog, error) {
	defer m.observe("GetAllSummarizationLogs", time.Now())
	return m.Store.GetAllSummarizationLogs()
}

func (m *meteredStore) GetRouteTraceByID(traceID string) (*model.RouteTrace, error) {
	defer m.observe("GetRouteTraceByID", time.Now())
	return m.Store.GetRouteTraceByID(traceID)
}

func (m *meteredStore) GetRouteTracesBySession(sessionID string) ([]*model.RouteTrace, error) {
	defer m.observe("GetRouteTracesBySession", time.Now())
	return m.Store.GetRouteTracesBySession(sessionID)
}

func (m *meteredStore) GetRouteTracesByUser(userID string) ([]*model.RouteTrace, error) {
	defer m.observe("GetRouteTracesByUser", time.Now())
	return m.Store.GetRouteTracesByUser(userID)
}

func (m *meteredStore) GetAllRouteTraces() ([]*model.RouteTrace, error) {
	defer m.observe("GetAllRouteTraces", time.Now())
	return m.Store.GetAllRouteTraces()
}

func (m *meteredStore) DeleteUserData(userID string) error {
	defer m.observe("DeleteUserData", time.Now())
	return m.Store.DeleteUserData(userID)
}

// --- Maintainer forwarding + escape hatch ---
//
// The wrapper is returned to callers as a plain Store. So that hosts which
// type-assert it to store.Maintainer (for backups / integrity checks) keep
// working, forward those methods to the wrapped store. They are NOT timed:
// Backup can stream an entire database and would pollute the query-latency
// histogram. Every built-in backend implements Maintainer, so the fallback is
// effectively dead code, kept only for custom stores.

func (m *meteredStore) Backup(w io.Writer) error {
	if mt, ok := m.Store.(Maintainer); ok {
		return mt.Backup(w)
	}
	return fmt.Errorf("store: backup not supported by %T", m.Store)
}

func (m *meteredStore) Verify() ([]Issue, error) {
	if mt, ok := m.Store.(Maintainer); ok {
		return mt.Verify()
	}
	return nil, fmt.Errorf("store: verify not supported by %T", m.Store)
}

// Unwrap returns the wrapped store, for callers that need the concrete backend
// type (e.g. for a further type assertion such as PoolStats).
func (m *meteredStore) Unwrap() Store { return m.Store }

// Compile-time guarantees that the metered wrapper satisfies the full Store
// contract (embedded interface + timed overrides) and the Maintainer contract.
var (
	_ Store      = (*meteredStore)(nil)
	_ Maintainer = (*meteredStore)(nil)
)
