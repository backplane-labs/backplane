package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Validator is implemented by a configuration, or any section of it (the
// value of a Live field too), that checks itself beyond the schema: Open
// fails with its error and a live update it rejects is not applied. Value
// or pointer receiver.
type Validator interface{ Validate() error }

// maxValidateDepth bounds the walk; configurations are trees, not graphs.
const maxValidateDepth = 64

// validate runs every Validator in v (a pointer to the configuration):
// sections first, depth-first, then the enclosing struct; errors join.
func validate(v any) error {
	var errs []error

	walkValidators(reflect.ValueOf(v), "", 0, true, &errs)

	return errors.Join(errs...)
}

// walkValidators visits v's sections, then calls v's own Validate when self:
// an embedded section whose Validate is promoted to the enclosing struct is
// called once, as the enclosing one's.
func walkValidators(v reflect.Value, path string, depth int, self bool, errs *[]error) {
	if depth > maxValidateDepth || !v.IsValid() {
		return
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkValidators(v.Elem(), path, depth+1, self, errs)
		}

		return
	case reflect.Struct:
		if isLiveType(v.Type()) {
			walkLive(v, path, depth, errs)

			return
		}

		walkFields(v, path, depth, errs)
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkValidators(v.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1, true, errs)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			walkValidators(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), depth+1, true, errs)
		}
	default:
	}

	if !self {
		return
	}

	if err := callValidate(v); err != nil {
		if path == "" {
			*errs = append(*errs, err)
		} else {
			*errs = append(*errs, fmt.Errorf("%s: %w", path, err))
		}
	}
}

// walkLive checks the value of Live field v as a section: a Live struct
// changes as a whole, so its Validate sees a consistent value.
func walkLive(v reflect.Value, path string, depth int, errs *[]error) {
	if l, ok := addr(v).(liveField); ok {
		walkValidators(l.value(), path, depth+1, true, errs)
	}
}

// addr is a pointer to v, to a copy when v is not addressable.
func addr(v reflect.Value) any {
	if !v.CanAddr() {
		c := reflect.New(v.Type()).Elem()
		c.Set(v)
		v = c
	}

	return v.Addr().Interface()
}

// walkFields walks the exported fields of struct v.
func walkFields(v reflect.Value, path string, depth int, errs *[]error) {
	outer := validator(v)

	for i := range v.NumField() {
		if f := v.Type().Field(i); f.IsExported() {
			promoted := f.Anonymous && outer && validator(v.Field(i))
			walkValidators(v.Field(i), join(path, fieldName(f)), depth+1, !promoted, errs)
		}
	}
}

// validator reports whether v has Validate with either receiver kind.
func validator(v reflect.Value) bool {
	iface := reflect.TypeFor[Validator]()

	return v.Kind() != reflect.Interface && (v.Type().Implements(iface) || reflect.PointerTo(v.Type()).Implements(iface))
}

// callValidate calls Validate through a pointer (both receiver kinds); a
// value that is not addressable (a map element) is copied first.
func callValidate(v reflect.Value) error {
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface || !validator(v) || !v.CanInterface() {
		return nil
	}

	if !v.CanAddr() {
		c := reflect.New(v.Type()).Elem()
		c.Set(v)
		v = c
	}

	if val, ok := v.Addr().Interface().(Validator); ok {
		return val.Validate() //nolint:wrapcheck // the author's error, prefixed with its path by the caller
	}

	return nil
}

func fieldName(f reflect.StructField) string {
	if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
		return name
	}

	return f.Name
}

func join(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}
