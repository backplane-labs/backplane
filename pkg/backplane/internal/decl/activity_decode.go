package decl

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// DecodeActivity checks raw input before a typed handler can lose fields.
// Only an actual decoding failure is marked final here.
func DecodeActivity(ctx context.Context, e *env.Env, name string, raw []byte, out any) error {
	guard := e.ActivityInputGuard()
	if guard != nil {
		if err := guard(ctx, name, raw); err != nil {
			return fmt.Errorf("input guard: %w", err)
		}
	}

	if err := decodeActivity(raw, out, guard != nil); err != nil {
		return env.NonRetryableError{Err: fmt.Errorf("decode: %w", err)}
	}

	return nil
}

func decodeActivity(raw []byte, out any, strict bool) error {
	if strict {
		if m, ok := message(out); ok {
			if err := (protojson.UnmarshalOptions{}).Unmarshal(raw, m); err != nil {
				return fmt.Errorf("protobuf JSON: %w", err)
			}

			return nil
		}
	}

	return Decode(raw, out)
}
