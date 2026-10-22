package decl_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
)

// payloads is a .proto of hook and activity payloads with what protojson
// and Go reflection disagree on: snake_case names, a Timestamp, a uint64,
// an enum, a oneof and a nested message.
//
//nolint:ireturn // protodesc's
func payloads(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()

	field := func(name string, n int32, typ descriptorpb.FieldDescriptorProto_Type, typeName string,
		oneof *int32,
	) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), Type: typ.Enum(),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), OneofIndex: oneof,
		}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}

		return f
	}

	const (
		str = descriptorpb.FieldDescriptorProto_TYPE_STRING
		u64 = descriptorpb.FieldDescriptorProto_TYPE_UINT64
		msg = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		enm = descriptorpb.FieldDescriptorProto_TYPE_ENUM
	)

	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("decltest/v1/payloads.proto"), Package: proto.String("decltest.v1"),
		Syntax: proto.String("proto3"), Dependency: []string{"google/protobuf/timestamp.proto"},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("State"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("STATE_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("STATE_ACTIVE"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Device"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("device_id", 1, str, "", nil),
			},
		}, {
			Name: proto.String("Joined"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("identity_id", 1, str, "", nil),
				field("created_at", 2, msg, ".google.protobuf.Timestamp", nil),
				field("seq_no", 3, u64, "", nil),
				field("state", 4, enm, ".decltest.v1.State", nil),
				field("email", 5, str, "", proto.Int32(0)),
				field("phone", 6, str, "", proto.Int32(0)),
				field("device", 7, msg, ".decltest.v1.Device", nil),
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("contact")}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}

	return fd
}

// A binding's analysis and its run agree on a proto payload: the schema
// names what Encode writes, and what a binding computes from it decodes.
func TestProtoPayloadBinding(t *testing.T) {
	t.Parallel()

	_ = timestamppb.Now() // the descriptor imports timestamp.proto
	joined := payloads(t).Messages().ByName("Joined")

	schema := func(name string) *sp.Schema {
		return decl.ProtoSchema(dynamicpb.NewMessage(joined), sp.ID(sp.Namespace("komeet"), sp.SchemaName(name), sp.Ver(1, 0, 0)))
	}

	if err := schema("in").CheckDescriptor(); err != nil {
		t.Fatal(err)
	}

	cat := bindings.Manifests{{
		Service: "komeet", Version: "1.0.0",
		Hooks: []*backplanev1.Hook{{Name: "Joined", Input: schema("hook_in"), Output: schema("hook_out")}},
	}, {
		Service: "audit", Version: "1.0.0",
		Activities: []*backplanev1.Activity{{
			Name: "Record", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY,
			Input: schema("record_in"), Output: schema("record_out"),
		}},
	}}

	b, err := bindings.BindingYAML(`
hook: komeet.Joined
steps:
  record:
    activity: audit.Record
    when: req.state == "STATE_ACTIVE" && req.seq_no > 1u && has(req.email)
    input:
      identity_id: req.identity_id
      created_at: req.created_at
      seq_no: req.seq_no + 1u
      state: req.state
      email: req.email
      device: {device_id: req.device.device_id}
result:
  identity_id: record.identity_id
  created_at: record.created_at
  seq_no: record.seq_no
  state: record.state
  phone: '"+1"'
`)
	if err != nil {
		t.Fatal(err)
	}

	p, err := bindings.CompileBinding(b, cat)
	if err != nil {
		t.Fatalf("analysis: %v", err)
	}

	camel, err := bindings.BindingYAML(`
hook: komeet.Joined
result: {identity_id: req.identityId}
`)
	if err != nil {
		t.Fatal(err)
	}

	if v, err := bindings.ValidateBinding(camel, cat); err != nil || len(v) == 0 {
		t.Fatalf("camelCase passed analysis: %v %v", v, err)
	}

	joinedAt := time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC)
	in := dynamicpb.NewMessage(joined)
	set := func(m *dynamicpb.Message, name string, v protoreflect.Value) {
		m.Set(m.Descriptor().Fields().ByName(protoreflect.Name(name)), v)
	}

	set(in, "identity_id", protoreflect.ValueOfString("id-1"))
	set(in, "created_at", protoreflect.ValueOfMessage(timestamppb.New(joinedAt).ProtoReflect()))
	set(in, "seq_no", protoreflect.ValueOfUint64(1<<60+1))
	set(in, "state", protoreflect.ValueOfEnum(1))
	set(in, "email", protoreflect.ValueOfString("a@b.c"))

	device := dynamicpb.NewMessage(joined.Fields().ByName("device").Message())
	set(device, "device_id", protoreflect.ValueOfString("d-1"))
	set(in, "device", protoreflect.ValueOfMessage(device))

	raw, err := decl.Encode(in)
	if err != nil {
		t.Fatal(err)
	}

	s, err := p.Start(raw)
	if err != nil {
		t.Fatalf("start %s: %v", raw, err)
	}

	if run, err := p.When("record", s); err != nil || !run {
		t.Fatalf("when on %s: %v %v", raw, run, err)
	}

	input, err := p.Input("record", s)
	if err != nil {
		t.Fatal(err)
	}

	got := dynamicpb.NewMessage(joined)
	if err := decl.Decode(input, got); err != nil {
		t.Fatalf("activity input %s: %v", input, err)
	}

	field := func(m *dynamicpb.Message, name string) protoreflect.Value {
		return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name)))
	}
	timeOf := func(m *dynamicpb.Message) time.Time {
		ts := field(m, "created_at").Message()

		return time.Unix(ts.Get(ts.Descriptor().Fields().ByName("seconds")).Int(),
			ts.Get(ts.Descriptor().Fields().ByName("nanos")).Int())
	}

	if field(got, "identity_id").String() != "id-1" || field(got, "seq_no").Uint() != 1<<60+2 ||
		field(got, "state").Enum() != 1 || field(got, "email").String() != "a@b.c" ||
		!timeOf(got).Equal(joinedAt) ||
		field(got, "device").Message().Get(device.Descriptor().Fields().ByName("device_id")).String() != "d-1" {
		t.Fatalf("activity input %s", input)
	}

	out, err := decl.Encode(got)
	if err != nil {
		t.Fatal(err)
	}

	if s, err = p.Bind(s, "record", out); err != nil {
		t.Fatal(err)
	}

	result, err := p.Result(s)
	if err != nil {
		t.Fatal(err)
	}

	res := dynamicpb.NewMessage(joined)
	if err := decl.Decode(result, res); err != nil {
		t.Fatalf("result %s: %v", result, err)
	}

	if field(res, "seq_no").Uint() != 1<<60+2 || field(res, "phone").String() != "+1" ||
		!timeOf(res).Equal(joinedAt) {
		t.Fatalf("result %s", result)
	}
}

// Schema of a generated message is its descriptor's: proto names, enums
// by name.
func TestSchemaOfGeneratedMessage(t *testing.T) {
	t.Parallel()

	s := decl.Schema[*backplanev1.Node](nil, "svc", "node")
	if s == nil {
		t.Fatal("no schema")
	}

	names := map[string]*sp.Schema_Field{}
	for _, f := range s.GetFields() {
		names[f.GetName()] = f
	}

	if names["path"] == nil || names["kind"].GetChoice() == nil ||
		names["kind"].GetChoice().GetOptions()[3].GetValue().GetStringValue() != "NODE_KIND_SINGLETON" {
		t.Fatalf("schema %v", s)
	}
}
