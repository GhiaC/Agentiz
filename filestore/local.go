package filestore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalFileStore stores file bytes on the local filesystem under a base
// directory. Keys are treated as forward-slash separated relative paths and are
// mapped onto the OS filesystem beneath baseDir.
type LocalFileStore struct {
	baseDir string
}

// Ensure LocalFileStore implements FileStore.
var _ FileStore = (*LocalFileStore)(nil)

// NewLocalFileStore creates a LocalFileStore rooted at baseDir, creating the
// directory if it does not exist.
func NewLocalFileStore(baseDir string) (*LocalFileStore, error) {
	if baseDir == "" {
		baseDir = "./data/files"
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create file store directory: %w", err)
	}
	return &LocalFileStore{baseDir: baseDir}, nil
}

// BaseDir returns the root directory under which file bytes are stored. Used by
// the debug dashboard to report where uploaded/generated files live on disk.
func (l *LocalFileStore) BaseDir() string {
	return l.baseDir
}

// keyToPath maps a forward-slash storage key to an absolute filesystem path
// beneath baseDir, rejecting any key that would escape the base directory.
func (l *LocalFileStore) keyToPath(key string) (string, error) {
	clean := filepath.Clean("/" + filepath.FromSlash(key)) // force key to be rooted
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("invalid storage key: %q", key)
	}
	full := filepath.Join(l.baseDir, clean)
	// Guard against path traversal: full must remain inside baseDir.
	rel, err := filepath.Rel(l.baseDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid storage key (path traversal): %q", key)
	}
	return full, nil
}

// Save writes r to the file identified by key and returns key unchanged.
func (l *LocalFileStore) Save(key string, r io.Reader) (string, error) {
	path, err := l.keyToPath(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("failed to create file directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	return key, nil
}

// Open returns a ReadCloser for the file identified by key.
func (l *LocalFileStore) Open(key string) (io.ReadCloser, error) {
	path, err := l.keyToPath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return f, nil
}

// Delete removes the file identified by key. A missing file is not an error.
func (l *LocalFileStore) Delete(key string) error {
	path, err := l.keyToPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

// Stat reports the size and existence of the file identified by key.
func (l *LocalFileStore) Stat(key string) (int64, bool, error) {
	path, err := l.keyToPath(key)
	if err != nil {
		return 0, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("failed to stat file: %w", err)
	}
	return info.Size(), true, nil
}
