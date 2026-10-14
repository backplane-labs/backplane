// Package envelope checks the browser SDK's error envelope (app-debug v1):
// the JSON body of the log record @backplane-labs/backplane-errors writes. The
// schema is the one the SDK's tests validate against.
package envelope

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed app-debug-v1.schema.json
var raw []byte

const resource = "urn:app-debug:envelope:1"

// ErrTrailing is JSON after the envelope's object.
var ErrTrailing = errors.New("trailing envelope content")

// Schema is the compiled envelope schema.
func Schema() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()

	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}

	if err := compiler.AddResource(resource, document); err != nil {
		return nil, fmt.Errorf("register schema: %w", err)
	}

	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}

	return schema, nil
}

// Decode reads one JSON object, numbers kept exact.
func Decode(body string) (map[string]any, error) {
	var value map[string]any

	decoder := json.NewDecoder(bytes.NewBufferString(body))
	decoder.UseNumber()

	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrTrailing
	}

	return value, nil
}
