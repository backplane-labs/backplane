// Package decl is shared by the hook, activity and event packages: it
// resolves the service a Scope belongs to, derives payload schemas and
// encodes payloads.
package decl

import (
	"encoding/json"
	"fmt"
	"reflect"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// Env returns the service env and node of scope; a zero scope panics.
func Env(scope any, what string) (*env.Env, *node.Node) {
	n := link.NodeOf(scope)
	if n == nil {
		panic(fmt.Sprintf("backplane: %s declared on a zero scope: use the Root or a component", what))
	}

	e := env.Of(n)
	if e == nil {
		panic(fmt.Sprintf("backplane: %s declared on a scope without a service", what))
	}

	return e, n
}

// Schema is T's schema under <service>/<name>@1.0.0, as Encode writes T:
// a proto message's from its descriptor (protoSchema), any other type
// reflected by schemapb. A type that cannot be described (an unsupported
// field, a malformed schemapb tag) leaves the payload plain JSON: the
// console shows no form for it, so the author is told on scope's log.
func Schema[T any](scope deps.Scope, service, name string) *sp.Schema {
	id := sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0))

	var (
		s   *sp.Schema
		err error
	)

	if m, ok := any(*new(T)).(proto.Message); ok {
		s = protoSchema(m, id)
		err = s.CheckDescriptor()
	} else {
		s, err = sp.ReflectType[T](id)
	}

	if err != nil {
		if scope != nil {
			scope.Log().Warn("payload schema not reflected: the console gets plain JSON for it",
				xlog.String("schema", name), xlog.Err(err))
		}

		return nil
	}

	return s
}

// Encode is protojson for proto messages, encoding/json otherwise. Proto
// fields keep their proto names, the names a binding reads in the .proto,
// and are written when unset too (a oneof's unset members aside), as a Go
// struct's are: a binding reads a zero count as 0, not as a missing key.
func Encode(v any) ([]byte, error) {
	if m, ok := v.(proto.Message); ok {
		return marshal.Marshal(m) //nolint:wrapcheck // one encoder per path
	}

	return json.Marshal(v) //nolint:wrapcheck // one encoder per path
}

// Decode is the inverse of Encode. Fields the payload has and v does not
// are ignored on both paths: a producer may add fields before its
// consumers know them (payloads evolve additively; a renamed field is a
// new field). v may point to a nil proto message pointer (a Ref[*pb.X]):
// the message is allocated.
func Decode(data []byte, v any) error {
	if m, ok := message(v); ok {
		return unmarshal.Unmarshal(data, m) //nolint:wrapcheck // one decoder per path
	}

	return json.Unmarshal(data, v) //nolint:wrapcheck // one decoder per path
}

//nolint:gochecknoglobals // immutable options
var (
	marshal      = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	unmarshal    = protojson.UnmarshalOptions{DiscardUnknown: true}
	protoMessage = reflect.TypeFor[proto.Message]()
)

// message is v as a proto message: v itself, or the message a **M points
// to, allocated when nil.
func message(v any) (proto.Message, bool) {
	if m, ok := v.(proto.Message); ok {
		return m, true
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, false
	}

	el := rv.Elem()
	if el.Kind() != reflect.Pointer || !el.Type().Implements(protoMessage) {
		return nil, false
	}

	if el.IsNil() {
		el.Set(reflect.New(el.Type().Elem()))
	}

	m, ok := el.Interface().(proto.Message)

	return m, ok
}
