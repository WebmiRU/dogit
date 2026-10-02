// Package objects stores large, immutable blobs outside the application.
//
// Artifacts, build caches and package archives do not belong in the database and
// do not belong on a container's writable layer: they need to survive a restart,
// be shared between instances, and be reachable from a runner that may run
// anywhere. S3-compatible object storage gives all three without making the
// application aware of where it physically lives.
//
// The interface is deliberately small — put, get, stat, list, remove — because a
// narrow surface is what keeps the Kubernetes migration mechanical: swapping the
// backend touches this file and nothing else.
package objects

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("object not found")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ETag         string    `json:"etag,omitempty"`
	ContentType  string    `json:"content_type,omitempty"`
}

// Store is the blob storage the application uses.
type Store interface {
	// Put writes an object, replacing any previous one with the same key.
	Put(ctx context.Context, key string, content io.Reader, size int64, contentType string) (ObjectInfo, error)
	// Get returns a reader. The caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// Stat describes an object without downloading it.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// List returns the objects under a prefix, most recent first.
	List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error)
	// Remove deletes an object. Removing a missing object is not an error.
	Remove(ctx context.Context, key string) error
	// Prefix returns the namespace this store writes under, so callers do not
	// have to know how keys are laid out.
	Prefix() string
}

// Local is a filesystem-backed Store.
//
// It exists for two reasons: a development instance should start without any
// credentials, and the runner must work on a machine with no network. It is a
// drop-in for the S3 store, so nothing above this package has to branch on which
// one it got.
type Local struct {
	root string
	// prefix namespaces everything, mirroring the key layout of the remote store.
	prefix string
}

func NewLocal(root, prefix string) (*Local, error) {
	if root == "" {
		return nil, fmt.Errorf("objects: a local root is required")
	}
	if prefix == "" {
		prefix = "dogit"
	}
	return &Local{root: root, prefix: strings.Trim(prefix, "/")}, nil
}

func (l *Local) Prefix() string { return l.prefix }

// Key joins the namespace and a relative key, rejecting anything that would
// escape the store.
func (l *Local) Key(relative string) string {
	return l.prefix + "/" + strings.TrimPrefix(relative, "/")
}

// path resolves a full key to a path on disk, refusing traversal.
func (l *Local) path(key string) (string, error) {
	if strings.Contains(key, "..") {
		return "", fmt.Errorf("objects: invalid key %q", key)
	}
	return filepath.Join(l.root, filepath.FromSlash(key)), nil
}

func (l *Local) Put(ctx context.Context, key string, content io.Reader, size int64, contentType string) (ObjectInfo, error) {
	full, err := l.path(l.Key(key))
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return ObjectInfo{}, fmt.Errorf("objects: create directory: %w", err)
	}

	// Write to a temporary file and rename: a reader that disappears mid-write must
	// not leave a half-written object that later looks like a valid artifact.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objects: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())

	written, err := io.Copy(tmp, content)
	if err != nil {
		tmp.Close()
		return ObjectInfo{}, fmt.Errorf("objects: write: %w", err)
	}
	if size >= 0 && written != size {
		tmp.Close()
		return ObjectInfo{}, fmt.Errorf("objects: expected %d bytes, wrote %d", size, written)
	}
	if err := tmp.Close(); err != nil {
		return ObjectInfo{}, fmt.Errorf("objects: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return ObjectInfo{}, fmt.Errorf("objects: publish: %w", err)
	}

	info, _ := os.Stat(full)
	object := ObjectInfo{Key: key, ContentType: contentType}
	if info != nil {
		object.Size = info.Size()
		object.LastModified = info.ModTime()
	}
	return object, nil
}

func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	full, err := l.path(l.Key(key))
	if err != nil {
		return nil, ObjectInfo{}, err
	}

	file, err := os.Open(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("objects: open: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, ObjectInfo{}, fmt.Errorf("objects: stat: %w", err)
	}
	return file, ObjectInfo{
		Key:          key,
		Size:         info.Size(),
		LastModified: info.ModTime(),
	}, nil
}

func (l *Local) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	full, err := l.path(l.Key(key))
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Stat(full)
	if errors.Is(err, os.ErrNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("objects: stat: %w", err)
	}
	return ObjectInfo{Key: key, Size: info.Size(), LastModified: info.ModTime()}, nil
}

func (l *Local) List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	root := filepath.Join(l.root, filepath.FromSlash(l.prefix))
	entries := []ObjectInfo{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A missing prefix is an empty listing, not a failure: callers ask for
			// prefixes that may have nothing stored yet.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if !strings.HasPrefix(relative, strings.TrimPrefix(prefix, "/")) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, ObjectInfo{
			Key:          relative,
			Size:         info.Size(),
			LastModified: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("objects: list: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastModified.After(entries[j].LastModified)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (l *Local) Remove(ctx context.Context, key string) error {
	full, err := l.path(l.Key(key))
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("objects: remove: %w", err)
	}
	return nil
}
