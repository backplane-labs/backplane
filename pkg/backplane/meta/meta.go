// Package meta is a call's metadata: string pairs the product attaches to
// a hook call that travel with it — through the binding's run to every
// activity the run calls, on either runtime and either transport. An
// activity handler reads them from its context, and a hook it calls in
// turn carries them on. What they mean is the product's: a user, a tenant,
// a locale. Backplane does not read them.
//
//	ctx = meta.With(ctx, "user", userID, "tenant", tenant)
//	out, err := ref.Call(ctx, in)
//
//	// in an activity handler
//	user := meta.Get(ctx, "user")
//
// They are as trustworthy as the service that set them: backplane takes a
// hook call only from a service holding the internal secret and calls
// activities with it.
package meta

import (
	"context"
	"maps"
)

type key struct{}

// pair is the length of a key, value pair.
const pair = 2

// With adds the pairs (key, value, key, value, ...) to ctx's metadata; a
// key already there takes the new value. An odd count panics.
func With(ctx context.Context, kv ...string) context.Context {
	if len(kv)%pair != 0 {
		panic("meta: With needs key, value pairs")
	}

	values := make(map[string]string, len(kv)/pair)
	for i := 0; i < len(kv); i += pair {
		values[kv[i]] = kv[i+1]
	}

	return Merge(ctx, values)
}

// Merge adds values to ctx's metadata; their keys take the new values.
func Merge(ctx context.Context, values map[string]string) context.Context {
	if len(values) == 0 {
		return ctx
	}

	out := From(ctx)
	if out == nil {
		out = make(map[string]string, len(values))
	}

	maps.Copy(out, values)

	return context.WithValue(ctx, key{}, out)
}

// Replace makes md ctx's metadata, whatever it had: what an incoming call
// brought.
func Replace(ctx context.Context, md map[string]string) context.Context {
	return context.WithValue(ctx, key{}, maps.Clone(md))
}

// From is a copy of ctx's metadata; nil when it has none.
func From(ctx context.Context) map[string]string {
	md, _ := ctx.Value(key{}).(map[string]string)
	if len(md) == 0 {
		return nil
	}

	return maps.Clone(md)
}

// Get is one value of ctx's metadata; "" when absent.
func Get(ctx context.Context, k string) string {
	md, _ := ctx.Value(key{}).(map[string]string)

	return md[k]
}
