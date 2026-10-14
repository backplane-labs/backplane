package redact_test

import (
	"bytes"
	"testing"

	sp "github.com/gopherex/schemapb/go/schemapb"

	"github.com/backplane-labs/backplane/internal/redact"
)

// Payloads as an author declares them: the SDK reflects these types into
// the schemas of the manifests.
type (
	otp struct {
		To    string            `json:"to"`
		Code  string            `json:"code"            schemapb:"secret=true"`
		User  user              `json:"user"`
		Codes []string          `json:"codes,omitempty" schemapb:"secret=true"`
		Links []link            `json:"links,omitempty"`
		Extra map[string]link   `json:"extra,omitempty"`
		Data  map[string]string `json:"data,omitempty"`
	}
	user struct {
		Name  string `json:"name"`
		Token string `json:"token" schemapb:"secret=true"`
	}
	link struct {
		URL   string `json:"url"`
		Nonce string `json:"nonce" schemapb:"secret=true"`
	}
	plain struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
)

func schemaOf[T any](t *testing.T) *sp.Schema {
	t.Helper()

	s, err := sp.ReflectType[T](sp.ID(sp.Namespace("test"), sp.SchemaName("payload"), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}

	return s
}

// Secret fields are hidden through objects, lists and maps; the rest, and
// fields the schema does not declare, stay; keys come out sorted.
func TestMask(t *testing.T) {
	t.Parallel()

	in := `{"to":"a@b","code":"1234","user":{"token":"t","name":"Ann"},"codes":["1","2"],` +
		`"links":[{"url":"u","nonce":"n"}],"extra":{"k":{"url":"u2","nonce":"n2"}},"data":{"x":"y"},"unknown":"kept"}`
	want := `{"code":"***","codes":"***","data":{"x":"y"},"extra":{"k":{"nonce":"***","url":"u2"}},` +
		`"links":[{"nonce":"***","url":"u"}],"to":"a@b","unknown":"kept","user":{"name":"Ann","token":"***"}}`

	if got := string(redact.Mask(schemaOf[otp](t), []byte(in))); got != want {
		t.Fatalf("masked\n got %s\nwant %s", got, want)
	}
}

// Without a secret in the schema, or without a schema, data returns as
// it was: byte for byte, not re-encoded.
func TestMaskUntouched(t *testing.T) {
	t.Parallel()

	in := []byte(`{"to":  "a@b", "text": "hi"}`)

	if got := redact.Mask(schemaOf[plain](t), in); !bytes.Equal(got, in) {
		t.Fatalf("plain schema: %s", got)
	}

	if got := redact.Mask(nil, in); !bytes.Equal(got, in) {
		t.Fatalf("nil schema: %s", got)
	}

	for _, raw := range []string{"", "not json", `"text"`, `[1,2]`, `null`} {
		if got := redact.Mask(schemaOf[otp](t), []byte(raw)); string(got) != raw {
			t.Errorf("%q: %s", raw, got)
		}
	}
}

// Numbers keep their exact text: an int64 above 2^53 is not rounded by
// the re-encoding.
func TestMaskExactNumbers(t *testing.T) {
	t.Parallel()

	in := `{"code":"x","to":"a","n":9007199254740993,"f":1.50}`

	got := string(redact.Mask(schemaOf[otp](t), []byte(in)))
	if got != `{"code":"***","f":1.50,"n":9007199254740993,"to":"a"}` {
		t.Fatalf("numbers: %s", got)
	}
}

func TestAll(t *testing.T) {
	t.Parallel()

	if got := redact.All([]byte(`{"a":1}`)); string(got) != `"***"` {
		t.Fatalf("all: %s", got)
	}

	if got := redact.All(nil); got != nil {
		t.Fatalf("empty: %s", got)
	}

	if got := redact.MaskString(schemaOf[otp](t), `{"code":"1"}`); got != `{"code":"***"}` {
		t.Fatalf("string: %s", got)
	}

	if got := redact.MaskString(schemaOf[otp](t), ""); got != "" {
		t.Fatalf("empty string: %q", got)
	}
}
