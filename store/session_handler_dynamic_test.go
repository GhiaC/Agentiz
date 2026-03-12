// Tests that need a *model.User use in-memory SQLite: store.NewSQLiteStore(":memory:")
// then sqliteStore.GetOrCreateUser(userID). SessionHandler is created with
// model.NewSessionHandler(sqliteStore, model.DefaultSessionHandlerConfig()).
package store_test

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func getSessionHandler(t *testing.T, disableLogs bool) (*model.SessionHandler, *store.SQLiteStore) {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	config := model.DefaultSessionHandlerConfig()
	config.DisableLogs = disableLogs
	sh := model.NewSessionHandler(sqliteStore, config)
	return sh, sqliteStore
}

func TestRegisterAgentDisplayName(t *testing.T) {
	sh, _ := getSessionHandler(t, false)
	sh.RegisterAgentDisplayName(model.AgentType("researcher"), "Research Agent")
	got := sh.AgentTypeDisplayName(model.AgentType("researcher"))
	if got != "Research Agent" {
		t.Errorf("AgentTypeDisplayName(researcher): got %q, want %q", got, "Research Agent")
	}
}

func TestAgentTypeDisplayName_BuiltinFallback(t *testing.T) {
	sh, _ := getSessionHandler(t, false)
	if got := sh.AgentTypeDisplayName(model.AgentTypeHigh); got != "UserAgent-High" {
		t.Errorf("AgentTypeHigh: got %q, want UserAgent-High", got)
	}
	if got := sh.AgentTypeDisplayName(model.AgentTypeLow); got != "UserAgent-Low" {
		t.Errorf("AgentTypeLow: got %q, want UserAgent-Low", got)
	}
	if got := sh.AgentTypeDisplayName(model.AgentTypeCore); got != "Core" {
		t.Errorf("AgentTypeCore: got %q, want Core", got)
	}
}

func TestAgentTypeDisplayName_UnknownFallback(t *testing.T) {
	sh, _ := getSessionHandler(t, false)
	got := sh.AgentTypeDisplayName(model.AgentType("xyz"))
	if got != "xyz" {
		t.Errorf("AgentType(xyz): got %q, want xyz", got)
	}
}

func TestAgentTypeDisplayName_OverrideBuiltin(t *testing.T) {
	sh, _ := getSessionHandler(t, false)
	sh.RegisterAgentDisplayName(model.AgentTypeHigh, "Custom High")
	got := sh.AgentTypeDisplayName(model.AgentTypeHigh)
	if got != "Custom High" {
		t.Errorf("override AgentTypeHigh: got %q, want Custom High", got)
	}
}

func TestDeleteSession_ClearsActiveForAnyAgentType(t *testing.T) {
	sh, sqliteStore := getSessionHandler(t, false)
	user, err := sqliteStore.GetOrCreateUser("user1")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	session, err := sh.CreateSessionForUser(user, model.AgentType("custom_agent"))
	if err != nil {
		t.Fatalf("CreateSessionForUser: %v", err)
	}

	user2, _ := sqliteStore.GetOrCreateUser("user1")
	if user2.GetActiveSessionID(model.AgentType("custom_agent")) != session.SessionID {
		t.Error("active session should be set before delete")
	}

	if err := sh.DeleteSession(session.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	user3, _ := sqliteStore.GetOrCreateUser("user1")
	if user3.GetActiveSessionID(model.AgentType("custom_agent")) != "" {
		t.Errorf("active session should be cleared after delete, got %q", user3.GetActiveSessionID(model.AgentType("custom_agent")))
	}
}

func TestGetSessionsPrompt_DynamicOrdering(t *testing.T) {
	sh, sqliteStore := getSessionHandler(t, true)

	userID := "user1"
	user, err := sqliteStore.GetOrCreateUser(userID)
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	for _, at := range []model.AgentType{model.AgentTypeHigh, model.AgentTypeLow, model.AgentTypeCore, model.AgentType("custom")} {
		_, err := sh.CreateSessionForUser(user, at)
		if err != nil {
			t.Fatalf("CreateSessionForUser %s: %v", at, err)
		}
		user, _ = sqliteStore.GetOrCreateUser(userID)
	}

	prompt, err := sh.GetSessionsPrompt(userID)
	if err != nil {
		t.Fatalf("GetSessionsPrompt: %v", err)
	}
	// Well-known types first: UserAgent-High, UserAgent-Low, Core; then custom
	idxHigh := strings.Index(prompt, "### UserAgent-High Sessions:")
	idxLow := strings.Index(prompt, "### UserAgent-Low Sessions:")
	idxCore := strings.Index(prompt, "### Core Sessions:")
	idxCustom := strings.Index(prompt, "### custom Sessions:")
	if idxHigh < 0 || idxLow < 0 || idxCore < 0 || idxCustom < 0 {
		t.Errorf("missing section in prompt: high=%d low=%d core=%d custom=%d", idxHigh, idxLow, idxCore, idxCustom)
	}
	if idxHigh >= idxLow || idxLow >= idxCore || idxCore >= idxCustom {
		t.Error("order should be: UserAgent-High, UserAgent-Low, Core, custom")
	}
}

func TestGetAgentTypeOrder(t *testing.T) {
	sh, sqliteStore := getSessionHandler(t, true)

	userID := "user2"
	user, err := sqliteStore.GetOrCreateUser(userID)
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	for _, at := range []model.AgentType{model.AgentTypeHigh, model.AgentType("custom_a"), model.AgentType("custom_b"), model.AgentTypeLow} {
		_, err := sh.CreateSessionForUser(user, at)
		if err != nil {
			t.Fatalf("CreateSessionForUser %s: %v", at, err)
		}
		user, _ = sqliteStore.GetOrCreateUser(userID)
	}

	prompt, err := sh.GetSessionsPrompt(userID)
	if err != nil {
		t.Fatalf("GetSessionsPrompt: %v", err)
	}
	// getAgentTypeOrder: well-known (high, low, core) then extras alphabetical -> high, low, custom_a, custom_b
	idxHigh := strings.Index(prompt, "### UserAgent-High Sessions:")
	idxLow := strings.Index(prompt, "### UserAgent-Low Sessions:")
	idxA := strings.Index(prompt, "### custom_a Sessions:")
	idxB := strings.Index(prompt, "### custom_b Sessions:")
	if idxHigh < 0 || idxLow < 0 || idxA < 0 || idxB < 0 {
		t.Errorf("missing section: high=%d low=%d custom_a=%d custom_b=%d", idxHigh, idxLow, idxA, idxB)
	}
	if idxHigh >= idxLow || idxLow >= idxA || idxA >= idxB {
		t.Error("order should be: UserAgent-High, UserAgent-Low, custom_a, custom_b")
	}
}
