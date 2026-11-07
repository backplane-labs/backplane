package apifiles_test

import (
	"bytes"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/apifiles"
)

func TestSnapshotImmutable(t *testing.T) {
	t.Parallel()

	source := fstest.MapFS{"api.yaml": {Data: []byte("root")}, "schemas/model.yaml": {Data: []byte("model")}}

	files, err := apifiles.Snapshot(source, "api.yaml")
	if err != nil {
		t.Fatal(err)
	}

	source["schemas/model.yaml"].Data = []byte("changed")

	data, err := fs.ReadFile(files, "schemas/model.yaml")
	if err != nil || string(data) != "model" {
		t.Fatalf("snapshot changed: %q %v", data, err)
	}

	updated, err := apifiles.Snapshot(source, "api.yaml")
	if err != nil || updated.Hash() == files.Hash() {
		t.Fatalf("hash did not change: %v", err)
	}

	for _, name := range []string{".", "../api.yaml", "missing.yaml", "schemas"} {
		if _, err := files.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("unsafe/missing path %q: %v", name, err)
		}
	}
}

func TestSnapshotBounds(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"", ".", "../api.yaml", "missing.yaml"} {
		if _, err := apifiles.Snapshot(fstest.MapFS{"api.yaml": {Data: []byte("root")}}, entry); !errors.Is(err, apifiles.ErrBundle) {
			t.Fatalf("entry %q: %v", entry, err)
		}
	}

	if _, err := apifiles.Snapshot(fstest.MapFS{"api.yaml": {Data: bytes.Repeat([]byte("x"), apifiles.MaxBytes+1)}}, "api.yaml"); !errors.Is(err, apifiles.ErrBundle) {
		t.Fatalf("oversized: %v", err)
	}

	if _, err := apifiles.Snapshot(fstest.MapFS{"api.yaml": {Mode: fs.ModeSymlink, Data: []byte("elsewhere")}}, "api.yaml"); !errors.Is(err, apifiles.ErrBundle) {
		t.Fatalf("symlink: %v", err)
	}
}

// One file is addressed as a snapshot of it is; bad names and oversize
// data are refused.
func TestOf(t *testing.T) {
	t.Parallel()

	data := []byte("set")

	one, err := apifiles.Of("descriptors.binpb", data)
	if err != nil {
		t.Fatal(err)
	}

	snap, err := apifiles.Snapshot(fstest.MapFS{"descriptors.binpb": {Data: data}}, "descriptors.binpb")
	if err != nil || one.Hash() != snap.Hash() {
		t.Fatalf("hash %s, snapshot %s %v", one.Hash(), snap.Hash(), err)
	}

	if got, err := fs.ReadFile(one, "descriptors.binpb"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %q %v", got, err)
	}

	for name, c := range map[string]struct {
		file string
		data []byte
	}{
		"absolute": {"/x", data},
		"dot":      {".", data},
		"too big":  {"x", make([]byte, apifiles.MaxBytes+1)},
	} {
		if _, err := apifiles.Of(c.file, c.data); !errors.Is(err, apifiles.ErrBundle) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
