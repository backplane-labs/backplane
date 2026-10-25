// Package seal encrypts what a service keeps and has to read back — a
// receiver's signing secret, a provider's token — so a copy of its database
// or of a backup holds ciphertext only. It is a configuration section and a
// dependency, like a database connection:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    Seal seal.Config `json:"seal"` // KOMEET_SEAL_KEY, KOMEET_SEAL_PREVIOUS
//	}
//
//	sealer := deps.NewDependency(root, seal.New(cfg.Seal), deps.Name("seal"))
//	sealed, err := sealer.Get().Seal(ctx, []byte(secret), []byte(subscriptionID))
//	secret, err := sealer.Get().Open(ctx, sealed, []byte(subscriptionID))
//
// The associated data (aad) binds a sealed value to what it belongs to: it
// is not stored, Open needs the same, and a value copied to another row
// does not open there.
//
// The keys are AES-256-GCM keys of the section: key seals and opens,
// previous only opens. A sealed value names its key (by a hash of it), so
// rotation is: generate a key (GenerateKey), set it as key and the old one
// as previous, Rewrap what is stored, then drop previous.
package seal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Errors.
var (
	// ErrConfig: the section has no key, or a key is not base64 of 32
	// bytes.
	ErrConfig = errors.New("seal: invalid configuration")
	// ErrOpen: the value is not sealed by these keys, was changed, or the
	// aad differs.
	ErrOpen = errors.New("seal: cannot open")
)

// Sealer seals and opens values. It is immutable and safe for concurrent
// use.
type Sealer interface {
	// Seal encrypts plaintext under the active key, bound to aad.
	Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error)
	// Open decrypts what Seal returned with the same aad, under the active
	// key or a previous one.
	Open(ctx context.Context, sealed, aad []byte) ([]byte, error)
	// Rewrap seals sealed again under the active key; changed is false,
	// and sealed returns as is, when it is under the active key already.
	Rewrap(ctx context.Context, sealed, aad []byte) (out []byte, changed bool, err error)
}

// Config is the section of the keys.
type Config struct {
	// Key seals and opens: base64 of 32 random bytes (GenerateKey).
	Key config.Secret `desc:"Active key: base64 of 32 random bytes; seals and opens." json:"key,omitempty"`
	// Previous keys only open: comma-separated, while values are rewrapped.
	Previous config.Secret `desc:"Previous keys, comma-separated: they only open, during rotation." json:"previous,omitempty"`
}

// Validate checks the keys.
func (c Config) Validate() error {
	_, err := newKeyring(c)

	return err
}

// GenerateKey is a new random key, base64.
func GenerateKey() string {
	key := make([]byte, keySize)
	_, _ = rand.Read(key)

	return base64.StdEncoding.EncodeToString(key)
}

// New is the provider of the section's sealer.
func New(cfg Config) deps.Provider[Sealer] { return provider{cfg: cfg} }

type provider struct{ cfg Config }

func (provider) Name() string { return "seal" }

//nolint:ireturn // the backend is the section's choice
func (p provider) Provide(context.Context, deps.Scope) (Sealer, error) { return Open(p.cfg) }

func (provider) Probe(context.Context, Sealer) error { return nil }

func (provider) Close(context.Context, Sealer) error { return nil }

// Open is the section's sealer, outside of a dependency (tools, tests).
//
//nolint:ireturn // the backend is the section's choice
func Open(cfg Config) (Sealer, error) {
	k, err := newKeyring(cfg)
	if err != nil {
		return nil, err
	}

	return k, nil
}

const (
	keySize = 32
	kidSize = 8
	// version of the keyring's format: version, kid, nonce, ciphertext.
	keyringV1 byte = 1
)

// keyring seals with AES-256-GCM under its active key.
type keyring struct {
	active kid
	keys   map[kid]cipher.AEAD
}

type kid [kidSize]byte

func kidOf(key []byte) kid {
	sum := sha256.Sum256(key)

	return kid(sum[:kidSize])
}

func newKeyring(c Config) (keyring, error) {
	if c.Key.Reveal() == "" {
		return keyring{}, fmt.Errorf("%w: key is required (seal.GenerateKey)", ErrConfig)
	}

	k := keyring{keys: map[kid]cipher.AEAD{}}

	encoded := append([]string{c.Key.Reveal()}, strings.Split(c.Previous.Reveal(), ",")...)
	for i, e := range encoded {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}

		key, err := base64.StdEncoding.DecodeString(e)
		if err != nil || len(key) != keySize {
			return keyring{}, fmt.Errorf("%w: key %d is not base64 of 32 bytes", ErrConfig, i)
		}

		block, _ := aes.NewCipher(key)  // 32 bytes: AES-256
		aead, _ := cipher.NewGCM(block) // standard nonce and tag

		id := kidOf(key)
		if i == 0 {
			k.active = id
		}

		k.keys[id] = aead
	}

	return k, nil
}

// header of a value sealed under id: version and kid, also its aad prefix.
func header(id kid) []byte { return append([]byte{keyringV1}, id[:]...) }

func (k keyring) Seal(_ context.Context, plaintext, aad []byte) ([]byte, error) {
	aead := k.keys[k.active]
	h := header(k.active)

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("seal: nonce: %w", err)
	}

	out := append(h, nonce...) //nolint:gocritic // a new slice from the header
	out = aead.Seal(out, nonce, plaintext, append(h, aad...))

	return out, nil
}

func (k keyring) Open(_ context.Context, sealed, aad []byte) ([]byte, error) {
	id, aead, rest, err := k.split(sealed)
	if err != nil {
		return nil, err
	}

	nonce, ct := rest[:aead.NonceSize()], rest[aead.NonceSize():]

	plaintext, err := aead.Open(nil, nonce, ct, append(header(id), aad...))
	if err != nil {
		return nil, ErrOpen
	}

	return plaintext, nil
}

func (k keyring) Rewrap(ctx context.Context, sealed, aad []byte) ([]byte, bool, error) {
	id, _, _, err := k.split(sealed)
	if err != nil {
		return nil, false, err
	}

	if id == k.active {
		return sealed, false, nil
	}

	plaintext, err := k.Open(ctx, sealed, aad)
	if err != nil {
		return nil, false, err
	}

	out, err := k.Seal(ctx, plaintext, aad)

	return out, err == nil, err
}

// split is the key of sealed and what follows the header.
func (k keyring) split(sealed []byte) (kid, cipher.AEAD, []byte, error) {
	const minimum = 1 + kidSize + 12 + 16 // header, nonce, tag

	if len(sealed) < minimum || sealed[0] != keyringV1 {
		return kid{}, nil, nil, ErrOpen
	}

	id := kid(sealed[1 : 1+kidSize])

	aead, ok := k.keys[id]
	if !ok {
		return kid{}, nil, nil, fmt.Errorf("%w: sealed under a key not configured", ErrOpen)
	}

	return id, aead, sealed[1+kidSize:], nil
}
