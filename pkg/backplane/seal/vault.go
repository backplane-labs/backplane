package seal

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// ErrVault: Vault answered with an error or could not be reached.
var ErrVault = errors.New("seal: vault")

// Vault is the Transit engine of a HashiCorp Vault (or OpenBao): the key
// never leaves it. Every Seal takes a fresh data key from Transit and
// keeps it wrapped next to the value; Open has Transit unwrap it.
type Vault struct {
	// Addr is Vault's URL; empty: no Vault, the section's keys seal.
	Addr  string        `desc:"Vault URL, e.g. https://vault:8200; empty: the section's keys seal." json:"addr,omitempty"`
	Token config.Secret `desc:"Token with datakey, decrypt and rewrap on the key."                  json:"token,omitempty"`
	// Key is the Transit key's name (type aes256-gcm96).
	Key       string        `desc:"Name of the Transit key."                     json:"key,omitempty"`
	Mount     string        `desc:"Mount path of the Transit engine."            json:"mount"               schemapb:"default=transit"`
	Namespace string        `desc:"Vault Enterprise namespace; empty: the root." json:"namespace,omitempty"`
	TLS       config.TLS    `desc:"TLS of the connection to Vault."              json:"tls"`
	Timeout   time.Duration `desc:"Bound of one call to Vault."                  json:"timeout"             schemapb:"default=10s"`
}

// Enabled reports whether Vault seals.
func (v Vault) Enabled() bool { return v.Addr != "" }

func (v Vault) validate() error {
	switch {
	case v.Token.Reveal() == "" || v.Key == "":
		return fmt.Errorf("%w: vault needs token and key", ErrConfig)
	case strings.ContainsAny(v.Key, "/?#") || strings.ContainsAny(v.Mount, "?#"):
		return fmt.Errorf("%w: vault key or mount is not a path segment", ErrConfig)
	}

	if _, err := url.Parse(v.Addr); err != nil {
		return fmt.Errorf("%w: vault addr: %w", ErrConfig, err)
	}

	if _, err := v.TLS.ClientConfig(); err != nil {
		return fmt.Errorf("%w: vault tls: %w", ErrConfig, err)
	}

	return nil
}

// version of the Vault format: version, length of the wrapped data key
// (uint16), the wrapped key, nonce, ciphertext.
const vaultV1 byte = 2

// transit seals with data keys of Vault Transit; previous opens what the
// section's keys sealed before (moving from keys to Vault).
type transit struct {
	cfg      Vault
	http     *http.Client
	previous *keyring
}

func newTransit(v Vault, previous *keyring) (transit, error) {
	if err := v.validate(); err != nil {
		return transit{}, err
	}

	tlsConfig, _ := v.TLS.ClientConfig() // validate parsed it

	transport, _ := http.DefaultTransport.(*http.Transport)
	transport = transport.Clone()

	if v.TLS.Enabled {
		transport.TLSClientConfig = tlsConfig
	}

	if v.Mount == "" {
		v.Mount = "transit"
	}

	if v.Timeout <= 0 {
		v.Timeout = defaultVaultTimeout
	}

	return transit{cfg: v, http: &http.Client{Transport: transport, Timeout: v.Timeout}, previous: previous}, nil
}

const (
	defaultVaultTimeout = 10 * time.Second
	keyBits             = keySize * 8
	// headerSize: version and the length of the wrapped data key.
	headerSize = 3
)

func (t transit) Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	var key struct {
		Plaintext  string `json:"plaintext"`
		Ciphertext string `json:"ciphertext"`
	}

	if err := t.call(ctx, "datakey/plaintext", map[string]any{"bits": keyBits}, &key); err != nil {
		return nil, err
	}

	dek, err := base64.StdEncoding.DecodeString(key.Plaintext)
	if err != nil || len(dek) != keySize {
		return nil, fmt.Errorf("%w: datakey is not 32 bytes", ErrVault)
	}

	h, err := vaultHeader(key.Ciphertext)
	if err != nil {
		return nil, err
	}

	aead := gcm(dek)
	clear(dek)

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("seal: nonce: %w", err)
	}

	out := append(bytes.Clone(h), nonce...)

	return aead.Seal(out, nonce, plaintext, append(h, aad...)), nil
}

func (t transit) Open(ctx context.Context, sealed, aad []byte) ([]byte, error) {
	if len(sealed) > 0 && sealed[0] == keyringV1 && t.previous != nil {
		return t.previous.Open(ctx, sealed, aad)
	}

	wrapped, rest, err := splitVault(sealed)
	if err != nil {
		return nil, err
	}

	var key struct {
		Plaintext string `json:"plaintext"`
	}

	if err := t.call(ctx, "decrypt", map[string]any{"ciphertext": wrapped}, &key); err != nil {
		return nil, err
	}

	dek, err := base64.StdEncoding.DecodeString(key.Plaintext)
	if err != nil || len(dek) != keySize {
		return nil, ErrOpen
	}

	aead := gcm(dek)
	clear(dek)

	if len(rest) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrOpen
	}

	h := sealed[:len(sealed)-len(rest)]

	plaintext, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], append(bytes.Clone(h), aad...))
	if err != nil {
		return nil, ErrOpen
	}

	return plaintext, nil
}

// Rewrap moves a value of the previous keys to Vault, and one whose data
// key an older version of the Transit key wrapped to the latest (sealed
// again with a fresh data key); a value current already returns as is.
func (t transit) Rewrap(ctx context.Context, sealed, aad []byte) ([]byte, bool, error) {
	plaintext, err := t.Open(ctx, sealed, aad)
	if err != nil {
		return nil, false, err
	}

	if sealed[0] == vaultV1 {
		wrapped, _, _ := splitVault(sealed) // Open split it

		var key struct {
			Ciphertext string `json:"ciphertext"`
		}

		if err := t.call(ctx, "rewrap", map[string]any{"ciphertext": wrapped}, &key); err != nil {
			return nil, false, err
		}

		// Transit wraps anew every time: what counts is the key version.
		if keyVersion(key.Ciphertext) == keyVersion(wrapped) {
			return sealed, false, nil
		}
	}

	out, err := t.Seal(ctx, plaintext, aad)

	return out, err == nil, err
}

// probe asks Vault whether it is unsealed and serving.
func (t transit) probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.cfg.Addr, "/")+"/v1/sys/health",
		http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVault, err)
	}

	res, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVault, err)
	}
	defer res.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxAnswer))

	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusTooManyRequests { // 429: a standby
		return fmt.Errorf("%w: health %s", ErrVault, res.Status)
	}

	return nil
}

const maxAnswer = 1 << 20

// call posts body to the Transit endpoint op of the key and decodes its
// data into out.
func (t transit) call(ctx context.Context, operation string, body map[string]any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVault, err)
	}

	endpoint := strings.TrimRight(t.cfg.Addr, "/") + "/v1/" + strings.Trim(t.cfg.Mount, "/") + "/" + operation + "/" +
		url.PathEscape(t.cfg.Key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVault, err)
	}

	req.Header.Set("X-Vault-Token", t.cfg.Token.Reveal())
	req.Header.Set("Content-Type", "application/json")

	if t.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", t.cfg.Namespace)
	}

	res, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrVault, operation, err)
	}
	defer res.Body.Close()

	answer, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswer))

	if res.StatusCode != http.StatusOK {
		var e struct {
			Errors []string `json:"errors"`
		}

		_ = json.Unmarshal(answer, &e)

		// A ciphertext Transit does not take is a value that does not open.
		if operation == "decrypt" && res.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("%w: %s", ErrOpen, strings.Join(e.Errors, "; "))
		}

		return fmt.Errorf("%w: %s: %s %s", ErrVault, operation, res.Status, strings.Join(e.Errors, "; "))
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}

	if err := json.Unmarshal(answer, &envelope); err != nil || json.Unmarshal(envelope.Data, out) != nil {
		return fmt.Errorf("%w: %s: unreadable answer", ErrVault, operation)
	}

	return nil
}

// vaultHeader is the header of a value whose data key Transit wrapped as
// wrapped: also its aad prefix.
func vaultHeader(wrapped string) ([]byte, error) {
	if wrapped == "" || len(wrapped) > 0xffff {
		return nil, fmt.Errorf("%w: wrapped data key of %d bytes", ErrVault, len(wrapped))
	}

	h := make([]byte, headerSize, headerSize+len(wrapped))
	h[0] = vaultV1
	binary.BigEndian.PutUint16(h[1:], uint16(len(wrapped))) //nolint:gosec // bounded above

	return append(h, wrapped...), nil
}

// splitVault is the wrapped data key of sealed and what follows the
// header.
func splitVault(sealed []byte) (string, []byte, error) {
	if len(sealed) < headerSize || sealed[0] != vaultV1 {
		return "", nil, ErrOpen
	}

	n := int(binary.BigEndian.Uint16(sealed[1:headerSize]))
	if len(sealed) < headerSize+n {
		return "", nil, ErrOpen
	}

	return string(sealed[headerSize : headerSize+n]), sealed[headerSize+n:], nil
}

// keyVersion is the Transit key version of a ciphertext "vault:v<n>:...".
func keyVersion(ciphertext string) string {
	parts := strings.SplitN(ciphertext, ":", 3) //nolint:mnd // vault, version, data

	if len(parts) < 2 { //nolint:mnd // vault, version
		return ""
	}

	return parts[1]
}

func gcm(key []byte) cipher.AEAD {
	block, _ := aes.NewCipher(key)  // 32 bytes: AES-256
	aead, _ := cipher.NewGCM(block) // standard nonce and tag

	return aead
}
