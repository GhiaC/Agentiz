package store_test

import (
	"fmt"

	"github.com/ghiac/agentize/store"
)

func ExampleNewSQLiteStore() {
	// Create a SQLite store with a file path
	sqliteStore, err := store.NewSQLiteStore("./data/sessions.db")
	if err != nil {
		fmt.Printf("Error creating SQLite store: %v\n", err)
		return
	}
	defer sqliteStore.Close()

	// Use it as a SessionStore
	var sessionStore store.SessionStore = sqliteStore
	_ = sessionStore

	fmt.Println("SQLite store created successfully")
}

func ExampleNewSQLiteStore_memory() {
	// Create an in-memory SQLite store (for testing)
	sqliteStore, err := store.NewSQLiteStore("")
	if err != nil {
		fmt.Printf("Error creating SQLite store: %v\n", err)
		return
	}
	defer sqliteStore.Close()

	fmt.Println("In-memory SQLite store created successfully")
}

func ExampleNewSQLiteStoreFromFile() {
	// Create a SQLite store from a file path (convenience function)
	// Note: In production, use a real file path like "./data/sessions.db"
	// For testing, you can use ":memory:" for in-memory database
	sessionStore, err := store.NewSQLiteStoreFromFile(":memory:")
	if err != nil {
		fmt.Printf("Error creating SQLite store: %v\n", err)
		return
	}

	// Type assert to access Close() if needed
	if sqliteStore, ok := sessionStore.(*store.SQLiteStore); ok {
		defer sqliteStore.Close()
	}

	fmt.Println("SQLite store created successfully")
	// Output: SQLite store created successfully
}

func ExampleAgentizeWithSQLite() {
	// Example of using SQLiteStore with Agentize
	// This would be in your application code:

	/*
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
	*/

	fmt.Println("Example usage shown")
}

func ExampleNewMongoDBStore() {
	// Create a MongoDB store with configuration
	config := store.DefaultMongoDBStoreConfig()
	config.URI = "mongodb://localhost:27017"
	config.Database = "agentize"
	config.Collection = "sessions"

	mongoStore, err := store.NewMongoDBStore(config)
	if err != nil {
		fmt.Printf("Error creating MongoDB store: %v\n", err)
		return
	}
	defer mongoStore.Close()

	// Use it as a SessionStore
	var sessionStore store.SessionStore = mongoStore
	_ = sessionStore

	fmt.Println("MongoDB store created successfully")
}

func ExampleNewMongoDBStoreFromURI() {
	// Create a MongoDB store from a connection URI (convenience function)
	// Note: This example requires a running MongoDB instance
	// In production, use a real MongoDB URI like "mongodb://localhost:27017"
	sessionStore, err := store.NewMongoDBStoreFromURI("mongodb://localhost:27017")
	if err != nil {
		// In real usage, handle the error appropriately
		fmt.Printf("Error creating MongoDB store: %v\n", err)
		return
	}

	// Type assert to access Close() if needed
	if mongoStore, ok := sessionStore.(*store.MongoDBStore); ok {
		defer mongoStore.Close()
	}

	fmt.Println("MongoDB store created successfully")
	// Note: Output varies depending on MongoDB availability and configuration
	// If MongoDB is available: "MongoDB store created successfully"
	// If MongoDB requires auth: "Error creating MongoDB store: failed to create indexes: ..."
	// If MongoDB is not running: "Error creating MongoDB store: failed to ping MongoDB: ..."
}

func ExampleAgentizeWithMongoDB() {
	// Example of using MongoDBStore with Agentize
	// This would be in your application code:

	/*
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
	*/

	fmt.Println("Example usage shown")
}
