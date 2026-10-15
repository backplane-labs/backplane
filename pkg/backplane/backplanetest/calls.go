package backplanetest

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Answer makes fn answer ref's calls, from Call and, in Workflows, from
// workflows.CallHook; unanswered hooks fail with hook.ErrUnavailable as in
// production without a transport (workflows.CallHook: hook.ErrNoBinding).
// An answered hook is bound (hook.Ref.Bound), an unanswered one is not, and
// BindingChanges signals when Answer binds one.
func Answer[Req, Res any](h *Harness, ref hook.Ref[Req, Res], fn func(ctx context.Context, in Req) (Res, error)) {
	h.bind(ref.Name())
	h.rec.answer(ref.Name(), func(ctx context.Context, in []byte) ([]byte, error) {
		var req Req
		if err := decl.Decode(in, &req); err != nil {
			return nil, fmt.Errorf("backplanetest: decode %s: %w", ref.Name(), err)
		}

		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}

		return decl.Encode(res)
	})
}

// CallKey is the hook.Key of the call an Answer function serves; "" for a
// call without one.
func CallKey(ctx context.Context) string { return env.CallKeyOf(ctx) }

// Activity invokes a declared activity as a binding would, once: the
// handler's ctx carries activity.InfoOf with Attempt 1, a Key unique to
// this call ("backplanetest/<name>/<uuid>/1") and ctx's deadline;
// Heartbeat does nothing.
func Activity[Req, Res any](ctx context.Context, h *Harness, name string, in Req) (Res, error) {
	var out Res

	handler, ok := h.env.ActivityHandler(name)
	if !ok {
		return out, fmt.Errorf("%w: activity %q", ErrNotDeclared, name)
	}

	info := env.ActivityInfo{
		Attempt:   1,
		Key:       "backplanetest/" + name + "/" + uuid.NewString() + "/1",
		Heartbeat: func(...any) {},
	}
	if deadline, has := ctx.Deadline(); has {
		info.Deadline = deadline
	}

	return invoke[Res](env.WithActivityInfo(ctx, info), handler, in)
}

func invoke[Res any](ctx context.Context, handler env.Handler, in any) (Res, error) {
	var out Res

	raw, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("backplanetest: encode: %w", err)
	}

	res, err := handler(ctx, raw)
	if err != nil || res == nil {
		return out, err
	}

	if err := decl.Decode(res, &out); err != nil {
		return out, fmt.Errorf("backplanetest: decode: %w", err)
	}

	return out, nil
}
