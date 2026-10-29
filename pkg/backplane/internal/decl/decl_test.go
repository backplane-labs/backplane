package decl_test

import (
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

// A newer producer's payload: fields the reader's type lacks.
const newer = `{"path":"db","kind":"NODE_KIND_SINGLETON","optional":true,"addedLater":{"x":[1,2]},"alsoNew":"y"}`

func TestDecodeIgnoresUnknownFields(t *testing.T) {
	t.Parallel()

	var m backplanev1.NodeStatus
	if err := decl.Decode([]byte(newer), &m); err != nil {
		t.Fatal(err)
	}

	if m.GetPath() != "db" || m.GetKind() != backplanev1.NodeKind_NODE_KIND_SINGLETON || !m.GetOptional() {
		t.Fatalf("message %v", &m)
	}

	// A Ref[*pb.Node] decodes into a nil pointer: allocated, protojson.
	var p *backplanev1.NodeStatus
	if err := decl.Decode([]byte(newer), &p); err != nil {
		t.Fatal(err)
	}

	if p.GetKind() != backplanev1.NodeKind_NODE_KIND_SINGLETON {
		t.Fatalf("pointer %v", p)
	}

	var s struct {
		Path string `json:"path"`
	}
	if err := decl.Decode([]byte(newer), &s); err != nil || s.Path != "db" {
		t.Fatalf("struct %+v %v", s, err)
	}
}

func TestEncodeDecodeProtoRoundTrip(t *testing.T) {
	t.Parallel()

	in := &backplanev1.NodeStatus{Path: "a/b", Kind: backplanev1.NodeKind_NODE_KIND_DEPENDENCY}

	raw, err := decl.Encode(in)
	if err != nil {
		t.Fatal(err)
	}

	var out *backplanev1.NodeStatus
	if err := decl.Decode(raw, &out); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}

	if out.GetPath() != "a/b" || out.GetKind() != backplanev1.NodeKind_NODE_KIND_DEPENDENCY {
		t.Fatalf("round trip %v", out)
	}
}
