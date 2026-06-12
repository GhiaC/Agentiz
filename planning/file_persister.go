package planning

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FilePersister is a reference Persister that stores plans as JSON lines (one
// plan per line) in a single file. Pair it with MemoryStore via WithPersister
// so plans survive restarts:
//
//	p := planning.NewFilePersister("data/plans.jsonl")
//	store := planning.NewMemoryStore(planning.WithPersister(p))
//	_ = store.LoadFromPersister(ctx) // at startup
//	// ... use store; call store.Shutdown(ctx) on graceful shutdown
//
// It is suitable for small deployments and as an example for DB-backed
// persisters.
type FilePersister struct {
	mu   sync.Mutex
	path string
}

// NewFilePersister returns a FilePersister writing to the given file path.
// Parent directories are created on save.
func NewFilePersister(path string) *FilePersister {
	return &FilePersister{path: path}
}

// LoadPlans reads all plans from the file. A missing file yields no plans and
// no error (first run).
func (f *FilePersister) LoadPlans(ctx context.Context) ([]*Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, err := os.Open(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("planning: file persister: open: %w", err)
	}
	defer file.Close()

	var plans []*Plan
	sc := bufio.NewScanner(file)
	// Plans can carry long step outputs; allow lines well beyond the default 64K.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var p Plan
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("planning: file persister: parse plan line: %w", err)
		}
		plans = append(plans, &p)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("planning: file persister: read: %w", err)
	}
	return plans, nil
}

// SavePlans writes all plans to the file, replacing its previous contents. The
// write goes to a temp file first and is renamed into place so a crash mid-save
// never corrupts the existing data.
func (f *FilePersister) SavePlans(ctx context.Context, plans []*Plan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dir := filepath.Dir(f.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("planning: file persister: mkdir: %w", err)
		}
	}
	tmp := f.path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("planning: file persister: create: %w", err)
	}
	w := bufio.NewWriter(file)
	for _, p := range plans {
		if p == nil {
			continue
		}
		data, err := json.Marshal(p)
		if err != nil {
			file.Close()
			return fmt.Errorf("planning: file persister: marshal plan %s: %w", p.ID, err)
		}
		w.Write(data)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		file.Close()
		return fmt.Errorf("planning: file persister: write: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("planning: file persister: close: %w", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("planning: file persister: rename: %w", err)
	}
	return nil
}
