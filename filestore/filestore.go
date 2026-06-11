// Package filestore provides pluggable storage for raw user-file bytes.
//
// Metadata about files (owner, name, MIME type, size, ...) lives in the database
// as model.UserFile records; this package stores only the bytes, keyed by an
// opaque StorageKey. The default implementation writes to local disk
// (LocalFileStore); alternative backends (S3, MinIO, ...) can implement the
// FileStore interface without touching the rest of the system.
package filestore

import "io"

// FileStore is the pluggable interface for storing and retrieving file bytes.
// Keys use forward slashes ("/") as separators regardless of platform.
type FileStore interface {
	// Save writes the bytes read from r under key and returns the canonical
	// storage key that should be persisted in the file's metadata. The returned
	// key may differ from the input key (e.g. a backend may add a prefix).
	Save(key string, r io.Reader) (string, error)

	// Open returns a ReadCloser for the object stored under key. The caller is
	// responsible for closing it.
	Open(key string) (io.ReadCloser, error)

	// Delete removes the object stored under key. Deleting a missing object is
	// not an error.
	Delete(key string) error

	// Stat reports the size in bytes and whether an object exists for key.
	Stat(key string) (size int64, exists bool, err error)
}
