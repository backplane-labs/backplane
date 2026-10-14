// Package config loads a service's configuration: one struct, schema from
// the Go type (schemapb), layers merged by xconf:
//
//	schema defaults < file < env <SERVICE>_* (BACKPLANE_* for the SDK block) < Consul KV
//
// Consul KV applies only to Live fields. Load reads once; Open keeps the
// configuration live.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xconf"
	jsondec "github.com/gopherex/xconf/contrib/decoders/json"
	yamldec "github.com/gopherex/xconf/contrib/decoders/yaml"
	"github.com/gopherex/xconf/contrib/sources/env"
	"github.com/gopherex/xconf/contrib/sources/file"

	"github.com/backplane-labs/backplane/pkg/backplane/build"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/configrt"
)

// EnvFile names the default configuration file.
const EnvFile = "BACKPLANE_CONFIG_FILE"

// blockPrefix of the SDK block's environment, the same for every service.
const blockPrefix = "BACKPLANE_"

// Load reads the configuration once: defaults, file and environment. For
// tools and tests; services use Open through backplane.Open.
func Load[C any](ctx context.Context, opts ...Option) (C, error) {
	var zero C

	st, err := newSettings(opts)
	if err != nil {
		return zero, err
	}

	schema, err := reflectSchema[C](st.service)
	if err != nil {
		return zero, err
	}

	sources, err := st.baseSources(reflect.TypeFor[C]())
	if err != nil {
		return zero, err
	}

	v, err := xconf.LoadAs[C](ctx, schema, sources...)
	if err != nil {
		return zero, fmt.Errorf("config: %w", err)
	}

	return v, nil
}

func newSettings(opts []Option) (*settings, error) {
	st := &settings{}
	for _, o := range opts {
		o(st)
	}

	if st.service == "" {
		st.service = build.Get().Service
	}

	if st.service == "" {
		return nil, fmt.Errorf("config: %w (or pass config.Service())", build.ErrUnnamed)
	}

	if !st.fileSet {
		st.file = os.Getenv(EnvFile)
	}

	if !st.prefixSet {
		st.envPrefix = strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(st.service)) + "_"
	}

	return st, nil
}

// baseSources: file < BACKPLANE_* for the block (when t embeds it) <
// <SERVICE>_* < extra.
func (st *settings) baseSources(t reflect.Type) ([]xconf.Source, error) {
	var sources []xconf.Source
	if src := st.fileSource(); src != nil {
		sources = append(sources, src)
	}

	if !st.noEnv {
		if key := blockKey(t); key != "" {
			block, err := reflectSchema[Backplane]("backplane")
			if err != nil {
				return nil, err
			}

			// Named apart from the service's own env source: a service
			// named "backplane" reads BACKPLANE_* twice, once per schema.
			inner := env.New(env.Prefix(blockPrefix), env.Name(configrt.SourceEnv+blockPrefix+" ("+key+")"))
			sources = append(sources, nested{inner: inner, key: key, schema: block})
		}

		sources = append(sources, st.env(st.envPrefix))
	}

	return append(sources, st.extra...), nil
}

func (st *settings) env(prefix string) xconf.Source {
	return env.New(env.Prefix(prefix), env.Name(configrt.SourceEnv+prefix))
}

func (st *settings) fileSource() xconf.Source {
	if st.noFile || st.file == "" {
		return nil
	}

	decode := jsondec.Decode
	if ext := strings.ToLower(filepath.Ext(st.file)); ext == ".yaml" || ext == ".yml" {
		decode = yamldec.Decode
	}

	return file.New(st.file, decode, file.Name(configrt.SourceFile+st.file))
}

// blockKey is the JSON name of the field of type Backplane in t; empty when
// t does not carry the block.
func blockKey(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return ""
	}

	block := reflect.TypeFor[Backplane]()

	for i := range t.NumField() {
		f := t.Field(i)
		if f.Type != block {
			continue
		}

		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			return ""
		case "":
			return f.Name
		}

		return name
	}

	return ""
}

func reflectSchema[T any](service string) (*sp.Schema, error) {
	ver, err := sp.ParseVersion(build.Version)
	if err != nil {
		ver = sp.Ver(0, 0, 0)
	}

	schema, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), "config", ver))
	if err != nil {
		return nil, fmt.Errorf("config: reflect: %w", err)
	}

	return schema, nil
}

// errLiveInCollection: a Live value could never be published in place there.
var errLiveInCollection = errors.New("lists and maps cannot hold Live fields")

// checkLive rejects Live fields inside slices, maps and arrays: they could
// never be published in place.
func checkLive(t reflect.Type, in string, seen map[reflect.Type]bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if seen[t] {
		return nil
	}

	seen[t] = true

	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}

		if isLiveType(elem) || (elem.Kind() == reflect.Struct && containsLive(elem, map[reflect.Type]bool{})) {
			return fmt.Errorf("config: %s: %w", in, errLiveInCollection)
		}
	case reflect.Struct:
		if isLiveType(t) {
			return nil
		}

		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				if err := checkLive(f.Type, in+"."+f.Name, seen); err != nil {
					return err
				}
			}
		}
	default:
	}

	return nil
}

func containsLive(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}

	seen[t] = true

	for i := range t.NumField() {
		ft := t.Field(i).Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		if isLiveType(ft) || (ft.Kind() == reflect.Struct && containsLive(ft, seen)) {
			return true
		}
	}

	return false
}

func isLiveType(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(reflect.TypeFor[liveField]())
}
