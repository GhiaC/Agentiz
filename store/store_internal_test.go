package store

import "strings"
import "testing"

// TestRedactURI ensures connection-string credentials never reach diagnostics.
func TestRedactURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"mongodb://localhost:27017", "mongodb://localhost:27017"},
		{"mongodb://user:pass@localhost:27017/agentize", "mongodb://***@localhost:27017/agentize"},
		{"mongodb://user:pass@h1,h2:27017/db?replicaSet=rs", "mongodb://***@h1,h2:27017/db?replicaSet=rs"},
		{"mongodb+srv://admin:secret@cluster.mongodb.net/db", "mongodb+srv://***@cluster.mongodb.net/db"},
		{"mongodb://user@host/db", "mongodb://***@host/db"},
		{"./data/sessions.db", "./data/sessions.db"},
	}
	for _, c := range cases {
		got := redactURI(c.in)
		if got != c.want {
			t.Errorf("redactURI(%q) = %q, want %q", c.in, got, c.want)
		}
		// Defense in depth: a password must never survive redaction.
		if strings.Contains(got, "pass") || strings.Contains(got, "secret") {
			t.Errorf("redactURI(%q) leaked credentials: %q", c.in, got)
		}
	}
}

// TestSQLitePathLabel covers the human-friendly path label used in BackendInfo.
func TestSQLitePathLabel(t *testing.T) {
	if got := sqlitePathLabel(""); got != "in-memory" {
		t.Errorf("sqlitePathLabel(\"\") = %q, want in-memory", got)
	}
	if got := sqlitePathLabel(":memory:"); got != "in-memory" {
		t.Errorf("sqlitePathLabel(:memory:) = %q, want in-memory", got)
	}
	if got := sqlitePathLabel("./data/sessions.db"); got != "./data/sessions.db" {
		t.Errorf("sqlitePathLabel(path) = %q, want ./data/sessions.db", got)
	}
}
