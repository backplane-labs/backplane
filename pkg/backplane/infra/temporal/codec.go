package temporal

import (
	"context"
	"errors"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

// EncodingSealed marks a payload the codec sealed: its data is the
// original payload, sealed.
const EncodingSealed = "binary/backplane-sealed"

// payloadAAD binds sealed payloads to this use of the keys.
var payloadAAD = []byte("backplane/temporal-payload") //nolint:gochecknoglobals // constant

// Codec seals every payload whole, metadata included, and opens what it
// sealed; a payload it did not seal passes as it is, so history written
// before encryption was on stays readable. Plain is the codec that changes
// nothing.
type Codec struct {
	s seal.Sealer
	// strict refuses payloads it did not seal (Decode, the SDK's path).
	strict bool
}

// NewCodec is the codec of an encryption section (config.Temporal).
func NewCodec(cfg config.Seal) (Codec, error) {
	s, err := seal.Open(cfg)
	if err != nil {
		return Codec{}, fmt.Errorf("temporal: encryption: %w", err)
	}

	return Codec{s: s}, nil
}

// Strict is the codec refusing, in Decode, payloads it did not seal: once
// no run from before encryption is left, nothing unsealed is taken. The
// console's DecodeMessage stays lenient: Temporal keeps memos and search
// attributes unsealed.
func (c Codec) Strict(on bool) Codec {
	c.strict = on && c.s != nil

	return c
}

// ErrUnsealed: a strict codec got a payload it did not seal.
var ErrUnsealed = errors.New("temporal: payload is not sealed and encryption is required")

// Plain is the codec of an installation without encryption.
var Plain = Codec{} //nolint:gochecknoglobals // a constant value

// Encode implements converter.PayloadCodec (the Temporal SDK gives it no
// context).
func (c Codec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	return c.EncodeContext(context.Background(), payloads)
}

// Decode implements converter.PayloadCodec (the Temporal SDK gives it no
// context). A strict codec refuses what it did not seal.
func (c Codec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	if c.strict {
		for _, p := range payloads {
			if string(p.GetMetadata()[converter.MetadataEncoding]) != EncodingSealed {
				return nil, ErrUnsealed
			}
		}
	}

	return c.DecodeContext(context.Background(), payloads)
}

// EncodeContext seals payloads, Vault calls bound by ctx.
func (c Codec) EncodeContext(ctx context.Context, payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	if c.s == nil {
		return payloads, nil
	}

	out := make([]*commonpb.Payload, len(payloads))

	for i, p := range payloads {
		raw, err := proto.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("temporal: encode payload: %w", err)
		}

		sealed, err := c.s.Seal(ctx, raw, payloadAAD)
		if err != nil {
			return nil, fmt.Errorf("temporal: seal payload: %w", err)
		}

		out[i] = &commonpb.Payload{
			Metadata: map[string][]byte{converter.MetadataEncoding: []byte(EncodingSealed)}, Data: sealed,
		}
	}

	return out, nil
}

// DecodeContext opens what the codec sealed, Vault calls bound by ctx.
func (c Codec) DecodeContext(ctx context.Context, payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(payloads))

	for i, p := range payloads {
		if string(p.GetMetadata()[converter.MetadataEncoding]) != EncodingSealed {
			out[i] = p

			continue
		}

		if c.s == nil {
			return nil, fmt.Errorf("temporal: a sealed payload, and encryption is not configured: %w", seal.ErrOpen)
		}

		raw, err := c.s.Open(ctx, p.GetData(), payloadAAD)
		if err != nil {
			return nil, fmt.Errorf("temporal: open payload: %w", err)
		}

		var original commonpb.Payload
		if err := proto.Unmarshal(raw, &original); err != nil {
			return nil, fmt.Errorf("temporal: decode payload: %w", err)
		}

		out[i] = &original
	}

	return out, nil
}

// DecodeOne is DecodeContext of one payload, which is not nil.
func (c Codec) DecodeOne(ctx context.Context, p *commonpb.Payload) (*commonpb.Payload, error) {
	out, err := c.DecodeContext(ctx, []*commonpb.Payload{p})
	if err != nil {
		return nil, err
	}

	return out[0], nil
}

// Sealer is the codec's sealer; nil without encryption.
func (c Codec) Sealer() seal.Sealer { return c.s } //nolint:ireturn // the package's interface

var _ converter.PayloadCodec = Codec{}

// payloadName is the full name of Temporal's payload message.
const payloadName = "temporal.api.common.v1.Payload"

// DecodeMessage decodes, in place, every payload m holds, however deep: a
// history event, a describe or list response read through the raw service
// API. Without encryption it changes nothing.
func (c Codec) DecodeMessage(ctx context.Context, m proto.Message) error {
	if c.s == nil || m == nil {
		return nil
	}

	return c.decodeIn(ctx, m.ProtoReflect())
}

func (c Codec) decodeIn(ctx context.Context, m protoreflect.Message) error {
	var err error

	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Message() == nil {
			return true
		}

		switch {
		case fd.IsList():
			list := v.List()
			for i := range list.Len() {
				if err = c.decodeValue(ctx, fd, list.Get(i).Message(), func(n protoreflect.Message) {
					list.Set(i, protoreflect.ValueOfMessage(n))
				}); err != nil {
					return false
				}
			}
		case fd.IsMap():
			if fd.MapValue().Message() == nil {
				return true
			}

			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				err = c.decodeValue(ctx, fd.MapValue(), mv.Message(), func(n protoreflect.Message) {
					v.Map().Set(k, protoreflect.ValueOfMessage(n))
				})

				return err == nil
			})
		default:
			err = c.decodeValue(ctx, fd, v.Message(), func(n protoreflect.Message) { m.Set(fd, protoreflect.ValueOfMessage(n)) })
		}

		return err == nil
	})

	return err
}

// decodeValue decodes a payload field's value, or descends into another
// message.
func (c Codec) decodeValue(
	ctx context.Context, fd protoreflect.FieldDescriptor, v protoreflect.Message, set func(protoreflect.Message),
) error {
	if fd.Message().FullName() != payloadName {
		return c.decodeIn(ctx, v)
	}

	p, ok := v.Interface().(*commonpb.Payload)
	if !ok {
		return nil
	}

	decoded, err := c.DecodeOne(ctx, p)
	if err != nil {
		return err
	}

	if decoded != p {
		set(decoded.ProtoReflect())
	}

	return nil
}
