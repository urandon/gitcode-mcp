package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// legacyYAMLScalars preserves the old loader's quoted numbers and strconv bool
// spellings without parsing YAML syntax ourselves. Coercion is schema-scoped:
// string settings and unknown product sections keep their original bytes.
// Copy-on-write also keeps shared anchors intact for differently typed fields.
func legacyYAMLScalars(node *yaml.Node, schema reflect.Type, depth int, budget *int) (*yaml.Node, error) {
	(*budget)--
	if depth > 64 || *budget < 0 {
		return nil, fmt.Errorf("configuration scalar traversal limit")
	}
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if node.Kind == yaml.AliasNode {
		return legacyYAMLScalars(node.Alias, schema, depth+1, budget)
	}
	copy := *node
	if node.Kind == yaml.ScalarNode && (node.Tag == "!!str" || schema.Kind() == reflect.Bool && node.Tag == "!!int") {
		switch schema.Kind() {
		case reflect.Int, reflect.Int64:
			if value, err := strconv.ParseInt(node.Value, 10, 64); err == nil {
				copy.Tag, copy.Value = "!!int", strconv.FormatInt(value, 10)
			}
		case reflect.Float64:
			if value, err := strconv.ParseFloat(node.Value, 64); err == nil {
				copy.Tag, copy.Value = "!!float", strconv.FormatFloat(value, 'g', -1, 64)
				switch copy.Value {
				case "NaN":
					copy.Value = ".nan"
				case "+Inf":
					copy.Value = ".inf"
				case "-Inf":
					copy.Value = "-.inf"
				}
			}
		case reflect.Bool:
			if value, err := strconv.ParseBool(node.Value); err == nil {
				copy.Tag, copy.Value = "!!bool", strconv.FormatBool(value)
			}
		}
		return &copy, nil
	}
	if node.Kind != yaml.MappingNode || schema.Kind() != reflect.Struct && schema.Kind() != reflect.Map {
		return node, nil
	}
	copy.Content = append([]*yaml.Node(nil), node.Content...)
	fields := map[string]reflect.Type{}
	if schema.Kind() == reflect.Struct {
		yamlScalarFields(schema, fields)
	}
	for index := 0; index < len(copy.Content); index += 2 {
		key, value := copy.Content[index], copy.Content[index+1]
		target := fields[key.Value]
		if schema.Kind() == reflect.Map {
			target = schema.Elem()
		}
		if key.Tag == "!!merge" {
			target = schema
			if value.Kind == yaml.SequenceNode {
				merged := *value
				merged.Content = append([]*yaml.Node(nil), value.Content...)
				for i, item := range merged.Content {
					next, err := legacyYAMLScalars(item, schema, depth+1, budget)
					if err != nil {
						return nil, err
					}
					merged.Content[i] = next
				}
				copy.Content[index+1] = &merged
				continue
			}
		}
		if target != nil {
			next, err := legacyYAMLScalars(value, target, depth+1, budget)
			if err != nil {
				return nil, err
			}
			copy.Content[index+1] = next
		}
	}
	return &copy, nil
}

func yamlScalarFields(schema reflect.Type, fields map[string]reflect.Type) {
	for i := 0; i < schema.NumField(); i++ {
		field := schema.Field(i)
		tag := field.Tag.Get("yaml")
		if strings.Contains(tag, ",inline") {
			yamlScalarFields(field.Type, fields)
		} else if name := strings.Split(tag, ",")[0]; name != "" && name != "-" {
			fields[name] = field.Type
		}
	}
}
