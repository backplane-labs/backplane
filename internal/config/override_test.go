package config_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xconf"
	consulsrc "github.com/gopherex/xconf/contrib/sources/consul"
	"github.com/gopherex/xconf/contrib/sources/env"

	"github.com/backplane-labs/backplane/internal/config"
)

func TestParseRejects(t *testing.T) {
	t.Parallel()

	sec := testManifest(t, "svc", "1.0.0").GetConfig()

	_, _, violations := config.Parse(sec, map[string]string{
		"static":       `"y"`,    // not Live
		"limits":       `{}`,     // a section, not a Live field
		"suffix":       `{`,      // malformed
		"limits.max":   `null`,   // null
		"timeout":      `"1s" 2`, // trailing data
		"features.foo": `true`,   // below a Live field
		"tags":         `["a"]`,  // fine
		"limits.min":   ` 2 `,    // fine
		"nope":         `"x"`,    // unknown
	})

	got := map[string]string{}
	for _, v := range violations {
		got[v.Path] = v.Code
	}

	want := map[string]string{
		"static": config.CodeNotLive, "limits": config.CodeNotLive, "suffix": config.CodeInvalidJSON,
		"limits.max": config.CodeNull, "timeout": config.CodeInvalidJSON, "features.foo": config.CodeNotLive,
		"nope": config.CodeNotLive,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("violations %v, want %v", got, want)
	}
}

// Every value kind encodes as the SDK's Consul source decodes it.
func TestEncodeKV(t *testing.T) {
	t.Parallel()

	sec := testManifest(t, "svc", "1.0.0").GetConfig()

	values, kv, violations := config.Parse(sec, map[string]string{
		"suffix":     `"?"`,
		"limits.min": `2`,
		"limits.max": `1e1`,
		"timeout":    `1500000000`,
		"tags":       `["a", "b"]`,
		"features":   `{"x": true}`,
	})
	if len(violations) > 0 {
		t.Fatal(violations)
	}

	want := map[string]string{
		"suffix": "?", "limits.min": "2", "limits.max": "10", "timeout": "1.5s",
		"tags": `["a","b"]`, "features": `{"x":true}`,
	}
	if !maps.Equal(kv, want) {
		t.Fatalf("kv %v, want %v", kv, want)
	}

	if string(values["tags"]) != `["a","b"]` || string(values["limits.max"]) != "1e1" {
		t.Fatalf("values are saved as given (compact): %s %s", values["tags"], values["limits.max"])
	}

	if _, text, _ := config.Parse(sec, map[string]string{"timeout": `"2m"`}); text["timeout"] != "2m" {
		t.Fatalf("duration text is kept: %q", text["timeout"])
	}
}

// lister serves KV pairs as Consul's List would.
type lister api.KVPairs

func (l lister) List(prefix string, _ *api.QueryOptions) (api.KVPairs, *api.QueryMeta, error) {
	var out api.KVPairs

	for _, p := range l {
		if strings.HasPrefix(p.Key, prefix) {
			out = append(out, p)
		}
	}

	return out, &api.QueryMeta{LastIndex: 1}, nil
}

// The encoding round trip: what backplane writes to KV, the SDK's Consul
// source (xconf NewPrefix, _revision ignored) decodes into the same Live
// values.
func TestKVRoundTrip(t *testing.T) {
	t.Parallel()

	m := testManifest(t, "svc", "1.0.0")

	_, kv, violations := config.Parse(m.GetConfig(), map[string]string{
		"suffix": `"?!"`, "limits.max": `20`, "timeout": `"1m30s"`, "tags": `["a","b"]`, "features": `{"x":true}`,
		"backplane.log_level": `"debug"`,
	})
	if len(violations) > 0 {
		t.Fatal(violations)
	}

	var pairs api.KVPairs

	for _, ops := range config.Txns("svc", 7, kv, nil) {
		for _, op := range ops {
			if op.KV.Verb == api.KVSet {
				pairs = append(pairs, &api.KVPair{Key: op.KV.Key, Value: op.KV.Value, ModifyIndex: 1})
			}
		}
	}

	src := consulsrc.NewPrefix(lister(pairs), config.Prefix("svc"), consulsrc.IgnoreKeys(config.RevisionKey))
	base := env.New(env.Prefix("SVC_"), env.Environment([]string{"SVC_PASSWORD=long-enough"}))

	snap, err := xconf.Load(context.Background(), m.GetConfig().GetSchema(), base, src)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := xconf.Decode[testConfig](snap)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Suffix.Get() != "?!" || cfg.Limits.Max.Get() != 20 || cfg.Limits.Min.Get() != 1 ||
		cfg.Timeout.Get() != 90*time.Second || !slices.Equal(cfg.Tags.Get(), []string{"a", "b"}) ||
		!cfg.Features.Get()["x"] || cfg.LogLevel.Get() != "debug" {
		t.Fatalf("decoded: suffix %q limits %d..%d timeout %v tags %v features %v log %q",
			cfg.Suffix.Get(), cfg.Limits.Min.Get(), cfg.Limits.Max.Get(), cfg.Timeout.Get(), cfg.Tags.Get(),
			cfg.Features.Get(), cfg.LogLevel.Get())
	}
}
