package decl

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	sp "github.com/gopherex/schemapb/go/schemapb"
)

// schemaPB is a message protoc-gen-go-schemapb generated a schema for.
type schemaPB interface{ SchemaPB() *sp.Schema }

// protoSchema is the schema of a proto message as Encode writes it: the
// generated SchemaPB when the message has one, otherwise the descriptor
// read with protojson's rules — proto field names, enums by name, 64-bit
// integers as strings, the well-known types in their JSON form, a oneof's
// members as fields of their own. Nested messages are defs.
func protoSchema(m proto.Message, id *sp.SchemaIdentity) *sp.Schema {
	if g, ok := m.(schemaPB); ok {
		if s := g.SchemaPB(); s != nil {
			s = proto.CloneOf(s)
			s.Id = id

			return s
		}
	}

	b := protoBuilder{defs: map[string]*sp.Schema{}}
	s := b.message(m.ProtoReflect().Descriptor())
	s.Id = id

	if len(b.defs) > 0 {
		s.Defs = b.defs
	}

	return s
}

type protoBuilder struct {
	defs map[string]*sp.Schema
}

func (b *protoBuilder) message(md protoreflect.MessageDescriptor) *sp.Schema {
	s := &sp.Schema{}

	fields := md.Fields()
	for i := range fields.Len() {
		s.Fields = append(s.Fields, b.field(fields.Get(i)))
	}

	return s
}

// field is fd under its proto name.
func (b *protoBuilder) field(fd protoreflect.FieldDescriptor) *sp.Schema_Field {
	name := string(fd.Name())

	switch {
	case fd.IsMap():
		value := b.single(fd.MapValue(), "value")

		return &sp.Schema_Field{Name: name, Kind: &sp.Schema_Field_Map_{
			Map: &sp.Schema_Field_Map{ValueField: value},
		}}
	case fd.IsList():
		return &sp.Schema_Field{Name: name, Kind: &sp.Schema_Field_List_{
			List: &sp.Schema_Field_List{Items: []*sp.Schema_Field{b.single(fd, "item")}},
		}}
	default:
		return b.single(fd, name)
	}
}

// single is one value of fd: a scalar, an enum or a message.
func (b *protoBuilder) single(fd protoreflect.FieldDescriptor, name string) *sp.Schema_Field {
	f := &sp.Schema_Field{Name: name}

	switch fd.Kind() {
	case protoreflect.EnumKind:
		f.Kind = enumKind(fd.Enum())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		b.messageKind(fd.Message(), f)
	default:
		scalar(f, fd.Kind())
	}

	return f
}

func enumKind(enum protoreflect.EnumDescriptor) *sp.Schema_Field_Choice_ {
	if enum.FullName() == "google.protobuf.NullValue" {
		return &sp.Schema_Field_Choice_{Choice: &sp.Schema_Field_Choice{
			Options: []*sp.Schema_Field_Choice_Option{{Value: sp.StrV("NULL_VALUE")}},
		}}
	}

	values := enum.Values()
	c := &sp.Schema_Field_Choice{Options: make([]*sp.Schema_Field_Choice_Option, 0, values.Len())}

	for i := range values.Len() {
		c.Options = append(c.Options, &sp.Schema_Field_Choice_Option{Value: sp.StrV(string(values.Get(i).Name()))})
	}

	return &sp.Schema_Field_Choice_{Choice: c}
}

// scalar sets f's kind to k's.
func scalar(f *sp.Schema_Field, k protoreflect.Kind) {
	switch k {
	case protoreflect.BoolKind:
		f.Kind = &sp.Schema_Field_Bool_{Bool: &sp.Schema_Field_Bool{}}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		f.Kind = &sp.Schema_Field_Int32_{Int32: &sp.Schema_Field_Int32{}}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		f.Kind = &sp.Schema_Field_Uint32{Uint32: &sp.Schema_Field_UInt32{}}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		f.Kind = &sp.Schema_Field_Int64_{Int64: &sp.Schema_Field_Int64{}}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		f.Kind = &sp.Schema_Field_Uint64{Uint64: &sp.Schema_Field_UInt64{}}
	case protoreflect.FloatKind:
		f.Kind = &sp.Schema_Field_Float_{Float: &sp.Schema_Field_Float{}}
	case protoreflect.DoubleKind:
		f.Kind = &sp.Schema_Field_Double_{Double: &sp.Schema_Field_Double{}}
	case protoreflect.BytesKind:
		f.Kind = &sp.Schema_Field_Bytes_{Bytes: &sp.Schema_Field_Bytes{}}
	default:
		f.Kind = &sp.Schema_Field_String_{String_: &sp.Schema_Field_String{}}
	}
}

// messageKind: a well-known type in its JSON form, any other message a ref
// to its def. A message field may be null.
func (b *protoBuilder) messageKind(desc protoreflect.MessageDescriptor, f *sp.Schema_Field) {
	f.Nullable = true

	switch name := desc.FullName(); name {
	case "google.protobuf.Timestamp":
		f.Kind = &sp.Schema_Field_Timestamp_{Timestamp: &sp.Schema_Field_Timestamp{}}
	case "google.protobuf.Duration":
		f.Kind = &sp.Schema_Field_Duration_{Duration: &sp.Schema_Field_Duration{}}
	case "google.protobuf.FieldMask":
		f.Kind = &sp.Schema_Field_String_{String_: &sp.Schema_Field_String{}}
	case "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.ListValue",
		"google.protobuf.Any", "google.protobuf.Empty":
		f.Kind = &sp.Schema_Field_Json_{Json: &sp.Schema_Field_Json{}}
	case "google.protobuf.DoubleValue", "google.protobuf.FloatValue", "google.protobuf.Int64Value",
		"google.protobuf.UInt64Value", "google.protobuf.Int32Value", "google.protobuf.UInt32Value",
		"google.protobuf.BoolValue", "google.protobuf.StringValue", "google.protobuf.BytesValue":
		scalar(f, desc.Fields().ByName("value").Kind())
	default:
		key := string(name)
		if _, ok := b.defs[key]; !ok {
			b.defs[key] = &sp.Schema{} // registered first: a recursive message finds it
			b.defs[key] = b.message(desc)
		}

		f.Kind = &sp.Schema_Field_Ref_{Ref: &sp.Schema_Field_Ref{Target: &sp.Schema_Field_Ref_Name{Name: key}}}
	}
}
