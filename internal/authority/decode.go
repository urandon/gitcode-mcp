package authority

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const MaxContractBytes = 512 << 10

// Protected results reserve finite RPC/cursor envelope headroom. Total private
// transport responses retain the same 512 KiB cap as request envelopes.
const MaxProtectedResultBytes = MaxContractBytes - 4096

// DecodeJSON rejects unknown/duplicate keys and suffix documents without
// exposing parser errors or payload bytes. Boundaries are checked before DTOs.
func DecodeJSON(data []byte, target any) error {
	return decodeJSONBounded(data, target, MaxContractBytes)
}

// DecodeStoredJSON retains the same closed grammar for bounded private
// metadata files. It is not a transport budget override: RPC callers continue
// using DecodeJSON and the fixed 512 KiB request/response envelope ceiling.
func DecodeStoredJSON(data []byte, target any, limit int) error {
	if limit < 1 || limit > 4<<20 {
		return Error{InvalidContract}
	}
	return decodeJSONBounded(data, target, limit)
}

func decodeJSONBounded(data []byte, target any, limit int) error {
	if len(data) > limit || !utf8.Valid(data) {
		return Error{InvalidContract}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || len(data) > limit || data[0] != '{' {
		return Error{InvalidContract}
	}
	walk := json.NewDecoder(bytes.NewReader(data))
	if err := jsonValue(walk, 0, reflect.TypeOf(target)); err != nil {
		return Error{InvalidContract}
	}
	if _, err := walk.Token(); err != io.EOF {
		return Error{InvalidContract}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		// Strict versioned union decoders may reject a supported JSON shape
		// before outer validation. Preserve only this finite semantic code;
		// parser errors and arbitrary custom decoder details remain hidden.
		var contractError Error
		if errors.As(err, &contractError) && contractError.Code == Unsupported {
			return contractError
		}
		return Error{InvalidContract}
	}
	if validated, ok := target.(interface{ Validate() error }); ok {
		return validated.Validate()
	}
	return nil
}

func jsonValue(d *json.Decoder, depth int, schema reflect.Type) error {
	if depth > 24 {
		return Error{InvalidContract}
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	optional := false
	for schema != nil && schema.Kind() == reflect.Pointer {
		optional = true
		schema = schema.Elem()
	}
	if schema == reflect.TypeOf(json.RawMessage{}) {
		schema = nil
	}
	if t == nil {
		if schema == nil || optional || schema.Kind() == reflect.Slice || schema.Kind() == reflect.Interface {
			return nil
		}
		return Error{InvalidContract}
	}
	delim, ok := t.(json.Delim)
	if !ok {
		if schema != nil && schema.Kind() != reflect.Interface {
			switch schema.Kind() {
			case reflect.String:
				if _, ok := t.(string); !ok {
					return Error{InvalidContract}
				}
			case reflect.Bool:
				if _, ok := t.(bool); !ok {
					return Error{InvalidContract}
				}
			case reflect.Int, reflect.Int64, reflect.Uint64:
				if _, ok := t.(float64); !ok {
					return Error{InvalidContract}
				}
			default:
				return Error{InvalidContract}
			}
		}
		return nil
	}
	switch delim {
	case '{':
		var fields map[string]reflect.Type
		required := map[string]bool{}
		if schema != nil && schema.Kind() == reflect.Struct {
			fields = map[string]reflect.Type{}
			for i := 0; i < schema.NumField(); i++ {
				f := schema.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")
				name := tag[0]
				if name != "" && name != "-" {
					fields[name] = f.Type
					optional := false
					for _, option := range tag[1:] {
						if option == "omitempty" {
							optional = true
						}
					}
					if !optional {
						required[name] = true
					}
				}
			}
		} else if schema != nil && schema.Kind() != reflect.Interface {
			return Error{InvalidContract}
		}
		keys := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || keys[key] {
				return Error{InvalidContract}
			}
			keys[key] = true
			var child reflect.Type
			if fields != nil {
				var found bool
				child, found = fields[key]
				if !found {
					return Error{InvalidContract}
				}
			}
			if err := jsonValue(d, depth+1, child); err != nil {
				return err
			}
		}
		for key := range required {
			if !keys[key] {
				return Error{InvalidContract}
			}
		}
	case '[':
		var child reflect.Type
		if schema != nil && schema.Kind() == reflect.Slice {
			child = schema.Elem()
		} else if schema != nil && schema.Kind() != reflect.Interface {
			return Error{InvalidContract}
		}
		for d.More() {
			if err := jsonValue(d, depth+1, child); err != nil {
				return err
			}
		}
	default:
		return Error{InvalidContract}
	}
	_, err = d.Token()
	return err
}

func decodeYAML(data []byte, target any) error {
	if len(data) == 0 || len(data) > 64<<10 {
		return Error{InvalidContract}
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var n yaml.Node
	if err := d.Decode(&n); err != nil {
		return Error{InvalidContract}
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return Error{InvalidContract}
	}
	budget := 8192
	if err := yamlValue(&n, 0, &budget); err != nil {
		return Error{InvalidContract}
	}
	if len(n.Content) != 1 || yamlSchema(n.Content[0], reflect.TypeOf(target)) != nil {
		return Error{InvalidContract}
	}
	d = yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(target); err != nil {
		return Error{InvalidContract}
	}
	return nil
}

func yamlSchema(n *yaml.Node, schema reflect.Type) error {
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	switch schema.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return Error{InvalidContract}
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < schema.NumField(); i++ {
			f := schema.Field(i)
			name := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		for i := 0; i < len(n.Content); i += 2 {
			child, ok := fields[n.Content[i].Value]
			if !ok || yamlSchema(n.Content[i+1], child) != nil {
				return Error{InvalidContract}
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return Error{InvalidContract}
		}
		for _, child := range n.Content {
			if yamlSchema(child, schema.Elem()) != nil {
				return Error{InvalidContract}
			}
		}
	case reflect.String:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return Error{InvalidContract}
		}
	case reflect.Int, reflect.Int64:
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			return Error{InvalidContract}
		}
	default:
		return Error{InvalidContract}
	}
	return nil
}

func yamlValue(n *yaml.Node, depth int, budget *int) error {
	*budget--
	if *budget < 0 || depth > 16 || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return Error{InvalidContract}
	}
	if n.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || keys[key.Value] || key.Value == "<<" {
				return Error{InvalidContract}
			}
			keys[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := yamlValue(child, depth+1, budget); err != nil {
			return err
		}
	}
	return nil
}
