package config_test

import (
	"encoding/json"
	"slices"
	"testing"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/config"
	"github.com/backplane-labs/backplane/internal/registry"
)

func check(t *testing.T, svc registry.Service, in map[string]string) []config.Violation {
	t.Helper()

	violations, err := config.Check(svc, in)
	if err != nil {
		t.Fatal(err)
	}

	return violations
}

func has(vs []config.Violation, instance, path, code string) bool {
	return slices.ContainsFunc(vs, func(v config.Violation) bool {
		return v.Instance == instance && v.Path == path && v.Code == code
	})
}

func service(m ...*backplanev1.Manifest) registry.Service {
	svc := registry.Service{Name: "svc", Manifests: map[string]*backplanev1.Manifest{}}
	for _, x := range m {
		svc.Manifests[x.GetVersion()] = x
	}

	return svc
}

// The override is laid over each live instance's effective configuration
// and validated by the schema: bounds, CEL between fields, per instance.
func TestValidateOnInstances(t *testing.T) {
	t.Parallel()

	m := testManifest(t, "svc", "1.0.0")
	svc := service(m)
	svc.Instances = []registry.Instance{
		instance(t, m, "svc-a", map[string]any{"password": "secret-password"}),
		// b runs with limits raised by its env.
		instance(t, m, "svc-b", map[string]any{
			"password": "secret-password", "limits": map[string]any{"min": "50", "max": "60"},
		}),
	}

	if v := check(t, svc, map[string]string{"suffix": `"?"`, "limits.max": `70`, "timeout": `"2s"`}); len(v) > 0 {
		t.Fatalf("valid override rejected: %v", v)
	}

	// Beyond the bound: both instances.
	bound := check(t, svc, map[string]string{"limits.max": `200`})
	if !has(bound, "svc-a", "limits.max", "LTE_VIOLATED") || !has(bound, "svc-b", "limits.max", "LTE_VIOLATED") {
		t.Fatalf("bound: %v", bound)
	}

	// The CEL rule fails only where min is above the new max.
	rule := check(t, svc, map[string]string{"limits.max": `20`})
	if len(rule) != 1 || rule[0].Instance != "svc-b" || rule[0].Code != "RULE_VIOLATED" || rule[0].Path != "limits" {
		t.Fatalf("rule: %v", rule)
	}

	// A wrong type, as the SDK would decode it from KV.
	if v := check(t, svc, map[string]string{"timeout": `"soon"`}); !has(v, "svc-a", "timeout", "TYPE_MISMATCH") {
		t.Fatalf("type: %v", v)
	}

	// Not Live: rejected before any instance.
	static := check(t, svc, map[string]string{"static": `"y"`})
	if len(static) != 1 || static[0].Code != config.CodeNotLive || static[0].Instance != "" {
		t.Fatalf("static: %v", static)
	}
}

// The state carries "***" for the password, which fails its min_len on its
// own: a masked value counts as present and valid, only what the override
// breaks is reported.
func TestValidateMaskedSecret(t *testing.T) {
	t.Parallel()

	m := testManifest(t, "svc", "1.0.0")
	in := instance(t, m, "svc-a", map[string]any{"password": "secret-password"})

	var cfg map[string]any
	if err := json.Unmarshal(in.State.GetConfig(), &cfg); err != nil || cfg["password"] != "***" {
		t.Fatalf("state config: %v %v", cfg, err)
	}

	res, err := m.GetConfig().GetSchema().Validate(map[string]any{"password": "***"})
	if err != nil || !slices.ContainsFunc(res.GetErrors(), func(e *sp.ValidationError) bool { return e.GetPath() == "password" }) {
		t.Fatalf("the masked password should fail min_len on its own: %v %v", res, err)
	}

	svc := service(m)
	svc.Instances = []registry.Instance{in}

	if v := check(t, svc, map[string]string{"suffix": `"?"`}); len(v) > 0 {
		t.Fatalf("masked secret reported: %v", v)
	}
}

// No live instance: the schema defaults are the base.
func TestValidateOnDefaults(t *testing.T) {
	t.Parallel()

	svc := service(testManifest(t, "svc", "1.0.0"))

	if v := check(t, svc, map[string]string{"limits.max": `5`}); len(v) > 0 {
		t.Fatalf("valid on defaults: %v", v)
	}

	v := check(t, svc, map[string]string{"limits.max": `0`})
	if len(v) != 1 || v[0].Instance != "" || v[0].Code != "RULE_VIOLATED" {
		t.Fatalf("rule on defaults: %v", v)
	}
}

// Each instance is validated by its own version's schema: a path Live only
// in the newer version is ignored by the older instance, as its SDK does.
func TestValidateOwnVersion(t *testing.T) {
	t.Parallel()

	newer := testManifest(t, "svc", "2.0.0")
	older := testManifest(t, "svc", "1.0.0")
	older.Config.Live = slices.DeleteFunc(slices.Clone(older.GetConfig().GetLive()), func(p string) bool {
		return p == "timeout"
	})

	svc := service(older, newer)
	svc.Instances = []registry.Instance{
		instance(t, older, "svc-old", map[string]any{"password": "secret-password"}),
		instance(t, newer, "svc-new", map[string]any{"password": "secret-password"}),
	}

	v := check(t, svc, map[string]string{"timeout": `"soon"`})
	if len(v) != 1 || v[0].Instance != "svc-new" {
		t.Fatalf("own version: %v", v)
	}
}

// Without a schema: JSON and declared Live paths only.
func TestValidateWithoutSchema(t *testing.T) {
	t.Parallel()

	m := &backplanev1.Manifest{Service: "svc", Version: "1.0.0", Config: &backplanev1.ConfigSection{
		Keys: []string{"greeter", "port"}, Live: []string{"greeter.suffix"},
	}}
	svc := service(m)

	if v := check(t, svc, map[string]string{"greeter.suffix": `{"any": ["json"]}`}); len(v) > 0 {
		t.Fatalf("no schema: %v", v)
	}

	if v := check(t, svc, map[string]string{"port": `1`}); len(v) != 1 || v[0].Code != config.CodeNotLive {
		t.Fatalf("no schema, not live: %v", v)
	}

	if _, kv, _ := config.Parse(m.GetConfig(), map[string]string{"greeter.suffix": `"x"`}); kv["greeter.suffix"] != "x" {
		t.Fatalf("scalar without schema: %q", kv["greeter.suffix"])
	}
}

// One answer lists every problem: a path that is not Live and a value the
// schema rejects are reported together.
func TestValidateReportsEverything(t *testing.T) {
	t.Parallel()

	m := testManifest(t, "svc", "1.0.0")
	svc := service(m)
	svc.Instances = []registry.Instance{instance(t, m, "svc-a", map[string]any{"password": "secret-password"})}

	v := check(t, svc, map[string]string{"nope": `1`, "limits.max": `"many"`})
	if !has(v, "", "nope", config.CodeNotLive) || !slices.ContainsFunc(v, func(x config.Violation) bool {
		return x.Path == "limits.max"
	}) {
		t.Fatalf("want both violations, got %v", v)
	}
}
