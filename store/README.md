# Store Package

This package provides storage implementations for Agentize sessions.

## Available Stores

### DBStore (Default)
In-memory storage that doesn't persist data across restarts. Suitable for testing and development.

```go
import "github.com/ghiac/agentize/store"

dbStore := store.NewDBStore()
```

### SQLiteStore
SQLite-based storage that persists data to a file. Suitable for production use when you don't have MongoDB available.

```go
import "github.com/ghiac/agentize/store"

// File-based storage
sqliteStore, err := store.NewSQLiteStore("./data/sessions.db")
if err != nil {
    log.Fatal(err)
}
defer sqliteStore.Close()

// In-memory storage (for testing)
sqliteStore, err := store.NewSQLiteStore("")
if err != nil {
    log.Fatal(err)
}
defer sqliteStore.Close()
```

### MongoDBStore
MongoDB-based storage for production use. Provides better scalability and performance for large-scale deployments.

```go
import "github.com/ghiac/agentize/store"

// Using default configuration
config := store.DefaultMongoDBStoreConfig()
config.URI = "mongodb://localhost:27017"
config.Database = "agentize"
config.Collection = "sessions"

mongoStore, err := store.NewMongoDBStore(config)
if err != nil {
    log.Fatal(err)
}
defer mongoStore.Close()

// Or use convenience function
mongoStore, err := store.NewMongoDBStoreFromURI("mongodb://localhost:27017")
if err != nil {
    log.Fatal(err)
}
defer mongoStore.Close()
```

## Usage with Agentize

### Using SQLiteStore

```go
import (
    "github.com/ghiac/agentize"
    "github.com/ghiac/agentize/store"
)

// Create SQLite store
sqliteStore, err := store.NewSQLiteStore("./data/sessions.db")
if err != nil {
    log.Fatal(err)
}
defer sqliteStore.Close()

// Create Agentize with SQLite store
ag, err := agentize.NewWithOptions("./knowledge", &agentize.Options{
    SessionStore: sqliteStore,
})
if err != nil {
    log.Fatal(err)
}

// Now Agentize will use SQLite for session persistence
```

### Using MongoDBStore

```go
import (
    "github.com/ghiac/agentize"
    "github.com/ghiac/agentize/store"
)

// Create MongoDB store
config := store.DefaultMongoDBStoreConfig()
config.URI = "mongodb://localhost:27017"
config.Database = "agentize"
config.Collection = "sessions"

mongoStore, err := store.NewMongoDBStore(config)
if err != nil {
    log.Fatal(err)
}
defer mongoStore.Close()

// Create Agentize with MongoDB store
ag, err := agentize.NewWithOptions("./knowledge", &agentize.Options{
    SessionStore: mongoStore,
})
if err != nil {
    log.Fatal(err)
}

// Now Agentize will use MongoDB for session persistence
```

## Custom Store Implementation

You can implement your own store by implementing the `model.SessionStore` interface:

```go
type SessionStore interface {
    Get(sessionID string) (*Session, error)
    Put(session *Session) error
    Delete(sessionID string) error
    List(userID string) ([]*Session, error)
}
```

For MongoDB implementation, you would create a similar structure to `SQLiteStore` but use MongoDB client instead.

## Database Schema

SQLiteStore uses the following schema:

```sql
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    agent_type TEXT NOT NULL,
    data TEXT NOT NULL,  -- JSON serialized Session
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_updated_at ON sessions(updated_at);
CREATE UNIQUE INDEX idx_sessions_user_core ON sessions(user_id, agent_type) WHERE agent_type = 'core';
```

## Core Session Uniqueness

**Important**: For each user, there can be only **one Core session**. This is enforced by:

1. **Database constraint**: A unique index ensures only one Core session per user
2. **PutCoreSession method**: Automatically deletes any existing Core sessions before storing a new one
3. **Put method**: Automatically routes Core sessions to `PutCoreSession` to enforce uniqueness

### Helper Methods for Core Sessions

```go
// Get the Core session for a user (returns nil if none exists)
coreSession, err := sqliteStore.GetCoreSession("user123")

// Store/update Core session (replaces any existing Core session for the user)
err := sqliteStore.PutCoreSession(coreSession)

// Regular Put also works - it automatically handles Core session uniqueness
err := sqliteStore.Put(coreSession) // Same as PutCoreSession for Core sessions
```

## Notes

- **SQLiteStore**: Uses JSON serialization for Session objects. All timestamps are stored as Unix timestamps (integers). Perfect for single-server deployments or when you don't want to manage a separate database server.

- **MongoDBStore**: Uses MongoDB's native BSON storage with JSON serialization for Session data. Provides better scalability, replication, and sharding capabilities. Ideal for production deployments with multiple servers.

- **Core Session Uniqueness**: Both stores enforce that only one Core session exists per user. This is handled automatically through:
  - SQLite: Unique index with partial filter
  - MongoDB: Unique index with partial filter expression

- **UserNodes**: Visited nodes are still stored in-memory for performance (same for all store implementations)

- **Backward Compatibility**: All stores implement the same `SessionStore` interface, so you can switch between them without changing your code
