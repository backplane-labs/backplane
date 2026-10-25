package seal_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

func open(t *testing.T, cfg seal.Config) seal.Sealer { //nolint:ireturn // the package's interface
	t.Helper()

	s, err := seal.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestSealOpen(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s := open(t, seal.Config{Key: config.Secret(seal.GenerateKey())})

	sealed, err := s.Seal(ctx, []byte("whsec_x"), []byte("sub-1"))
	if err != nil || bytes.Contains(sealed, []byte("whsec_x")) {
		t.Fatalf("seal: %x %v", sealed, err)
	}

	again, _ := s.Seal(ctx, []byte("whsec_x"), []byte("sub-1"))
	if bytes.Equal(sealed, again) {
		t.Fatal("two seals are equal: nonce reused")
	}

	if got, err := s.Open(ctx, sealed, []byte("sub-1")); err != nil || string(got) != "whsec_x" {
		t.Fatalf("open: %q %v", got, err)
	}

	if _, err := s.Open(ctx, sealed, []byte("sub-2")); !errors.Is(err, seal.ErrOpen) {
		t.Fatalf("other aad: %v", err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1

	if _, err := s.Open(ctx, tampered, []byte("sub-1")); !errors.Is(err, seal.ErrOpen) {
		t.Fatalf("tampered: %v", err)
	}

	other := open(t, seal.Config{Key: config.Secret(seal.GenerateKey())})
	if _, err := other.Open(ctx, sealed, []byte("sub-1")); !errors.Is(err, seal.ErrOpen) {
		t.Fatalf("another key: %v", err)
	}
}

// Rotation: the new key seals, the previous one still opens, Rewrap moves
// values to the new key, and without previous only rewrapped ones open.
func TestRotation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	oldKey, newKey := seal.GenerateKey(), seal.GenerateKey()

	before := open(t, seal.Config{Key: config.Secret(oldKey)})
	sealed, _ := before.Seal(ctx, []byte("v"), nil)

	rotating := open(t, seal.Config{Key: config.Secret(newKey), Previous: config.Secret(" ," + oldKey)})
	if got, err := rotating.Open(ctx, sealed, nil); err != nil || string(got) != "v" {
		t.Fatalf("previous key: %q %v", got, err)
	}

	rewrapped, changed, err := rotating.Rewrap(ctx, sealed, nil)
	if err != nil || !changed {
		t.Fatalf("rewrap: %v %v", changed, err)
	}

	if again, moved, err := rotating.Rewrap(ctx, rewrapped, nil); err != nil || moved || !bytes.Equal(again, rewrapped) {
		t.Fatalf("rewrap again: %v %v", moved, err)
	}

	after := open(t, seal.Config{Key: config.Secret(newKey)})
	if got, err := after.Open(ctx, rewrapped, nil); err != nil || string(got) != "v" {
		t.Fatalf("rewrapped: %q %v", got, err)
	}

	if _, err := after.Open(ctx, sealed, nil); !errors.Is(err, seal.ErrOpen) {
		t.Fatalf("old value after rotation: %v", err)
	}
}

func TestConfig(t *testing.T) {
	t.Parallel()

	if _, err := seal.Open(seal.Config{}); !errors.Is(err, seal.ErrConfig) {
		t.Errorf("no key: %v", err)
	}

	for name, cfg := range map[string]seal.Config{
		"not base64":   {Key: "!!"},
		"short key":    {Key: "c2hvcnQ="},
		"bad previous": {Key: config.Secret(seal.GenerateKey()), Previous: "c2hvcnQ="},
	} {
		if err := cfg.Validate(); !errors.Is(err, seal.ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
