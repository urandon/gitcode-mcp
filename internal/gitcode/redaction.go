package gitcode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var bearerPattern = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)

func NewRedactedCapture(rawURL string, headers http.Header, body []byte, err error, approvedHosts []string, sensitiveTerms ...string) RedactedCapture {
	capture := RedactedCapture{URL: RedactURL(rawURL, approvedHosts, sensitiveTerms...), Headers: RedactHeaders(headers, sensitiveTerms...), Body: RedactJSONBody(body, sensitiveTerms...)}
	if err != nil {
		capture.Error = RedactText(err.Error(), sensitiveTerms...)
	}
	return capture
}

func RedactHeaders(headers http.Header, sensitiveTerms ...string) map[string][]string {
	out := map[string][]string{}
	for key, values := range headers {
		lower := strings.ToLower(key)
		redactValue := lower == "authorization" || lower == "cookie" || lower == "set-cookie" || strings.Contains(lower, "token")
		for _, value := range values {
			if redactValue {
				out[key] = append(out[key], "[REDACTED]")
				continue
			}
			out[key] = append(out[key], RedactText(value, sensitiveTerms...))
		}
	}
	return out
}

func RedactURL(raw string, approvedHosts []string, sensitiveTerms ...string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return RedactText(raw, sensitiveTerms...)
	}
	if parsed.Host != "" && !hostAllowed(parsed.Hostname(), approvedHosts) {
		parsed.Host = "redacted.example.com"
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") {
			query.Set(key, "[REDACTED]")
		}
	}
	parsed.RawQuery = query.Encode()
	return RedactText(parsed.String(), sensitiveTerms...)
}

func RedactJSONBody(body []byte, sensitiveTerms ...string) []byte {
	if len(body) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return []byte(RedactText(string(body), sensitiveTerms...))
	}
	redacted := redactJSONValue(value, sensitiveTerms...)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return []byte(RedactText(string(body), sensitiveTerms...))
	}
	return encoded
}

func redactJSONValue(value any, sensitiveTerms ...string) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			lower := strings.ToLower(key)
			if lower == "authorization" || lower == "cookie" || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || lower == "owner" || lower == "repo" || lower == "repository" {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactJSONValue(child, sensitiveTerms...)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = redactJSONValue(child, sensitiveTerms...)
		}
		return out
	case string:
		return RedactText(v, sensitiveTerms...)
	default:
		return v
	}
}

func RedactText(text string, sensitiveTerms ...string) string {
	redacted := bearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	for _, term := range sensitiveTerms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, term, "[REDACTED]")
	}
	return redacted
}

func hostAllowed(host string, approved []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return true
	}
	for _, allowed := range approved {
		if strings.EqualFold(host, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func CaptureIsSanitized(c RedactedCapture, forbidden ...string) bool {
	var buf bytes.Buffer
	buf.WriteString(c.URL)
	buf.Write(c.Body)
	buf.WriteString(c.Error)
	for key, values := range c.Headers {
		buf.WriteString(key)
		for _, value := range values {
			buf.WriteString(value)
		}
	}
	text := buf.String()
	for _, token := range forbidden {
		if token != "" && strings.Contains(text, token) {
			return false
		}
	}
	return true
}
