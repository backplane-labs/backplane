package backplanetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gopherex/xconf/contrib/sources/env"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

const (
	// maxValidateDepth bounds the walk over a configuration's sections.
	maxValidateDepth = 64
	// envPrefix is added to the names of vars and stripped by the source,
	// so that a name the schema does not know is an error.
	envPrefix = "BACKPLANETEST_"
)

// Config loads a configuration of type C (a section such as greeter.Config
// or a whole service Config) as the service would, from the schema
// defaults and vars only: no file, no process environment, no Consul.
// vars are named as in the service's environment without its prefix:
// {"SUFFIX": "?"} for a section, {"GREETER_SUFFIX": "?"} for the whole
// Config; a name the schema does not know fails. Every Validate of C and
// its sections runs, as Open runs them. A failure fails the test.
func Config[C any](tb testing.TB, vars map[string]string) C {
	tb.Helper()

	c, err := LoadConfig[C](tb.Context(), vars)
	if err != nil {
		tb.Fatalf("backplanetest: config: %v", err)
	}

	return c
}

// LoadConfig is Config returning the error: to test what the
// configuration rejects.
func LoadConfig[C any](ctx context.Context, vars map[string]string) (C, error) {
	environ := make([]string, 0, len(vars))
	for k, v := range vars {
		environ = append(environ, envPrefix+k+"="+v)
	}

	slices.Sort(environ)

	c, err := config.Load[C](ctx, config.Service(defaultService), config.WithoutFile(), config.WithoutEnv(),
		config.Source(env.New(env.Name("env:backplanetest"), env.Prefix(envPrefix), env.Environment(environ), env.Strict())))
	if err != nil {
		return c, err
	}

	var errs []error

	validate(reflect.ValueOf(&c), "", 0, &errs)

	return c, errors.Join(errs...)
}

// validate calls Validate on v's sections depth-first, then on v; an
// embedded section whose Validate is promoted to v is called once, as v's.
func validate(v reflect.Value, path string, depth int, errs *[]error) {
	if depth > maxValidateDepth || !v.IsValid() {
		return
	}

	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			validate(v.Elem(), path, depth+1, errs)
		}
	case reflect.Struct:
		validateStruct(v, path, depth, errs)
	default:
	}
}

// validateStruct walks v's exported fields, then calls v's own Validate.
func validateStruct(v reflect.Value, path string, depth int, errs *[]error) {
	self := validator(v.Type())

	for i := range v.NumField() {
		f := v.Type().Field(i)
		if !f.IsExported() || (f.Anonymous && self && validator(f.Type)) {
			continue
		}

		validate(v.Field(i), joinPath(path, f), depth+1, errs)
	}

	if !self || !v.CanAddr() {
		return
	}

	val, ok := v.Addr().Interface().(config.Validator)
	if !ok {
		return
	}

	if err := val.Validate(); err != nil {
		if path != "" {
			err = fmt.Errorf("%s: %w", path, err)
		}

		*errs = append(*errs, err)
	}
}

func validator(t reflect.Type) bool {
	iface := reflect.TypeFor[config.Validator]()

	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}

func joinPath(path string, f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" || name == "-" {
		name = f.Name
	}

	if path == "" {
		return name
	}

	return path + "." + name
}
