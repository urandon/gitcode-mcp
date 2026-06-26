package gitcode

import (
	"encoding/json"
	"fmt"
	"strings"
)

func EncodeIssueLabels(labels []string) json.RawMessage {
	var cleaned []string
	for _, l := range labels {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}
	if len(cleaned) == 0 {
		return nil
	}
	data, err := json.Marshal(strings.Join(cleaned, ","))
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func NormalizeLabels(labels []GitCodeLabel) ([]string, error) {
	if len(labels) == 0 {
		return []string{}, nil
	}
	result := make([]string, 0, len(labels))
	for idx, label := range labels {
		if label.ID == 0 {
			return nil, &ErrSchemaDecode{
				Field:   fmt.Sprintf("labels[%d].id", idx),
				Message: "label id is required and must be non-zero",
			}
		}
		name := strings.TrimSpace(label.Name)
		if name == "" {
			return nil, &ErrSchemaDecode{
				Field:   fmt.Sprintf("labels[%d].name", idx),
				Message: "label name is required and must not be empty",
			}
		}
		result = append(result, name)
	}
	return result, nil
}

func NormalizeSingleLabel(label GitCodeLabel) (string, error) {
	if label.ID == 0 {
		return "", &ErrSchemaDecode{
			Field:   "label.id",
			Message: "label id is required and must be non-zero",
		}
	}
	name := strings.TrimSpace(label.Name)
	if name == "" {
		return "", &ErrSchemaDecode{
			Field:   "label.name",
			Message: "label name is required and must not be empty",
		}
	}
	return name, nil
}
