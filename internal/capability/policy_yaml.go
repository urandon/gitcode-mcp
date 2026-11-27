package capability

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const MaxPolicyDocumentBytes = 1 << 20

// ParsePolicyDocument reads only the dedicated capabilities section. Ordinary
// runtime fields are not authority. No decoder error/scalar is ever echoed.
func ParsePolicyDocument(data []byte) (*Specification, bool, error) {
	if len(data) > MaxPolicyDocumentBytes || !utf8.Valid(data) {
		return nil, false, InvalidPolicy()
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err == io.EOF {
		return nil, false, nil
	} else if err != nil {
		return nil, false, InvalidPolicy()
	}
	if dec.Decode(&extra) != io.EOF {
		return nil, false, InvalidPolicy()
	}
	if len(doc.Content) == 1 && doc.Content[0].Tag == "!!null" && doc.Content[0].Value == "" {
		return nil, false, nil
	}
	if len(doc.Content) != 1 {
		return nil, false, InvalidPolicy()
	}
	root, err := policyMap(doc.Content[0])
	if err != nil {
		return nil, false, err
	}
	node, ok := root["capabilities"]
	if !ok {
		return nil, false, nil
	}
	fields, err := policyMap(node)
	if err != nil {
		return nil, true, err
	}
	for key := range fields {
		if key != "schema" && key != "profile" && key != "ceiling" && key != "profiles" {
			return nil, true, InvalidPolicy()
		}
	}
	if n := fields["schema"]; n == nil || n.Tag != "!!int" || n.Value != "1" {
		return nil, true, InvalidPolicy()
	}
	spec := &Specification{Profiles: map[string]Rule{}}
	if n, ok := fields["profile"]; ok {
		if !policyName(n) {
			return nil, true, InvalidPolicy()
		}
		spec.Profile = n.Value
	}
	if n, ok := fields["ceiling"]; ok {
		spec.Ceiling, err = policyRule(n)
		if err != nil {
			return nil, true, err
		}
	}
	if n, ok := fields["profiles"]; ok {
		profiles, e := policyMap(n)
		if e != nil || len(profiles) > 32 {
			return nil, true, InvalidPolicy()
		}
		for name, node := range profiles {
			if !policyName(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}) {
				return nil, true, InvalidPolicy()
			}
			rule, e := policyRule(node)
			if e != nil {
				return nil, true, e
			}
			spec.Profiles[name] = rule
		}
	}
	if spec.Profile != "" {
		if _, ok := spec.Profiles[spec.Profile]; !ok {
			return nil, true, InvalidPolicy()
		}
	}
	return spec, true, nil
}

func policyMap(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content)%2 != 0 {
		return nil, InvalidPolicy()
	}
	out := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, InvalidPolicy()
		}
		if _, ok := out[key.Value]; ok {
			return nil, InvalidPolicy()
		}
		out[key.Value] = node.Content[i+1]
	}
	return out, nil
}

func policyRule(node *yaml.Node) (Rule, error) {
	fields, err := policyMap(node)
	if err != nil {
		return Rule{}, err
	}
	rule := Rule{}
	for key, node := range fields {
		if key != "allow" && key != "deny" || node.Kind != yaml.SequenceNode || node.Tag != "!!seq" || len(node.Content) > 512 {
			return Rule{}, InvalidPolicy()
		}
		list := []string{}
		seen := map[string]bool{}
		for _, n := range node.Content {
			if !policyName(n) || seen[n.Value] {
				return Rule{}, InvalidPolicy()
			}
			seen[n.Value] = true
			list = append(list, n.Value)
		}
		if key == "allow" {
			rule.Allow = list
		} else {
			rule.Deny = list
		}
	}
	return rule, nil
}

func policyName(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || len(n.Value) == 0 || len(n.Value) > 128 || strings.TrimSpace(n.Value) != n.Value {
		return false
	}
	for _, c := range n.Value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
