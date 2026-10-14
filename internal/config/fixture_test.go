package config_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
	sdkconfig "github.com/backplane-labs/backplane/pkg/backplane/config"
)

// limits is a section whose Live fields a CEL rule ties together.
type limits struct {
	Min sdkconfig.Live[int64] `json:"min" schemapb:"default=1"`
	Max sdkconfig.Live[int64] `json:"max" schemapb:"default=10;lte=100"`
}

// SchemaField adds the rule to the section's field.
func (*limits) SchemaField(f *sp.Schema_Field) error {
	f.Rules = append(f.Rules, &sp.Schema_Field_Rule{Expr: "this.min <= this.max", Message: "min must not exceed max"})

	return nil
}

// testConfig is a service configuration as authors write it: the SDK
// block, a secret with a constraint, static and Live fields of every
// encoding kind.
type testConfig struct {
	sdkconfig.Backplane `json:"backplane"`

	Password sdkconfig.Secret                `json:"password"           schemapb:"min_len=8"`
	Static   string                          `json:"static"             schemapb:"default=x"`
	Suffix   sdkconfig.Live[string]          `json:"suffix"             schemapb:"default=!"`
	Limits   limits                          `json:"limits"`
	Timeout  sdkconfig.Live[time.Duration]   `json:"timeout"            schemapb:"default=5s"`
	Tags     sdkconfig.Live[[]string]        `json:"tags,omitempty"`
	Features sdkconfig.Live[map[string]bool] `json:"features,omitempty"`
}

// testSchema is the schema the SDK reflects for testConfig.
func testSchema(t *testing.T, service string) *sp.Schema {
	t.Helper()

	s, err := sp.ReflectType[testConfig](sp.ID(sp.Namespace(service), "config", sp.Ver(0, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// testManifest is the manifest the SDK writes for testConfig: schema, keys
// and Live paths.
func testManifest(t *testing.T, service, version string) *backplanev1.Manifest {
	t.Helper()

	s := testSchema(t, service)
	sec := &backplanev1.ConfigSection{Schema: s}

	for _, f := range s.GetFields() {
		sec.Keys = append(sec.Keys, f.GetName())
	}

	var walk func(s *sp.Schema, prefix []string)

	walk = func(s *sp.Schema, prefix []string) {
		for _, f := range s.GetFields() {
			path := append(append([]string{}, prefix...), f.GetName())
			if f.GetAnnotations()["backplane.live"].GetBoolValue() {
				sec.Live = append(sec.Live, strings.Join(path, "."))

				continue
			}

			if o := f.GetObject(); o != nil {
				walk(o.GetSchema(), path)
			}
		}
	}
	walk(s, nil)

	return &backplanev1.Manifest{Service: service, Version: version, Config: sec}
}

// effective is an instance's effective configuration as the SDK publishes
// it in its state: baked, secrets masked, JSON (durations in nanoseconds).
func effective(t *testing.T, s *sp.Schema, values map[string]any) []byte {
	t.Helper()

	baked, res, err := s.Bake(values)
	if err != nil || res.Blocking() {
		t.Fatalf("bake: %v %v", err, res)
	}

	raw, err := json.Marshal(baked.Masked().ToGo())
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// instance is a live instance of m's version running with values.
func instance(t *testing.T, m *backplanev1.Manifest, id string, values map[string]any) registry.Instance {
	t.Helper()

	return registry.Instance{ID: id, State: &backplanev1.InstanceState{
		Id: id, Service: m.GetService(), Version: m.GetVersion(),
		Config: effective(t, m.GetConfig().GetSchema(), values),
	}}
}
