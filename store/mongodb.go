package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoDBStore is a MongoDB implementation of SessionStore
// It stores sessions in a MongoDB collection with JSON serialization
type MongoDBStore struct {
	client     *mongo.Client
	database   *mongo.Database
	collection *mongo.Collection
	mu         sync.RWMutex

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

	clientOptions := options.Client().ApplyURI(config.URI)
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
		client:     client,
		database:   database,
		collection: collection,
		userLock:   make(map[string]*sync.Mutex),
	}

	// Create indexes
	if err := store.initIndexes(ctx); err != nil {
		client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to create indexes: %w", err)
	}

	return store, nil
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

	return nil
}

// Close closes the MongoDB connection
func (s *MongoDBStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	SessionID string    `bson:"_id"`
	UserID    string    `bson:"user_id"`
	AgentType string    `bson:"agent_type"`
	Data      string    `bson:"data"` // JSON serialized Session
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
}

// Get retrieves a session by ID
func (s *MongoDBStore) Get(sessionID string) (*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	if err := json.Unmarshal([]byte(doc.Data), session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Restore timestamps
	session.CreatedAt = doc.CreatedAt
	session.UpdatedAt = doc.UpdatedAt

	return session, nil
}

// Put stores or updates a session
// For Core sessions, this ensures only one Core session exists per user
func (s *MongoDBStore) Put(session *model.Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}

	// For Core sessions, use PutCoreSession to ensure uniqueness
	if session.AgentType == model.AgentTypeCore {
		return s.PutCoreSession(session)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session.UpdatedAt = time.Now()

	// Serialize session to JSON
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	doc := sessionDocument{
		SessionID: session.SessionID,
		UserID:    session.UserID,
		AgentType: string(session.AgentType),
		Data:      string(data),
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.collection.DeleteOne(ctx, bson.M{"_id": sessionID})
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

// List returns all sessions for a user
func (s *MongoDBStore) List(userID string) ([]*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
		if err := json.Unmarshal([]byte(doc.Data), session); err != nil {
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

// GetCoreSession returns the Core session for a user
// For each user, there should be only one Core session
// If no Core session exists, it returns nil without error
func (s *MongoDBStore) GetCoreSession(userID string) (*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	if err := json.Unmarshal([]byte(doc.Data), session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Restore timestamps
	session.CreatedAt = doc.CreatedAt
	session.UpdatedAt = doc.UpdatedAt

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

	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Serialize session to JSON
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	doc := sessionDocument{
		SessionID: session.SessionID,
		UserID:    session.UserID,
		AgentType: string(session.AgentType),
		Data:      string(data),
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
	}

	_, err = s.collection.InsertOne(ctx, doc)
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

// Ensure MongoDBStore implements model.SessionStore
var _ model.SessionStore = (*MongoDBStore)(nil)
