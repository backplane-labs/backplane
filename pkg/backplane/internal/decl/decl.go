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

// Schema reflects T under <service>/<name>@1.0.0, or nil when T cannot be
// described: the declaration stays, the payload is plain JSON.
func Schema[T any](service, name string) *sp.Schema {
	s, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0)))
	if err != nil {
		return nil
	}

	return s
}

// Encode is protojson for proto messages, encoding/json otherwise.
func Encode(v any) ([]byte, error) {
	if m, ok := v.(proto.Message); ok {
		return protojson.Marshal(m) //nolint:wrapcheck // one encoder per path
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
