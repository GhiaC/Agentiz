package core

import (
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

// putUserFile inserts a minimal uploaded user file directly into the store so the
// User Files prompt section and the cache behavior can be exercised.
func putUserFile(t *testing.T, st interface {
	PutUserFile(*model.UserFile) error
}, userID, fileID, name string) {
	t.Helper()
	uf := &model.UserFile{
		FileID:    fileID,
		UserID:    userID,
		SessionID: userID + "-low-s0001",
		Name:      name,
		MIMEType:  "application/pdf",
		Size:      2048,
		Source:    model.FileSourceUploaded,
		CreatedAt: time.Now(),
	}
	if err := st.PutUserFile(uf); err != nil {
		t.Fatalf("PutUserFile: %v", err)
	}
}

func TestBuildUserFilesPrompt_ListsFiles(t *testing.T) {
	ch, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	if got := ch.buildUserFilesPrompt("u1"); got != "" {
		t.Fatalf("expected empty User Files section when user has no files, got: %q", got)
	}

	putUserFile(t, sqliteStore, "u1", "u1-low-s0001-uf0001", "report.pdf")

	got := ch.buildUserFilesPrompt("u1")
	if !strings.Contains(got, "# User Files") {
		t.Errorf("User Files section missing header; got: %q", got)
	}
	if !strings.Contains(got, "u1-low-s0001-uf0001") || !strings.Contains(got, "report.pdf") {
		t.Errorf("User Files section should list the file ID and name; got: %q", got)
	}
}

func TestGenerateSystemPrompt_CachesUntilInvalidated(t *testing.T) {
	ch, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	// First build: no files yet, so the file must not be listed. This populates the
	// cache. (We assert on the file ID, not a "# User Files" header — the controller
	// prompt itself documents a "## User Files" section, which contains that substring.)
	const fileID = "u1-low-s0001-uf0001"
	first, err := ch.generateSystemPrompt("u1", nil)
	if err != nil {
		t.Fatalf("generateSystemPrompt (first): %v", err)
	}
	if strings.Contains(strings.Join(first, " "), fileID) {
		t.Fatal("did not expect any file listed before one was added")
	}

	// A file arrives, but the cache is still fresh → it must NOT appear yet.
	putUserFile(t, sqliteStore, "u1", fileID, "report.pdf")
	cached, err := ch.generateSystemPrompt("u1", nil)
	if err != nil {
		t.Fatalf("generateSystemPrompt (cached): %v", err)
	}
	if strings.Contains(strings.Join(cached, " "), fileID) {
		t.Error("cached system prompt should not reflect the new file until invalidated")
	}

	// After invalidation the next build picks up the new file.
	ch.invalidateSystemPrompt("u1")
	rebuilt, err := ch.generateSystemPrompt("u1", nil)
	if err != nil {
		t.Fatalf("generateSystemPrompt (rebuilt): %v", err)
	}
	if !strings.Contains(strings.Join(rebuilt, " "), "report.pdf") {
		t.Error("after invalidation the system prompt should list the new file")
	}
}

func TestGenerateSystemPrompt_RebuildsWhenCoreSessionSummarized(t *testing.T) {
	ch, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	cs := &model.Session{UserID: "u1", AgentType: model.AgentTypeCore}

	if _, err := ch.generateSystemPrompt("u1", cs); err != nil {
		t.Fatalf("generateSystemPrompt (first): %v", err)
	}

	// New file + still-fresh cache + no new summarization → it stays absent.
	const fileID = "u1-low-s0001-uf0001"
	putUserFile(t, sqliteStore, "u1", fileID, "report.pdf")
	cached, _ := ch.generateSystemPrompt("u1", cs)
	if strings.Contains(strings.Join(cached, " "), fileID) {
		t.Error("expected cached prompt while SummarizedAt is unchanged")
	}

	// A background summarization advances SummarizedAt past the cache build time →
	// the memory is stale, so the prompt must be rebuilt even within the TTL.
	cs.SummarizedAt = time.Now().Add(time.Hour)
	rebuilt, _ := ch.generateSystemPrompt("u1", cs)
	if !strings.Contains(strings.Join(rebuilt, " "), "report.pdf") {
		t.Error("a newer SummarizedAt should force a rebuild of the cached system prompt")
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1048576: "1.0 MB",
	}
	for in, want := range cases {
		if got := humanizeBytes(in); got != want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
