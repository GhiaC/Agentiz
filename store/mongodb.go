package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoDBStore is a MongoDB implementation of SessionStore and DebugStore
// It stores sessions, users, messages, tool calls, etc. in MongoDB collections with BSON serialization (backward compatible with JSON)
type MongoDBStore struct {
	client     *mongo.Client
	database   *mongo.Database
	collection *mongo.Collection // sessions collection
	uri        string            // connection URI (retained for diagnostics/dashboard)
	mu         sync.RWMutex

	// Additional collections for DebugStore
	usersCollection             *mongo.Collection
	messagesCollection          *mongo.Collection
	toolCallsCollection         *mongo.Collection
	openedFilesCollection       *mongo.Collection
	userFilesCollection         *mongo.Collection
	summarizationLogsCollection *mongo.Collection

	// UserNodes tracks visited nodes for each user (user-level, not session-level)
	userNodes sync.Map
	userLock  map[string]*sync.Mutex
	nodesMu   sync.RWMutex // Protects userLock map
}

// MongoDBStoreConfig holds configuration for MongoDBStore
type MongoDBStoreConfig struct {
	URI        string // MongoDB connection URI (e.g., "mongodb://localhost:27017")
	Database   string // Database name (default: "agentize")
	Collection string // Collection name (default: "sessions")
}

// DefaultMongoDBStoreConfig returns default configuration
func DefaultMongoDBStoreConfig() MongoDBStoreConfig {
	return MongoDBStoreConfig{
		URI:        "mongodb://localhost:27017",
		Database:   "agentize",
		Collection: "sessions",
	}
}

// URI returns the MongoDB connection URI this store was created with. Used by
// the debug dashboard to report the connection target (credentials should be
// redacted before display).
func (s *MongoDBStore) URI() string {
	return s.uri
}

// DatabaseName returns the MongoDB database name in use. Used by the debug
// dashboard to report where data is persisted.
func (s *MongoDBStore) DatabaseName() string {
	if s.database != nil {
		return s.database.Name()
	}
	return ""
}

// BackendInfo describes this backend for diagnostics and the debug dashboard.
// The connection URI is credential-redacted before display.
func (s *MongoDBStore) BackendInfo() debuger.BackendInfo {
	loc := redactURI(s.uri)
	if db := s.DatabaseName(); db != "" {
		loc = loc + "  (db: " + db + ")"
	}
	return debuger.BackendInfo{Type: "MongoDB", Location: loc}
}

// NewMongoDBStore creates a new MongoDB session store
func NewMongoDBStore(config MongoDBStoreConfig) (*MongoDBStore, error) {
	if config.URI == "" {
		config.URI = "mongodb://localhost:27017"
	}
	if config.Database == "" {
		config.Database = "agentize"
	}
	if config.Collection == "" {
		config.Collection = "sessions"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Configure connection pool for high load
	clientOptions := options.Client().
		ApplyURI(config.URI).
		SetMaxPoolSize(100).                       // Maximum concurrent connections
		SetMinPoolSize(10).                        // Minimum connections to keep open
		SetMaxConnIdleTime(30 * time.Minute).      // Close idle connections after 30 minutes
		SetRetryWrites(true).                      // Retry write operations on transient errors
		SetRetryReads(true).                       // Retry read operations on transient errors
		SetServerSelectionTimeout(5 * time.Second) // Timeout for server selection

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Ping to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	database := client.Database(config.Database)
	collection := database.Collection(config.Collection)

	store := &MongoDBStore{
		client:                      client,
		database:                    database,
		collection:                  collection,
		uri:                         config.URI,
		usersCollection:             database.Collection("users"),
		messagesCollection:          database.Collection("messages"),
		toolCallsCollection:         database.Collection("tool_calls"),
		openedFilesCollection:       database.Collection("opened_files"),
		userFilesCollection:         database.Collection("user_files"),
		summarizationLogsCollection: database.Collection("summarization_logs"),
		userLock:                    make(map[string]*sync.Mutex),
	}

	// Create indexes
	if err := store.initIndexes(ctx); err != nil {
		client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to create indexes: %w", err)
	}

	return store, nil
}

// unmarshalJSONOrBSON tries to unmarshal JSON first, falls back to BSON for backward compatibility
// This handles the case where old data was stored as BSON but new code expects JSON
func unmarshalJSONOrBSON(data string, v interface{}) error {
	// Try JSON first (new format)
	if err := json.Unmarshal([]byte(data), v); err == nil {
		return nil
	}
	// Fallback to BSON for old data
	return bson.Unmarshal([]byte(data), v)
}

// initIndexes creates the necessary indexes
func (s *MongoDBStore) initIndexes(ctx context.Context) error {
	// Index on user_id
	_, err := s.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("failed to create user_id index: %w", err)
	}

	// Index on updated_at
	_, err = s.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "updated_at", Value: -1}},
	})
	if err != nil {
		return fmt.Errorf("failed to create updated_at index: %w", err)
	}

	// Unique index for Core sessions (one Core session per user)
	_, err = s.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "agent_type", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{
			"agent_type": "core",
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to create unique core session index: %w", err)
	}

	// Compound index for GetNextSessionSeq: user_id + agent_type + session_seq
	// This optimizes the aggregation pipeline that finds MAX(session_seq)
	_, err = s.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "agent_type", Value: 1},
			{Key: "session_seq", Value: -1}, // Descending for MAX queries
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create user_agent_seq index: %w", err)
	}

	// ============================================================================
	// Messages Collection Indexes
	// ============================================================================

	// Index for getMaxSeqIDForSession and other session_id queries: session_id
	// This is a simple index for efficient filtering by session_id
	_, err = s.messagesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "session_id", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("failed to create messages session_id index: %w", err)
	}

	// Index for efficient MAX(seq_id) queries: session_id + seq_id
	// This compound index optimizes aggregation pipeline in getMaxSeqIDForSession
	_, err = s.messagesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "seq_id", Value: -1}, // Descending for MAX queries
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create messages session_id+seq_id index: %w", err)
	}

	// Index for GetMessagesBySession: session_id + created_at DESC
	_, err = s.messagesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "created_at", Value: -1},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create messages session_id+created_at index: %w", err)
	}

	// Index for GetMessagesByUser: user_id + created_at DESC
	_, err = s.messagesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "created_at", Value: -1},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create messages user_id+created_at index: %w", err)
	}

	// ============================================================================
	// ToolCalls Collection Indexes
	// ============================================================================

	// Index for GetToolCallsBySession: session_id + created_at (query sorts created_at DESC)
	_, err = s.toolCallsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "created_at", Value: 1},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create tool_calls session_id+created_at index: %w", err)
	}

	// Unique index for GetToolCallByToolID: tool_id (prevents duplicates)
	// Use partial filter to only index non-null tool_id values (for backward compatibility with old data)
	// This allows documents with null/missing tool_id to coexist without violating unique constraint
	_, err = s.toolCallsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "tool_id", Value: 1}},
		Options: options.Index().
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"tool_id": bson.M{"$exists": true, "$type": "string"}}),
	})
	if err != nil {
		// If unique index creation fails due to duplicates, fall back to non-unique index
		// This allows the system to work even with duplicate tool_ids
		_, fallbackErr := s.toolCallsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "tool_id", Value: 1}},
			Options: options.Index().SetPartialFilterExpression(bson.M{"tool_id": bson.M{"$exists": true, "$type": "string"}}),
		})
		if fallbackErr != nil {
			return fmt.Errorf("failed to create tool_calls tool_id index (unique failed: %v, fallback failed: %w)", err, fallbackErr)
		}
		// Non-unique index created successfully - this is acceptable
	}

	// Index for GetToolCallByID: lookup by tool_call_id (OpenAI's ID), not _id
	_, err = s.toolCallsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "tool_call_id", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("failed to create tool_calls tool_call_id index: %w", err)
	}

	// ============================================================================
	// OpenedFiles Collection Indexes
	// ============================================================================

	// Index for GetOpenedFilesBySession: session_id
	_, err = s.openedFilesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "session_id", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("failed to create opened_files session_id index: %w", err)
	}

	// Compound index for GetCurrentlyOpenedFilesBySession: session_id + closed_at
	// This supports queries with $exists on closed_at
	_, err = s.openedFilesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "closed_at", Value: 1},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create opened_files session_id+closed_at index: %w", err)
	}

	// ============================================================================
	// UserFiles Collection Indexes
	// ============================================================================

	// Index for GetUserFilesByUser: user_id + created_at (newest first)
	_, err = s.userFilesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "created_at", Value: -1},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create user_files user_id index: %w", err)
	}

	// Index for GetUserFilesBySession: session_id
	_, err = s.userFilesCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "session_id", Value: 1}},
	})
	if err != nil {
		return fmt.Errorf("failed to create user_files session_id index: %w", err)
	}

	// ============================================================================
	// SummarizationLogs Collection Indexes
	// ============================================================================

	// Index for GetSummarizationLogsBySession: session_id + created_at ASC
	_, err = s.summarizationLogsCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "created_at", Value: 1},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create summarization_logs session_id+created_at index: %w", err)
	}

	return nil
}

// Close closes the MongoDB connection
func (s *MongoDBStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.client.Disconnect(ctx)
}

// getOrCreateLock gets or creates a mutex for a userID
func (s *MongoDBStore) getOrCreateLock(userID string) *sync.Mutex {
	s.nodesMu.RLock()
	lock, exists := s.userLock[userID]
	s.nodesMu.RUnlock()

	if exists {
		return lock
	}

	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()

	// Double check after acquiring write lock
	if lock, exists := s.userLock[userID]; exists {
		return lock
	}

	lock = &sync.Mutex{}
	s.userLock[userID] = lock
	return lock
}

// sessionDocument represents a session document in MongoDB
type sessionDocument struct {
	SessionID  string    `bson:"_id"`
	UserID     string    `bson:"user_id"`
	AgentType  string    `bson:"agent_type"`
	SessionSeq int       `bson:"session_seq"`
	Data       string    `bson:"data"` // JSON serialized Session
	CreatedAt  time.Time `bson:"created_at"`
	UpdatedAt  time.Time `bson:"updated_at"`
}

// extractSessionSeqFromID extracts the sequence number from a session ID
// Format: userID-agentType-s0001 -> 1
// Returns 0 if the format is not recognized
func extractSessionSeqFromID(sessionID string) int {
	// Find the last occurrence of "-s" and extract the number after it
	idx := strings.LastIndex(sessionID, "-s")
	if idx == -1 || idx+2 >= len(sessionID) {
		return 0
	}
	seqStr := sessionID[idx+2:]
	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		return 0
	}
	return seq
}

// Get retrieves a session by ID
func (s *MongoDBStore) Get(sessionID string) (*model.Session, error) {
	// MongoDB is thread-safe, no mutex needed
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var doc sessionDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": sessionID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query session: %w", err)
	}

	session := &model.Session{}
	if err := unmarshalJSONOrBSON(doc.Data, session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Restore timestamps
	session.CreatedAt = doc.CreatedAt
	session.UpdatedAt = doc.UpdatedAt

	// Restore MessageSeq from database to ensure it's correct
	// Use MAX(seq_id) from messages collection to get the highest sequence number
	maxSeqID := s.getMaxSeqIDForSession(ctx, sessionID)
	if maxSeqID > session.MessageSeq {
		// Ensure MessageSeq is at least as high as the highest seq_id in the database
		session.MessageSeq = maxSeqID
	}

	// Restore ToolSeq from tool_calls so we never reuse a tool ID (ensures new tool calls are stored with unique IDs)
	maxToolSeq := s.getMaxToolSeqForSession(ctx, sessionID)
	if maxToolSeq > session.ToolSeq {
		log.Log.Debugf("[MongoDBStore] Get | Restoring ToolSeq | SessionID: %s | OldToolSeq: %d | MaxToolSeq: %d", sessionID, session.ToolSeq, maxToolSeq)
		session.ToolSeq = maxToolSeq
	}
	log.Log.Debugf("[MongoDBStore] Get | Final ToolSeq | SessionID: %s | ToolSeq: %d", sessionID, session.ToolSeq)

	return session, nil
}

// getMaxSeqIDForSession returns the maximum seq_id for a session.
// Used to restore MessageSeq counter correctly from database.
// Uses aggregation pipeline for efficient querying (seq_id is stored as separate field).
func (s *MongoDBStore) getMaxSeqIDForSession(ctx context.Context, sessionID string) int {
	// Use aggregation pipeline to find MAX(seq_id) efficiently
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"session_id": sessionID}}},
		{{Key: "$group", Value: bson.M{
			"_id":      nil,
			"maxSeqID": bson.M{"$max": "$seq_id"},
		}}},
	}

	cursor, err := s.messagesCollection.Aggregate(ctx, pipeline)
	if err != nil {
		// Fallback: if aggregation fails (e.g., old data without seq_id field), use old method
		return s.getMaxSeqIDForSessionFallback(ctx, sessionID)
	}
	defer cursor.Close(ctx)

	if cursor.Next(ctx) {
		var result struct {
			MaxSeqID int `bson:"maxSeqID"`
		}
		if err := cursor.Decode(&result); err == nil {
			return result.MaxSeqID
		}
	}

	return 0
}

// getMaxToolSeqForSession returns the maximum tool sequence number for a session from tool_calls collection.
func (s *MongoDBStore) getMaxToolSeqForSession(ctx context.Context, sessionID string) int {
	cursor, err := s.toolCallsCollection.Find(ctx, bson.M{"session_id": sessionID}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		log.Log.Warnf("[MongoDBStore] getMaxToolSeqForSession query error | SessionID: %s | Error: %v", sessionID, err)
		return 0
	}
	defer cursor.Close(ctx)
	maxSeq := 0
	count := 0
	for cursor.Next(ctx) {
		var doc struct {
			ID string `bson:"_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		count++
		if seq := parseToolSeqFromToolID(doc.ID); seq > maxSeq {
			maxSeq = seq
		}
	}
	log.Log.Debugf("[MongoDBStore] getMaxToolSeqForSession | SessionID: %s | Found: %d tool_calls | MaxSeq: %d", sessionID, count, maxSeq)
	return maxSeq
}

// getMaxSeqIDForSessionFallback is a fallback method for old data without seq_id field.
// Reads all messages and unmarshals JSON to find max SeqID.
func (s *MongoDBStore) getMaxSeqIDForSessionFallback(ctx context.Context, sessionID string) int {
	cursor, err := s.messagesCollection.Find(ctx, bson.M{"session_id": sessionID})
	if err != nil {
		return 0
	}
	defer cursor.Close(ctx)

	maxSeqID := 0
	for cursor.Next(ctx) {
		var doc messageDocument
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		// If seq_id is already in document (new format), use it directly
		if doc.SeqID > maxSeqID {
			maxSeqID = doc.SeqID
			continue
		}

		// Fallback: unmarshal JSON to get SeqID (for old data)
		message := &model.Message{}
		if err := unmarshalJSONOrBSON(doc.Data, message); err != nil {
			continue
		}

		if message.SeqID > maxSeqID {
			maxSeqID = message.SeqID
		}
	}

	return maxSeqID
}

// Put stores or updates a session
// For Core sessions, this ensures only one Core session exists per user
func (s *MongoDBStore) Put(session *model.Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}

	// Ensure user exists when storing a session (otherwise user is never created on first session)
	if _, err := s.GetOrCreateUser(session.UserID); err != nil {
		return fmt.Errorf("ensure user for session: %w", err)
	}

	// For Core sessions, use PutCoreSession to ensure uniqueness
	if session.AgentType == model.AgentTypeCore {
		return s.PutCoreSession(session)
	}

	// MongoDB is thread-safe, no mutex needed
	session.UpdatedAt = time.Now()

	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	doc := sessionDocument{
		SessionID:  session.SessionID,
		UserID:     session.UserID,
		AgentType:  string(session.AgentType),
		SessionSeq: extractSessionSeqFromID(session.SessionID),
		Data:       string(data),
		CreatedAt:  session.CreatedAt,
		UpdatedAt:  session.UpdatedAt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := options.Replace().SetUpsert(true)
	_, err = s.collection.ReplaceOne(ctx, bson.M{"_id": session.SessionID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store session: %w", err)
	}

	return nil
}

// Delete removes a session
func (s *MongoDBStore) Delete(sessionID string) error {
	// MongoDB is thread-safe, no mutex needed
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := s.collection.DeleteOne(ctx, bson.M{"_id": sessionID})
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

// DeleteUserData deletes all sessions, messages, tool calls, summarization logs,
// and opened files for a user. Resets user's ActiveSessionIDs and SessionSeqs,
// and unbans the user (clears BanUntil, BanMessage, NonsenseCount).
func (s *MongoDBStore) DeleteUserData(userID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	userFilter := bson.M{"user_id": userID}

	// Get session IDs for this user (needed for collections that use session_id)
	sessions, err := s.List(userID)
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		sessionIDs = append(sessionIDs, sess.SessionID)
	}

	// Delete messages by user_id
	if _, err := s.messagesCollection.DeleteMany(ctx, userFilter); err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}

	// Delete user_files by user_id
	if _, err := s.userFilesCollection.DeleteMany(ctx, userFilter); err != nil {
		return fmt.Errorf("failed to delete user_files: %w", err)
	}

	// Delete tool_calls and summarization_logs by session_id (these don't have user_id at top level)
	if len(sessionIDs) > 0 {
		sessionFilter := bson.M{"session_id": bson.M{"$in": sessionIDs}}
		if _, err := s.toolCallsCollection.DeleteMany(ctx, sessionFilter); err != nil {
			return fmt.Errorf("failed to delete tool_calls: %w", err)
		}
		if _, err := s.summarizationLogsCollection.DeleteMany(ctx, sessionFilter); err != nil {
			return fmt.Errorf("failed to delete summarization_logs: %w", err)
		}
		if _, err := s.openedFilesCollection.DeleteMany(ctx, sessionFilter); err != nil {
			return fmt.Errorf("failed to delete opened_files: %w", err)
		}
	}

	// Delete sessions
	if _, err := s.collection.DeleteMany(ctx, userFilter); err != nil {
		return fmt.Errorf("failed to delete sessions: %w", err)
	}

	// Reset user's ActiveSessionIDs and SessionSeqs
	var doc userDocument
	err = s.usersCollection.FindOne(ctx, bson.M{"_id": userID}).Decode(&doc)
	if err == nil {
		user := &model.User{}
		if json.Unmarshal([]byte(doc.Data), user) == nil {
			user.ActiveSessionIDs = make(map[model.AgentType]string)
			user.SessionSeqs = make(map[model.AgentType]int)
			user.Unban() // remove ban so user is no longer banned after full data delete
			if userData, err := json.Marshal(user); err == nil {
				opts := options.Replace().SetUpsert(true)
				doc.Data = string(userData)
				doc.UpdatedAt = user.UpdatedAt
				_, _ = s.usersCollection.ReplaceOne(ctx, bson.M{"_id": userID}, doc, opts)
			}
		}
	}

	return nil
}

// List returns all sessions for a user
func (s *MongoDBStore) List(userID string) ([]*model.Session, error) {
	// MongoDB is thread-safe, no mutex needed
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.collection.Find(ctx, bson.M{"user_id": userID}, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer cursor.Close(ctx)

	var sessions []*model.Session
	for cursor.Next(ctx) {
		var doc sessionDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode session: %w", err)
		}

		session := &model.Session{}
		if err := unmarshalJSONOrBSON(doc.Data, session); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session: %w", err)
		}

		// Restore timestamps
		session.CreatedAt = doc.CreatedAt
		session.UpdatedAt = doc.UpdatedAt

		sessions = append(sessions, session)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessions, nil
}

// GetNextSessionSeq returns the next session sequence number for a user and agent type
// Uses MAX(session_seq) to avoid duplicate IDs when sessions are deleted
func (s *MongoDBStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use aggregation to find MAX(session_seq) for this user and agent type
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"user_id":    userID,
			"agent_type": string(agentType),
		}}},
		{{Key: "$group", Value: bson.M{
			"_id":     nil,
			"max_seq": bson.M{"$max": "$session_seq"},
		}}},
	}

	cursor, err := s.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, fmt.Errorf("failed to aggregate sessions: %w", err)
	}
	defer cursor.Close(ctx)

	var result struct {
		MaxSeq *int `bson:"max_seq"`
	}

	if cursor.Next(ctx) {
		if err := cursor.Decode(&result); err != nil {
			return 0, fmt.Errorf("failed to decode result: %w", err)
		}
		if result.MaxSeq != nil {
			return *result.MaxSeq + 1, nil
		}
	}

	return 1, nil
}

// GetCoreSession returns the Core session for a user
// For each user, there should be only one Core session
// If no Core session exists, it returns nil without error
func (s *MongoDBStore) GetCoreSession(userID string) (*model.Session, error) {
	// MongoDB is thread-safe, no mutex needed
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var doc sessionDocument
	err := s.collection.FindOne(ctx, bson.M{
		"user_id":    userID,
		"agent_type": string(model.AgentTypeCore),
	}).Decode(&doc)

	if err == mongo.ErrNoDocuments {
		return nil, nil // No Core session found, return nil without error
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query core session: %w", err)
	}

	session := &model.Session{}
	if err := unmarshalJSONOrBSON(doc.Data, session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Restore timestamps
	session.CreatedAt = doc.CreatedAt
	session.UpdatedAt = doc.UpdatedAt

	// Restore seq counters from persisted rows so core-session ID generation never
	// reuses an ID. Mirrors Get() and the SQLite backend for cross-store parity.
	if maxSeqID := s.getMaxSeqIDForSession(ctx, session.SessionID); maxSeqID > session.MessageSeq {
		session.MessageSeq = maxSeqID
	}
	if maxToolSeq := s.getMaxToolSeqForSession(ctx, session.SessionID); maxToolSeq > session.ToolSeq {
		session.ToolSeq = maxToolSeq
	}

	return session, nil
}

// PutCoreSession stores or updates a Core session for a user
// This ensures only one Core session exists per user by deleting any existing Core sessions first
func (s *MongoDBStore) PutCoreSession(session *model.Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}
	if session.AgentType != model.AgentTypeCore {
		return fmt.Errorf("session must be of type Core")
	}

	// Ensure user exists when storing a session
	if _, err := s.GetOrCreateUser(session.UserID); err != nil {
		return fmt.Errorf("ensure user for core session: %w", err)
	}

	// MongoDB is thread-safe, no mutex needed
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Delete any existing Core sessions for this user
	_, err := s.collection.DeleteMany(ctx, bson.M{
		"user_id":    session.UserID,
		"agent_type": string(model.AgentTypeCore),
	})
	if err != nil {
		return fmt.Errorf("failed to delete existing core sessions: %w", err)
	}

	// Now store the new Core session
	session.UpdatedAt = time.Now()

	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	doc := sessionDocument{
		SessionID:  session.SessionID,
		UserID:     session.UserID,
		AgentType:  string(session.AgentType),
		SessionSeq: extractSessionSeqFromID(session.SessionID),
		Data:       string(data),
		CreatedAt:  session.CreatedAt,
		UpdatedAt:  session.UpdatedAt,
	}

	// Upsert by _id (the session ID) so the operation is idempotent and matches
	// the SQLite backend's INSERT OR REPLACE semantics.
	opts := options.Replace().SetUpsert(true)
	_, err = s.collection.ReplaceOne(ctx, bson.M{"_id": session.SessionID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store core session: %w", err)
	}

	return nil
}

// AddVisitedNode adds a visited node for a user
// This tracks nodes at user level, across all sessions
func (s *MongoDBStore) AddVisitedNode(userID string, nodeDigest *model.NodeDigest) {
	if nodeDigest == nil {
		return
	}

	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		if un.VisitedNodes == nil {
			un.VisitedNodes = make(map[string]*model.NodeDigest)
		}
		un.VisitedNodes[nodeDigest.Path] = nodeDigest
		un.LastActivity = time.Now()
		s.userNodes.Store(userID, un)
	} else {
		un := &UserNodes{
			VisitedNodes: map[string]*model.NodeDigest{
				nodeDigest.Path: nodeDigest,
			},
			LastActivity: time.Now(),
		}
		s.userNodes.Store(userID, un)
	}
}

// GetVisitedNodes returns all visited nodes for a user
func (s *MongoDBStore) GetVisitedNodes(userID string) map[string]*model.NodeDigest {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		// Return a copy to prevent external modification
		result := make(map[string]*model.NodeDigest)
		for k, v := range un.VisitedNodes {
			// Create a copy of NodeDigest
			digestCopy := *v
			result[k] = &digestCopy
		}
		return result
	}
	return make(map[string]*model.NodeDigest)
}

// GetVisitedNodePaths returns a list of visited node paths for a user
func (s *MongoDBStore) GetVisitedNodePaths(userID string) []string {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		paths := make([]string, 0, len(un.VisitedNodes))
		for path := range un.VisitedNodes {
			paths = append(paths, path)
		}
		return paths
	}
	return []string{}
}

// HasVisitedNode checks if a user has visited a specific node
func (s *MongoDBStore) HasVisitedNode(userID string, nodePath string) bool {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		_, exists := un.VisitedNodes[nodePath]
		return exists
	}
	return false
}

// ClearVisitedNodes clears all visited nodes for a user
func (s *MongoDBStore) ClearVisitedNodes(userID string) {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	s.userNodes.Delete(userID)
}

// NewMongoDBStoreFromURI creates a new MongoDB session store from a connection URI
// This is a convenience function that uses default database and collection names
// Example: store, err := NewMongoDBStoreFromURI("mongodb://localhost:27017")
func NewMongoDBStoreFromURI(uri string) (model.SessionStore, error) {
	config := DefaultMongoDBStoreConfig()
	config.URI = uri
	return NewMongoDBStore(config)
}

// ============================================================================
// DebugStore Interface Implementation
// ============================================================================

// GetSession is an alias for Get to match DebugStore interface
func (s *MongoDBStore) GetSession(sessionID string) (*model.Session, error) {
	return s.Get(sessionID)
}

// GetAllSessions returns all sessions grouped by userID
func (s *MongoDBStore) GetAllSessions() (map[string][]*model.Session, error) {
	// MongoDB is thread-safe, no mutex needed
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer cursor.Close(ctx)

	result := make(map[string][]*model.Session)
	for cursor.Next(ctx) {
		var doc sessionDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode session: %w", err)
		}

		session := &model.Session{}
		if err := unmarshalJSONOrBSON(doc.Data, session); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session: %w", err)
		}

		session.CreatedAt = doc.CreatedAt
		session.UpdatedAt = doc.UpdatedAt

		result[doc.UserID] = append(result[doc.UserID], session)
	}

	return result, cursor.Err()
}

// userDocument represents a user document in MongoDB.
// Uses _id for MongoDB primary key and user_id for alignment with SQLite schema (same value).
type userDocument struct {
	UserID    string    `bson:"_id"`     // MongoDB primary key
	UserIDAlt string    `bson:"user_id"` // same as _id; aligns stored shape with SQLite (users.user_id)
	Data      string    `bson:"data"`    // JSON serialized User
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
}

// GetUser retrieves a user by ID
func (s *MongoDBStore) GetUser(userID string) (*model.User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var doc userDocument
	err := s.usersCollection.FindOne(ctx, bson.M{"_id": userID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil // User not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	user := &model.User{}
	if err := unmarshalJSONOrBSON(doc.Data, user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	// Initialize ActiveSessionIDs if nil (backward compatibility for old users)
	if user.ActiveSessionIDs == nil {
		user.ActiveSessionIDs = make(map[model.AgentType]string)
	}

	// Initialize SessionSeqs if nil (backward compatibility for old users)
	if user.SessionSeqs == nil {
		user.SessionSeqs = make(map[model.AgentType]int)
	}

	return user, nil
}

// PutUser stores or updates a user
func (s *MongoDBStore) PutUser(user *model.User) error {
	if user == nil {
		return fmt.Errorf("user cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	doc := userDocument{
		UserID:    user.UserID,
		UserIDAlt: user.UserID, // align with SQLite: stored as user_id
		Data:      string(data),
		CreatedAt: user.CreatedAt,
		UpdatedAt: time.Now(),
	}

	opts := options.Replace().SetUpsert(true)
	_, err = s.usersCollection.ReplaceOne(ctx, bson.M{"_id": user.UserID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store user: %w", err)
	}

	return nil
}

// GetOrCreateUser gets an existing user or creates a new one
func (s *MongoDBStore) GetOrCreateUser(userID string) (*model.User, error) {
	user, err := s.GetUser(userID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		needsSave := false

		// Compute SessionSeqs from existing sessions if empty (backward compatibility)
		if len(user.SessionSeqs) == 0 {
			if err := s.computeSessionSeqs(user); err == nil && len(user.SessionSeqs) > 0 {
				needsSave = true
			}
		}

		// Compute ActiveSessionIDs from existing sessions if empty (backward compatibility)
		if len(user.ActiveSessionIDs) == 0 {
			if err := s.computeActiveSessionIDs(user); err == nil && len(user.ActiveSessionIDs) > 0 {
				needsSave = true
			}
		}

		// Save user if any backward compatibility computation was done
		if needsSave {
			_ = s.PutUser(user) // Best effort save
		}

		return user, nil
	}

	// Create new user
	user = model.NewUser(userID)
	if err := s.PutUser(user); err != nil {
		return nil, err
	}
	return user, nil
}

// computeSessionSeqs computes SessionSeqs from existing sessions for backward compatibility
// This is called when a user has no SessionSeqs (old user migrating to new format)
func (s *MongoDBStore) computeSessionSeqs(user *model.User) error {
	if user == nil {
		return nil
	}

	// Get all sessions for this user
	sessions, err := s.List(user.UserID)
	if err != nil {
		return err
	}

	// Count sessions by agent type
	seqCounts := make(map[model.AgentType]int)
	for _, session := range sessions {
		if session.AgentType != "" {
			seqCounts[session.AgentType]++
		}
	}

	// Update user's SessionSeqs
	if user.SessionSeqs == nil {
		user.SessionSeqs = make(map[model.AgentType]int)
	}
	for agentType, count := range seqCounts {
		user.SessionSeqs[agentType] = count
	}

	return nil
}

// computeActiveSessionIDs computes ActiveSessionIDs from existing sessions for backward compatibility
// This is called when a user has no ActiveSessionIDs (old user migrating to new format)
// For each agent type, it selects the most recently updated session as the active session
func (s *MongoDBStore) computeActiveSessionIDs(user *model.User) error {
	if user == nil {
		return nil
	}

	// Get all sessions for this user
	sessions, err := s.List(user.UserID)
	if err != nil {
		return err
	}

	// Find the most recent session for each agent type
	latestByType := make(map[model.AgentType]*model.Session)
	for _, session := range sessions {
		if session.AgentType == "" {
			continue
		}
		existing := latestByType[session.AgentType]
		if existing == nil || session.UpdatedAt.After(existing.UpdatedAt) {
			latestByType[session.AgentType] = session
		}
	}

	// Update user's ActiveSessionIDs
	if user.ActiveSessionIDs == nil {
		user.ActiveSessionIDs = make(map[model.AgentType]string)
	}
	for agentType, session := range latestByType {
		user.ActiveSessionIDs[agentType] = session.SessionID
	}

	return nil
}

// GetAllUsers returns all users
func (s *MongoDBStore) GetAllUsers() ([]*model.User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.usersCollection.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to query users: %w", err)
	}
	defer cursor.Close(ctx)

	var users []*model.User
	for cursor.Next(ctx) {
		var doc userDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode user: %w", err)
		}

		user := &model.User{}
		if err := unmarshalJSONOrBSON(doc.Data, user); err != nil {
			return nil, fmt.Errorf("failed to unmarshal user: %w", err)
		}

		users = append(users, user)
	}

	return users, cursor.Err()
}

// messageDocument represents a message document in MongoDB
type messageDocument struct {
	MessageID string    `bson:"_id"`
	SessionID string    `bson:"session_id"`
	UserID    string    `bson:"user_id"`
	SeqID     int       `bson:"seq_id,omitempty"` // Sequence ID for efficient querying (added for optimization)
	Data      string    `bson:"data"`             // JSON serialized Message
	CreatedAt time.Time `bson:"created_at"`
}

// PutMessage stores a message
func (s *MongoDBStore) PutMessage(message *model.Message) error {
	if message == nil {
		return fmt.Errorf("message cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	doc := messageDocument{
		MessageID: message.MessageID,
		SessionID: message.SessionID,
		UserID:    message.UserID,
		SeqID:     message.SeqID, // Store seq_id separately for efficient querying
		Data:      string(data),
		CreatedAt: message.CreatedAt,
	}

	opts := options.Replace().SetUpsert(true)
	_, err = s.messagesCollection.ReplaceOne(ctx, bson.M{"_id": message.MessageID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store message: %w", err)
	}

	return nil
}

// GetMessagesBySession returns all messages for a session
func (s *MongoDBStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.messagesCollection.Find(ctx, bson.M{"session_id": sessionID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer cursor.Close(ctx)

	var messages []*model.Message
	for cursor.Next(ctx) {
		var doc messageDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode message: %w", err)
		}

		message := &model.Message{}
		if err := unmarshalJSONOrBSON(doc.Data, message); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}

		messages = append(messages, message)
	}

	return messages, cursor.Err()
}

// GetMessagesByUser returns all messages for a user
func (s *MongoDBStore) GetMessagesByUser(userID string) ([]*model.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.messagesCollection.Find(ctx, bson.M{"user_id": userID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer cursor.Close(ctx)

	var messages []*model.Message
	for cursor.Next(ctx) {
		var doc messageDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode message: %w", err)
		}

		message := &model.Message{}
		if err := unmarshalJSONOrBSON(doc.Data, message); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}

		messages = append(messages, message)
	}

	return messages, cursor.Err()
}

// GetAllMessages returns all messages
func (s *MongoDBStore) GetAllMessages() ([]*model.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.messagesCollection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer cursor.Close(ctx)

	var messages []*model.Message
	for cursor.Next(ctx) {
		var doc messageDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode message: %w", err)
		}

		message := &model.Message{}
		if err := unmarshalJSONOrBSON(doc.Data, message); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}

		messages = append(messages, message)
	}

	return messages, cursor.Err()
}

// openedFileDocument represents an opened file document in MongoDB
type openedFileDocument struct {
	ID        string    `bson:"_id"`
	SessionID string    `bson:"session_id"`
	FilePath  string    `bson:"file_path"`
	Data      string    `bson:"data"` // JSON serialized OpenedFile
	OpenedAt  time.Time `bson:"opened_at"`
	ClosedAt  time.Time `bson:"closed_at,omitempty"`
}

// AddOpenedFile records that a file was opened
func (s *MongoDBStore) AddOpenedFile(openedFile *model.OpenedFile) error {
	if openedFile == nil {
		return fmt.Errorf("openedFile cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(openedFile)
	if err != nil {
		return fmt.Errorf("failed to marshal opened file: %w", err)
	}

	id := fmt.Sprintf("%s:%s", openedFile.SessionID, openedFile.FilePath)
	doc := openedFileDocument{
		ID:        id,
		SessionID: openedFile.SessionID,
		FilePath:  openedFile.FilePath,
		Data:      string(data),
		OpenedAt:  openedFile.OpenedAt,
	}

	opts := options.Replace().SetUpsert(true)
	_, err = s.openedFilesCollection.ReplaceOne(ctx, bson.M{"_id": id}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store opened file: %w", err)
	}

	return nil
}

// CloseOpenedFile marks the currently-open record for (sessionID, filePath)
// closed. It is a no-op when the file is not currently open, matching the SQLite
// backend's "WHERE is_open = 1" guard. The embedded JSON is updated too so a
// later read reflects the closed state (IsOpen=false, ClosedAt set), exactly as
// the SQLite backend reconstructs it from columns.
func (s *MongoDBStore) CloseOpenedFile(sessionID string, filePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id := fmt.Sprintf("%s:%s", sessionID, filePath)

	// Only act on a currently-open record (no closed_at yet).
	var doc openedFileDocument
	err := s.openedFilesCollection.FindOne(ctx, bson.M{
		"_id":       id,
		"closed_at": bson.M{"$exists": false},
	}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil // not open (or absent) → no-op, matching SQLite
	}
	if err != nil {
		return fmt.Errorf("failed to query opened file: %w", err)
	}

	now := time.Now()
	file := &model.OpenedFile{}
	if uErr := unmarshalJSONOrBSON(doc.Data, file); uErr == nil {
		file.IsOpen = false
		file.ClosedAt = now
		if data, mErr := json.Marshal(file); mErr == nil {
			doc.Data = string(data)
		}
	}

	_, err = s.openedFilesCollection.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"closed_at": now, "data": doc.Data},
	})
	if err != nil {
		return fmt.Errorf("failed to close opened file: %w", err)
	}

	return nil
}

// GetOpenedFilesBySession returns all opened files for a session
func (s *MongoDBStore) GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.openedFilesCollection.Find(ctx, bson.M{"session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer cursor.Close(ctx)

	var files []*model.OpenedFile
	for cursor.Next(ctx) {
		var doc openedFileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode opened file: %w", err)
		}

		file := &model.OpenedFile{}
		if err := unmarshalJSONOrBSON(doc.Data, file); err != nil {
			return nil, fmt.Errorf("failed to unmarshal opened file: %w", err)
		}

		files = append(files, file)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}

	sortOpenedFilesByOpenedAtAsc(files)
	return files, nil
}

// GetCurrentlyOpenedFilesBySession returns only currently open files
func (s *MongoDBStore) GetCurrentlyOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.openedFilesCollection.Find(ctx, bson.M{
		"session_id": sessionID,
		"closed_at":  bson.M{"$exists": false},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer cursor.Close(ctx)

	var files []*model.OpenedFile
	for cursor.Next(ctx) {
		var doc openedFileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode opened file: %w", err)
		}

		file := &model.OpenedFile{}
		if err := unmarshalJSONOrBSON(doc.Data, file); err != nil {
			return nil, fmt.Errorf("failed to unmarshal opened file: %w", err)
		}

		files = append(files, file)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}

	sortOpenedFilesByOpenedAtAsc(files)
	return files, nil
}

// GetAllOpenedFiles returns all opened files
func (s *MongoDBStore) GetAllOpenedFiles() ([]*model.OpenedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.openedFilesCollection.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer cursor.Close(ctx)

	var files []*model.OpenedFile
	for cursor.Next(ctx) {
		var doc openedFileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode opened file: %w", err)
		}

		file := &model.OpenedFile{}
		if err := unmarshalJSONOrBSON(doc.Data, file); err != nil {
			return nil, fmt.Errorf("failed to unmarshal opened file: %w", err)
		}

		files = append(files, file)
	}

	return files, cursor.Err()
}

// GetOpenedFilesByUser returns opened files for a user sorted by OpenedAt (newest first).
// MongoDB stores full document in Data; we filter by user_id after decode.
func (s *MongoDBStore) GetOpenedFilesByUser(userID string) ([]*model.OpenedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.openedFilesCollection.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer cursor.Close(ctx)

	var files []*model.OpenedFile
	for cursor.Next(ctx) {
		var doc openedFileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode opened file: %w", err)
		}

		file := &model.OpenedFile{}
		if err := unmarshalJSONOrBSON(doc.Data, file); err != nil {
			return nil, fmt.Errorf("failed to unmarshal opened file: %w", err)
		}

		if file.UserID == userID {
			files = append(files, file)
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	// Sort by OpenedAt descending
	sort.Slice(files, func(i, j int) bool {
		return files[i].OpenedAt.After(files[j].OpenedAt)
	})

	return files, nil
}

// userFileDocument represents a user file metadata document in MongoDB.
// _id is FileID. Top-level user_id/session_id/created_at support indexed queries;
// the full record is JSON-serialized in Data.
type userFileDocument struct {
	ID        string    `bson:"_id"`
	UserID    string    `bson:"user_id"`
	SessionID string    `bson:"session_id"`
	Data      string    `bson:"data"` // JSON serialized UserFile
	CreatedAt time.Time `bson:"created_at"`
}

// decodeUserFiles decodes a cursor of userFileDocument into model.UserFile slice.
func decodeUserFiles(ctx context.Context, cursor *mongo.Cursor) ([]*model.UserFile, error) {
	defer cursor.Close(ctx)
	var files []*model.UserFile
	for cursor.Next(ctx) {
		var doc userFileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode user file: %w", err)
		}
		f := &model.UserFile{}
		if err := unmarshalJSONOrBSON(doc.Data, f); err != nil {
			return nil, fmt.Errorf("failed to unmarshal user file: %w", err)
		}
		files = append(files, f)
	}
	return files, cursor.Err()
}

// PutUserFile inserts or updates a user file record.
func (s *MongoDBStore) PutUserFile(f *model.UserFile) error {
	if f == nil {
		return fmt.Errorf("userFile cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("failed to marshal user file: %w", err)
	}

	doc := userFileDocument{
		ID:        f.FileID,
		UserID:    f.UserID,
		SessionID: f.SessionID,
		Data:      string(data),
		CreatedAt: f.CreatedAt,
	}

	opts := options.Replace().SetUpsert(true)
	if _, err := s.userFilesCollection.ReplaceOne(ctx, bson.M{"_id": f.FileID}, doc, opts); err != nil {
		return fmt.Errorf("failed to store user file: %w", err)
	}
	return nil
}

// GetUserFile returns a single user file by ID, or nil if not found.
func (s *MongoDBStore) GetUserFile(fileID string) (*model.UserFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var doc userFileDocument
	err := s.userFilesCollection.FindOne(ctx, bson.M{"_id": fileID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user file: %w", err)
	}
	f := &model.UserFile{}
	if err := unmarshalJSONOrBSON(doc.Data, f); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user file: %w", err)
	}
	return f, nil
}

// GetUserFilesByUser returns all files for a user, newest first.
func (s *MongoDBStore) GetUserFilesByUser(userID string) ([]*model.UserFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := s.userFilesCollection.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query user files: %w", err)
	}
	return decodeUserFiles(ctx, cursor)
}

// GetUserFilesBySession returns all files for a session, newest first.
func (s *MongoDBStore) GetUserFilesBySession(sessionID string) ([]*model.UserFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := s.userFilesCollection.Find(ctx, bson.M{"session_id": sessionID}, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query user files: %w", err)
	}
	return decodeUserFiles(ctx, cursor)
}

// GetAllUserFiles returns all user files, newest first.
func (s *MongoDBStore) GetAllUserFiles() ([]*model.UserFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := s.userFilesCollection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query user files: %w", err)
	}
	return decodeUserFiles(ctx, cursor)
}

// DeleteUserFile removes a user file metadata record by ID.
func (s *MongoDBStore) DeleteUserFile(fileID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.userFilesCollection.DeleteOne(ctx, bson.M{"_id": fileID}); err != nil {
		return fmt.Errorf("failed to delete user file: %w", err)
	}
	return nil
}

// toolCallDocument represents a tool call document in MongoDB.
// _id is ToolID (our sequential key) so upserts and lookups use ToolID consistently.
type toolCallDocument struct {
	ID         string    `bson:"_id"`          // ToolID, our sequential key
	ToolCallID string    `bson:"tool_call_id"` // LLM's ID (from OpenAI)
	ToolID     string    `bson:"tool_id"`      // same as _id, kept for backward compatibility
	SessionID  string    `bson:"session_id"`
	Data       string    `bson:"data"` // JSON serialized ToolCall
	CreatedAt  time.Time `bson:"created_at"`
}

// PutToolCall stores a tool call
func (s *MongoDBStore) PutToolCall(toolCall *model.ToolCall) error {
	if toolCall == nil {
		return fmt.Errorf("toolCall cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(toolCall)
	if err != nil {
		return fmt.Errorf("failed to marshal tool call: %w", err)
	}

	doc := toolCallDocument{
		ID:         toolCall.ToolID,
		ToolCallID: toolCall.ToolCallID,
		ToolID:     toolCall.ToolID,
		SessionID:  toolCall.SessionID,
		Data:       string(data),
		CreatedAt:  toolCall.CreatedAt,
	}

	opts := options.Replace().SetUpsert(true)
	_, err = s.toolCallsCollection.ReplaceOne(ctx, bson.M{"_id": toolCall.ToolID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store tool call: %w", err)
	}

	return nil
}

// GetToolCallsBySession returns all tool calls for a session
func (s *MongoDBStore) GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.toolCallsCollection.Find(ctx, bson.M{"session_id": sessionID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query tool calls: %w", err)
	}
	defer cursor.Close(ctx)

	var toolCalls []*model.ToolCall
	for cursor.Next(ctx) {
		var doc toolCallDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode tool call: %w", err)
		}

		tc := &model.ToolCall{}
		if err := unmarshalJSONOrBSON(doc.Data, tc); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool call: %w", err)
		}

		toolCalls = append(toolCalls, tc)
	}

	return toolCalls, cursor.Err()
}

// GetToolCallByID returns a tool call by its ID (OpenAI's tool_call_id), not by ToolID.
// In MongoDB _id is ToolID; tool_call_id is stored as a separate field for this lookup.
func (s *MongoDBStore) GetToolCallByID(toolCallID string) (*model.ToolCall, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var doc toolCallDocument
	err := s.toolCallsCollection.FindOne(ctx, bson.M{"tool_call_id": toolCallID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query tool call: %w", err)
	}

	tc := &model.ToolCall{}
	if err := unmarshalJSONOrBSON(doc.Data, tc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool call: %w", err)
	}

	return tc, nil
}

// GetToolCallByToolID returns a tool call by ToolID (sequential ID)
func (s *MongoDBStore) GetToolCallByToolID(toolID string) (*model.ToolCall, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var doc toolCallDocument
	err := s.toolCallsCollection.FindOne(ctx, bson.M{"tool_id": toolID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query tool call by tool ID: %w", err)
	}

	tc := &model.ToolCall{}
	if err := unmarshalJSONOrBSON(doc.Data, tc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool call: %w", err)
	}

	return tc, nil
}

// GetAllToolCalls returns all tool calls
func (s *MongoDBStore) GetAllToolCalls() ([]*model.ToolCall, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.toolCallsCollection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query tool calls: %w", err)
	}
	defer cursor.Close(ctx)

	var toolCalls []*model.ToolCall
	for cursor.Next(ctx) {
		var doc toolCallDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode tool call: %w", err)
		}

		tc := &model.ToolCall{}
		if err := unmarshalJSONOrBSON(doc.Data, tc); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool call: %w", err)
		}

		toolCalls = append(toolCalls, tc)
	}

	return toolCalls, cursor.Err()
}

// UpdateToolCallResponse updates the response for a tool call by ToolID and calculates duration.
// When execErr != nil, sets status=failed and error=execErr.Error().
func (s *MongoDBStore) UpdateToolCallResponse(toolID string, response string, execErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now()

	// First, get the existing tool call to calculate duration (_id is ToolID)
	var doc toolCallDocument
	err := s.toolCallsCollection.FindOne(ctx, bson.M{"_id": toolID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return fmt.Errorf("tool call not found (PutToolCall may have failed earlier): %w", err)
		}
		return fmt.Errorf("failed to find tool call: %w", err)
	}

	// Unmarshal existing data
	tc := &model.ToolCall{}
	if err := unmarshalJSONOrBSON(doc.Data, tc); err != nil {
		return fmt.Errorf("failed to unmarshal tool call: %w", err)
	}

	// Calculate duration
	durationMs := now.Sub(tc.CreatedAt).Milliseconds()

	// Update the tool call fields
	tc.Response = response
	tc.ResponseLength = len([]rune(response)) // Character count
	tc.DurationMs = durationMs
	tc.UpdatedAt = now
	if execErr != nil {
		tc.Status = model.ToolCallStatusFailed
		tc.Error = execErr.Error()
	} else {
		tc.Status = model.ToolCallStatusSuccess
	}

	// Marshal back to BSON
	data, err := json.Marshal(tc)
	if err != nil {
		return fmt.Errorf("failed to marshal tool call: %w", err)
	}

	// Update document
	doc.Data = string(data)

	opts := options.Replace().SetUpsert(false)
	_, err = s.toolCallsCollection.ReplaceOne(ctx, bson.M{"_id": toolID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to update tool call response: %w", err)
	}

	return nil
}

// summarizationLogDocument represents a summarization log document in MongoDB
type summarizationLogDocument struct {
	ID        string    `bson:"_id"`
	SessionID string    `bson:"session_id"`
	Data      string    `bson:"data"` // JSON serialized SummarizationLog
	CreatedAt time.Time `bson:"created_at"`
}

// PutSummarizationLog stores a summarization log
func (s *MongoDBStore) PutSummarizationLog(log *model.SummarizationLog) error {
	if log == nil {
		return fmt.Errorf("log cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	data, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("failed to marshal summarization log: %w", err)
	}

	// Key by LogID, identical to the SQLite primary key, so the same log upserts
	// to the same document on both backends (contract parity).
	id := log.LogID
	doc := summarizationLogDocument{
		ID:        id,
		SessionID: log.SessionID,
		Data:      string(data),
		CreatedAt: log.CreatedAt,
	}

	opts := options.Replace().SetUpsert(true)
	_, err = s.summarizationLogsCollection.ReplaceOne(ctx, bson.M{"_id": id}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store summarization log: %w", err)
	}

	return nil
}

// GetSummarizationLogsBySession returns all summarization logs for a session
func (s *MongoDBStore) GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Newest first, matching the SQLite backend (contract parity).
	cursor, err := s.summarizationLogsCollection.Find(ctx, bson.M{"session_id": sessionID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query summarization logs: %w", err)
	}
	defer cursor.Close(ctx)

	var logs []*model.SummarizationLog
	for cursor.Next(ctx) {
		var doc summarizationLogDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode summarization log: %w", err)
		}

		log := &model.SummarizationLog{}
		if err := unmarshalJSONOrBSON(doc.Data, log); err != nil {
			return nil, fmt.Errorf("failed to unmarshal summarization log: %w", err)
		}

		logs = append(logs, log)
	}

	return logs, cursor.Err()
}

// GetAllSummarizationLogs returns all summarization logs
func (s *MongoDBStore) GetAllSummarizationLogs() ([]*model.SummarizationLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cursor, err := s.summarizationLogsCollection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to query summarization logs: %w", err)
	}
	defer cursor.Close(ctx)

	var logs []*model.SummarizationLog
	for cursor.Next(ctx) {
		var doc summarizationLogDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode summarization log: %w", err)
		}

		log := &model.SummarizationLog{}
		if err := unmarshalJSONOrBSON(doc.Data, log); err != nil {
			return nil, fmt.Errorf("failed to unmarshal summarization log: %w", err)
		}

		logs = append(logs, log)
	}

	return logs, cursor.Err()
}

// Ensure MongoDBStore implements model.SessionStore and debuger.DebugStore
var (
	_ model.SessionStore = (*MongoDBStore)(nil)
	_ debuger.DebugStore = (*MongoDBStore)(nil)
)
