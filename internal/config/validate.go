package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf/contrib/sources/env"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

// engines compiles schemas once: manifests in registry snapshots are
// immutable and shared, so a schema pointer names its content.
type engines struct {
	mu    sync.Mutex
	cache map[*sp.Schema]*sp.Engine
}

// maxEngines bounds the cache; manifests of gone versions leave it on the
// next reset.
const maxEngines = 256

func (e *engines) get(s *sp.Schema) (*sp.Engine, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if eng, ok := e.cache[s]; ok {
		return eng, nil
	}

	eng, err := sp.Compile(s)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}

	if e.cache == nil || len(e.cache) >= maxEngines {
		e.cache = map[*sp.Schema]*sp.Engine{}
	}

	e.cache[s] = eng

	return eng, nil
}

// validate checks a parsed override against svc (§5.2): on the effective
// configuration of every live instance — each by the schema of its own
// version, which is what that instance validates KV with — or, with no
// live instance, on the schema defaults of the latest manifest. Paths the
// instance's version does not declare Live are skipped for it: its SDK
// ignores them. Without a schema only the parse checks apply.
//
// What the instance validated already is not reported again: an error
// that its configuration has without the override too (a masked secret
// failing its own constraint, "***" being all backplane sees) is dropped,
// so masked values count as present and valid.
// check parses values and validates the valid part: paths rejected by
// parse are left out of the override and the rest still goes through the
// schemas, so one answer lists every problem.
func (e *engines) check(svc registry.Service, values map[string]string) (override, []Violation, error) {
	latest := svc.Latest()
	o, violations := parse(latest.GetConfig(), values)

	schema, err := e.validate(svc, latest, o)

	return o, append(violations, schema...), err
}

func (e *engines) validate(svc registry.Service, latest *backplanev1.Manifest, o override) ([]Violation, error) {
	var bases []base

	for _, in := range svc.Instances {
		st := in.State
		if st == nil || len(st.GetConfig()) == 0 {
			continue
		}

		m := svc.Manifest(st.GetVersion())
		if m.GetConfig().GetSchema() == nil {
			m = latest
		}

		bases = append(bases, base{instance: in.ID, section: m.GetConfig(), config: st.GetConfig()})
	}

	if len(bases) == 0 {
		bases = append(bases, base{section: latest.GetConfig()})
	}

	var out []Violation

	for _, b := range bases {
		if b.section.GetSchema() == nil {
			continue
		}

		v, err := e.validateOn(b, o)
		if err != nil {
			return nil, err
		}

		out = append(out, v...)
	}

	return out, nil
}

// base is a configuration an override is laid over: an instance's
// effective one, or the schema defaults when config is empty.
type base struct {
	instance string
	section  *backplanev1.ConfigSection
	config   []byte
}

func (e *engines) validateOn(b base, o override) ([]Violation, error) {
	schema := b.section.GetSchema()

	eng, err := e.get(schema)
	if err != nil {
		return nil, err
	}

	layer, err := kvLayer(b.section, o)
	if err != nil {
		// The instance's SDK fails the same way on these values: a
		// violation, not a failure of validation.
		v := Violation{Instance: b.instance, Code: CodeInvalidJSON, Message: err.Error()}

		return []Violation{v}, nil //nolint:nilerr // a violation, see above
	}

	before, err := b.values(schema)
	if err != nil {
		return nil, err
	}

	after, err := b.values(schema)
	if err != nil {
		return nil, err
	}

	known := map[string]bool{}
	for _, failure := range blocking(eng.Validate(before)) {
		known[errorKey(failure)] = true
	}

	var out []Violation

	for _, failure := range blocking(eng.Validate(merge(after, layer))) {
		if known[errorKey(failure)] {
			continue
		}

		out = append(out, Violation{
			Path: failure.GetPath(), Instance: b.instance,
			Code: strings.TrimPrefix(failure.GetCode().String(), "ERROR_CODE_"), Message: failure.GetMessage(),
		})
	}

	return out, nil
}

// kvLayer is the override exactly as the instance's SDK reads it from KV:
// the encoded values decoded by xconf's env decoding against the
// instance's schema, Live paths of its version only.
func kvLayer(section *backplanev1.ConfigSection, o override) (map[string]any, error) {
	vars := map[string]string{}

	var opts []env.Option

	for _, path := range slices.Sorted(maps.Keys(o.kv)) {
		if !slices.Contains(section.GetLive(), path) {
			continue
		}

		name := kvPath(path)
		vars[name] = o.kv[path]
		opts = append(opts, env.Bind(name, strings.Split(path, ".")...))
	}

	// The prefix of the SDK's Consul source (xconf NewPrefix): the names it
	// derives from the schema never meet the bound keys.
	opts = append(opts, env.Prefix("__XCONF_CONSUL_"))

	layer, err := env.Decode(section.GetSchema(), vars, opts...)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	return layer.Values, nil
}

// values decodes the base configuration for the engine (a fresh copy per
// call: validation resolves in place). The SDK's JSON writes durations as
// nanoseconds; they are turned back into durations.
func (b base) values(schema *sp.Schema) (map[string]any, error) {
	if len(b.config) == 0 {
		return map[string]any{}, nil
	}

	v, err := decodeJSON(b.config)
	if err != nil {
		return nil, fmt.Errorf("instance %s: config: %w", b.instance, err)
	}

	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("instance %s: config is not an object", b.instance) //nolint:err113 // one-off
	}

	durations(schema, schema, m)

	return m, nil
}

// durations replaces numbers of duration fields in m with durations.
func durations(root, s *sp.Schema, m map[string]any) {
	for _, f := range s.GetFields() {
		v, present := m[f.GetName()]
		if !present {
			continue
		}

		n, isNumber := v.(json.Number)
		sub, isObject := v.(map[string]any)

		switch {
		case f.GetDuration() != nil && isNumber:
			if i, err := n.Int64(); err == nil {
				m[f.GetName()] = time.Duration(i)
			}
		case isObject && subSchema(root, f) != nil:
			durations(root, subSchema(root, f), sub)
		}
	}
}

// merge lays src over dst as xconf merges layers: objects merge key by
// key, everything else is replaced.
func merge(dst, src map[string]any) map[string]any {
	for k, sv := range src {
		dm, dok := dst[k].(map[string]any)
		sm, sok := sv.(map[string]any)

		if dok && sok {
			dst[k] = merge(dm, sm)

			continue
		}

		dst[k] = sv
	}

	return dst
}

func blocking(res *sp.ValidationResult) []*sp.ValidationError {
	var out []*sp.ValidationError

	for _, e := range res.GetErrors() {
		if e.GetSeverity() != sp.Schema_Field_SEVERITY_WARNING {
			out = append(out, e)
		}
	}

	return out
}

func errorKey(e *sp.ValidationError) string {
	return e.GetPath() + "\x00" + e.GetCode().String() + "\x00" + e.GetMessage()
}

// kvPath is a Live path as a key under config/<service>/: "a.b" -> "a/b".
func kvPath(path string) string { return strings.ReplaceAll(path, ".", "/") }
