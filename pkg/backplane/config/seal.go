package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrSeal: a seal section has no key, a key is not base64 of 32 bytes, or
// its vault is incomplete.
var ErrSeal = errors.New("seal: invalid configuration")

// SealKeySize is the size of a seal key: AES-256.
const SealKeySize = 32

// Seal is the section of a sealer (pkg/backplane/seal): its AES-256-GCM
// keys, or Vault Transit.
type Seal struct {
	// Key seals and opens: base64 of 32 random bytes (seal.GenerateKey).
	Key Secret `desc:"Active key: base64 of 32 random bytes; seals and opens." json:"key,omitempty"`
	// Previous keys only open: comma-separated, while values are rewrapped.
	Previous Secret `desc:"Previous keys, comma-separated: they only open, during rotation." json:"previous,omitempty"`
	// Vault seals instead of key when its addr is set; key and previous
	// then only open what they sealed before.
	Vault SealVault `desc:"Vault Transit: when set it seals, the keys only open." json:"vault"`
}

// Enabled reports whether the section seals: a key or a vault is set.
func (s Seal) Enabled() bool { return s.Key.Reveal() != "" || s.Vault.Enabled() }

// Keys are the decoded keys, the active one first (none when key is
// empty).
func (s Seal) Keys() ([][]byte, error) {
	if s.Key.Reveal() == "" {
		if strings.TrimSpace(s.Previous.Reveal()) != "" {
			return nil, fmt.Errorf("%w: previous without key", ErrSeal)
		}

		return nil, nil
	}

	var out [][]byte

	for i, e := range append([]string{s.Key.Reveal()}, strings.Split(s.Previous.Reveal(), ",")...) {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}

		key, err := base64.StdEncoding.DecodeString(e)
		if err != nil || len(key) != SealKeySize {
			return nil, fmt.Errorf("%w: key %d is not base64 of 32 bytes", ErrSeal, i)
		}

		out = append(out, key)
	}

	return out, nil
}

// Validate checks what is set: the keys parse, a vault is complete. An
// empty section passes (an optional one is off); a sealer refuses it.
func (s Seal) Validate() error {
	if _, err := s.Keys(); err != nil {
		return err
	}

	if s.Vault.Enabled() {
		return s.Vault.Validate()
	}

	return nil
}

// SealVault is the Transit engine of a HashiCorp Vault (or OpenBao): the
// key never leaves it. Every Seal takes a fresh data key from Transit and
// keeps it wrapped next to the value; Open has Transit unwrap it.
//
// Give the sealer a Transit key of its own and grant no one encrypt on it:
// whoever can encrypt with the key can wrap a data key they chose and
// forge a value Open accepts. The token needs datakey, decrypt and rewrap.
type SealVault struct {
	// Addr is Vault's URL; empty: no Vault, the section's keys seal.
	Addr  string `desc:"Vault URL, e.g. https://vault:8200; empty: the section's keys seal." json:"addr,omitempty"`
	Token Secret `desc:"Token with datakey, decrypt and rewrap on the key."                  json:"token,omitempty"`
	// Key is the Transit key's name (type aes256-gcm96).
	Key       string        `desc:"Name of the Transit key."                     json:"key,omitempty"`
	Mount     string        `desc:"Mount path of the Transit engine."            json:"mount"               schemapb:"default=transit"`
	Namespace string        `desc:"Vault Enterprise namespace; empty: the root." json:"namespace,omitempty"`
	TLS       TLS           `desc:"TLS of the connection to Vault."              json:"tls"`
	Timeout   time.Duration `desc:"Bound of one call to Vault."                  json:"timeout"             schemapb:"default=10s"`
}

// Enabled reports whether Vault seals.
func (v SealVault) Enabled() bool { return v.Addr != "" }

// Validate checks a vault that is set; an empty one passes.
func (v SealVault) Validate() error {
	switch {
	case !v.Enabled():
		return nil
	case v.Token.Reveal() == "" || v.Key == "":
		return fmt.Errorf("%w: vault needs token and key", ErrSeal)
	case strings.ContainsAny(v.Key, "/?#") || strings.ContainsAny(v.Mount, "?#"):
		return fmt.Errorf("%w: vault key or mount is not a path segment", ErrSeal)
	}

	if _, err := url.Parse(v.Addr); err != nil {
		return fmt.Errorf("%w: vault addr: %w", ErrSeal, err)
	}

	if _, err := v.TLS.ClientConfig(); err != nil {
		return fmt.Errorf("%w: vault tls: %w", ErrSeal, err)
	}

	return nil
}
