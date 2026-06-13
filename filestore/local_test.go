package filestore

import (
	"io"
	"strings"
	"testing"
)

func newStore(t *testing.T) *LocalFileStore {
	t.Helper()
	st, err := NewLocalFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFileStore: %v", err)
	}
	return st
}

func TestLocalFileStore_SaveOpenDeleteStat(t *testing.T) {
	st := newStore(t)

	key, err := st.Save("user-1/uf-1-report.pdf", strings.NewReader("hello bytes"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if key != "user-1/uf-1-report.pdf" {
		t.Errorf("Save returned %q, want the key unchanged", key)
	}

	size, exists, err := st.Stat(key)
	if err != nil || !exists || size != int64(len("hello bytes")) {
		t.Fatalf("Stat = (%d, %v, %v), want (11, true, nil)", size, exists, err)
	}

	r, err := st.Open(key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "hello bytes" {
		t.Errorf("round-trip mismatch: %q", data)
	}

	if err := st.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, exists, _ := st.Stat(key); exists {
		t.Error("file still exists after Delete")
	}
	// Deleting a missing file is not an error.
	if err := st.Delete(key); err != nil {
		t.Errorf("Delete of missing file: %v", err)
	}
}

func TestLocalFileStore_SaveRejectsUnnamespacedKeys(t *testing.T) {
	st := newStore(t)

	// Flat keys collide across callers — rejected.
	for _, key := range []string{"report.pdf", "/report.pdf", "report.pdf/"} {
		if _, err := st.Save(key, strings.NewReader("x")); err == nil {
			t.Errorf("Save(%q) accepted an unnamespaced key", key)
		}
	}
	// Namespaced keys are fine.
	if _, err := st.Save("user-1/report.pdf", strings.NewReader("x")); err != nil {
		t.Errorf("Save namespaced key: %v", err)
	}
}

func TestLocalFileStore_PathTraversalRejected(t *testing.T) {
	st := newStore(t)

	for _, key := range []string{"../escape.txt", "user/../../escape.txt", ""} {
		if _, err := st.Open(key); err == nil {
			t.Errorf("Open(%q) accepted a traversal/invalid key", key)
		}
	}
	// ".." that normalizes INSIDE the base dir is acceptable.
	if _, err := st.Save("user-1/sub/../inside.txt", strings.NewReader("x")); err != nil {
		t.Errorf("Save normalized in-base key: %v", err)
	}
}
