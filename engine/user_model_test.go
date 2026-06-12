package engine

import "testing"

func TestUserModelOverride(t *testing.T) {
	e := &Engine{}

	if got := e.UserModelOverride("u1"); got != "" {
		t.Fatalf("expected empty override initially, got %q", got)
	}

	e.SetUserModel("u1", "openai/gpt-5")
	if got := e.UserModelOverride("u1"); got != "openai/gpt-5" {
		t.Fatalf("override not set, got %q", got)
	}
	// Trimmed on read/write.
	if got := e.UserModelOverride("  u1  "); got != "openai/gpt-5" {
		t.Fatalf("userID should be trimmed, got %q", got)
	}
	// Other users unaffected.
	if got := e.UserModelOverride("u2"); got != "" {
		t.Fatalf("u2 should have no override, got %q", got)
	}

	// Clearing reverts to engine default ("").
	e.SetUserModel("u1", "")
	if got := e.UserModelOverride("u1"); got != "" {
		t.Fatalf("override not cleared, got %q", got)
	}

	// Blank userID is a no-op (must not store under the empty key).
	e.SetUserModel("   ", "x")
	if got := e.UserModelOverride(""); got != "" {
		t.Fatalf("blank user must not store an override, got %q", got)
	}
}
