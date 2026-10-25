package seal_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

// vault is a dev Vault (BACKPLANE_TEST_VAULT=addr, BACKPLANE_TEST_VAULT_TOKEN)
// with transit enabled, and a new key of its own.
func vault(t *testing.T) seal.Vault {
	t.Helper()

	addr, token := os.Getenv("BACKPLANE_TEST_VAULT"), os.Getenv("BACKPLANE_TEST_VAULT_TOKEN")
	if addr == "" || token == "" {
		t.Skip("BACKPLANE_TEST_VAULT and BACKPLANE_TEST_VAULT_TOKEN not set")
	}

	var b [4]byte

	_, _ = rand.Read(b[:])
	v := seal.Vault{Addr: addr, Token: config.Secret(token), Key: "test-" + hex.EncodeToString(b[:]), Mount: "transit"}

	vaultPost(t, v, "/v1/transit/keys/"+v.Key)

	return v
}

func vaultPost(t *testing.T, v seal.Vault, path string) {
	t.Helper()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, v.Addr+path, strings.NewReader("{}"))
	req.Header.Set("X-Vault-Token", v.Token.Reveal())

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		t.Fatalf("vault %s: %s", path, res.Status)
	}
}

func TestVault(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := vault(t)
	oldKey := seal.GenerateKey()

	before := open(t, seal.Config{Key: config.Secret(oldKey)})
	legacy, _ := before.Seal(ctx, []byte("old"), []byte("row-1"))

	s := open(t, seal.Config{Key: config.Secret(oldKey), Vault: v})

	sealed, err := s.Seal(ctx, []byte("whsec_x"), []byte("row-1"))
	if err != nil || bytes.Contains(sealed, []byte("whsec_x")) {
		t.Fatalf("seal: %v", err)
	}

	if got, err := s.Open(ctx, sealed, []byte("row-1")); err != nil || string(got) != "whsec_x" {
		t.Fatalf("open: %q %v", got, err)
	}

	if _, err := s.Open(ctx, sealed, []byte("row-2")); !errors.Is(err, seal.ErrOpen) {
		t.Fatalf("other aad: %v", err)
	}

	// From the section's key to Vault.
	if got, err := s.Open(ctx, legacy, []byte("row-1")); err != nil || string(got) != "old" {
		t.Fatalf("legacy: %q %v", got, err)
	}

	moved, changed, err := s.Rewrap(ctx, legacy, []byte("row-1"))
	if err != nil || !changed {
		t.Fatalf("rewrap legacy: %v %v", changed, err)
	}

	vaultOnly := open(t, seal.Config{Vault: v})
	if got, err := vaultOnly.Open(ctx, moved, []byte("row-1")); err != nil || string(got) != "old" {
		t.Fatalf("moved: %q %v", got, err)
	}

	// A Transit key rotation: values move to the latest version.
	if _, current, err := vaultOnly.Rewrap(ctx, sealed, []byte("row-1")); err != nil || current {
		t.Fatalf("rewrap current: %v %v", current, err)
	}

	vaultPost(t, v, "/v1/transit/keys/"+v.Key+"/rotate")

	rotated, changed, err := vaultOnly.Rewrap(ctx, sealed, []byte("row-1"))
	if err != nil || !changed {
		t.Fatalf("rewrap after rotation: %v %v", changed, err)
	}

	if got, err := vaultOnly.Open(ctx, rotated, []byte("row-1")); err != nil || string(got) != "whsec_x" {
		t.Fatalf("rotated: %q %v", got, err)
	}

	deps := seal.New(seal.Config{Vault: v})
	if err := deps.Probe(ctx, vaultOnly); err != nil {
		t.Fatalf("probe: %v", err)
	}

	denied := v
	denied.Token = "nope"

	if _, err := open(t, seal.Config{Vault: denied}).Seal(ctx, []byte("x"), nil); !errors.Is(err, seal.ErrVault) {
		t.Fatalf("bad token: %v", err)
	}
}
