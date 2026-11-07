// Package apifiles snapshots API files for content-addressed delivery.
package apifiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"
)

const (
	// MaxBytes caps a complete API bundle, outside the manifest's KV limit.
	MaxBytes = 32 << 20
	// MaxFiles bounds the number of documents in a bundle.
	MaxFiles                 = 256
	readOnlyMode fs.FileMode = 0o444
)

// ErrBundle is an invalid or oversized API file bundle.
var ErrBundle = errors.New("route: invalid API file bundle")

// Files is an immutable snapshot. Open never returns a directory listing.
type Files struct {
	data map[string][]byte
	hash string
}

// Snapshot reads only regular files, bounds total size and validates the entry.
func Snapshot(source fs.FS, entry string) (*Files, error) {
	if source == nil || !fs.ValidPath(entry) || entry == "." {
		return nil, fmt.Errorf("%w: entry must be a relative file path", ErrBundle)
	}

	files := &Files{data: map[string][]byte{}}
	hash := sha256.New()
	total := 0

	err := fs.WalkDir(source, ".", func(name string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if item.IsDir() {
			return nil
		}

		if !item.Type().IsRegular() || len(files.data) >= MaxFiles {
			return ErrBundle
		}

		info, err := item.Info()
		if err != nil {
			return fmt.Errorf("stat API file %s: %w", name, err)
		}

		if info.Size() > int64(MaxBytes-total) {
			return ErrBundle
		}

		file, err := source.Open(name)
		if err != nil {
			return fmt.Errorf("open API file %s: %w", name, err)
		}

		data, readErr := io.ReadAll(io.LimitReader(file, int64(MaxBytes-total)+1))

		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}

		total += len(data)
		if total > MaxBytes {
			return ErrBundle
		}

		files.data[name] = data
		hashFile(hash, name, data)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBundle, err)
	}

	if _, ok := files.data[entry]; !ok {
		return nil, fmt.Errorf("%w: entry %q not found", ErrBundle, entry)
	}

	files.hash = hex.EncodeToString(hash.Sum(nil))

	return files, nil
}

// Of is the snapshot of one file, addressed as Snapshot addresses it.
func Of(name string, data []byte) (*Files, error) {
	switch {
	case !fs.ValidPath(name) || name == ".":
		return nil, fmt.Errorf("%w: %q is not a relative file path", ErrBundle, name)
	case len(data) > MaxBytes:
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrBundle, name, MaxBytes)
	}

	hash := sha256.New()
	hashFile(hash, name, data)

	return &Files{data: map[string][]byte{name: data}, hash: hex.EncodeToString(hash.Sum(nil))}, nil
}

// hashFile adds one file, its name and length delimited, to the hash.
func hashFile(h io.Writer, name string, data []byte) {
	fmt.Fprintf(h, "%d:%s:%d:", len(name), name, len(data))
	_, _ = h.Write(data)
}

// Hash addresses every file in this snapshot.
func (f *Files) Hash() string { return f.hash }

// Open implements fs.FS with seekable files for http.FileServerFS.
func (f *Files) Open(name string) (fs.File, error) {
	data, ok := f.data[name]
	if !ok || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	return &file{Reader: bytes.NewReader(data), name: name}, nil
}

type file struct {
	*bytes.Reader
	name string
}

func (f *file) Close() error               { return nil }
func (f *file) Stat() (fs.FileInfo, error) { return info{name: f.name, size: f.Size()}, nil }

type info struct {
	name string
	size int64
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return readOnlyMode }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return false }
func (i info) Sys() any           { return nil }
