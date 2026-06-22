package diagnostics

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const Redacted = "[REDACTED]"

var (
	bearerPattern      = regexp.MustCompile(`(?i)bearer\s+[^\s,;"'{}\[\]<>]+`)
	authHeaderPattern  = regexp.MustCompile(`(?im)(authorization\s*[:=]\s*)([^\r\n,;]+)`)
	cookieLinePattern  = regexp.MustCompile(`(?im)((?:set-)?cookie\s*[:=]\s*)([^\r\n]+)`)
	secretPairPattern  = regexp.MustCompile(`(?i)([?&;\s,]*(?:access[_-]?token|auth[_-]?token|gitcode[_-]?token|token|secret|api[_-]?key|access[_-]?key|private[_-]?key|password|credential)\s*[=:]\s*)([^\s&;,"'{}\[\]<>]+)`)
	jsonSecretPattern  = regexp.MustCompile(`(?i)("[^"]*(?:token|secret|authorization|cookie|api[_-]?key|access[_-]?key|private[_-]?key|password)[^"]*"\s*:\s*")([^"]*)(")`)
)

type Filter struct {
	SensitiveTerms []string
	ApprovedHosts  []string
}

func NewFilter(sensitiveTerms ...string) Filter {
	return Filter{SensitiveTerms: compact(sensitiveTerms)}
}

func (f Filter) WithApprovedHosts(hosts ...string) Filter {
	f.ApprovedHosts = compact(hosts)
	return f
}

func (f Filter) RedactText(text string) string {
	redacted := bearerPattern.ReplaceAllString(text, "Bearer "+Redacted)
	redacted = authHeaderPattern.ReplaceAllString(redacted, "${1}"+Redacted)
	redacted = cookieLinePattern.ReplaceAllString(redacted, "${1}"+Redacted)
	redacted = jsonSecretPattern.ReplaceAllString(redacted, "${1}"+Redacted+"${3}")
	redacted = secretPairPattern.ReplaceAllString(redacted, "${1}"+Redacted)
	for _, term := range f.SensitiveTerms {
		if term == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, term, Redacted)
	}
	return redacted
}

func (f Filter) RedactBytes(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	return []byte(f.RedactText(string(payload)))
}

func (f Filter) RedactHeaders(headers http.Header) map[string][]string {
	out := map[string][]string{}
	for key, values := range headers {
		lower := strings.ToLower(key)
		redactValue := lower == "authorization" || lower == "cookie" || lower == "set-cookie" || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key")
		for _, value := range values {
			if redactValue {
				out[key] = append(out[key], Redacted)
				continue
			}
			out[key] = append(out[key], f.RedactText(value))
		}
	}
	return out
}

func (f Filter) RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return f.RedactText(raw)
	}
	if parsed.Host != "" && !hostAllowed(parsed.Hostname(), f.ApprovedHosts) {
		parsed.Host = "redacted.example.com"
	}
	query := parsed.Query()
	for key := range query {
		if isSecretKey(key) {
			query.Set(key, Redacted)
		}
	}
	parsed.RawQuery = query.Encode()
	return f.RedactText(parsed.String())
}

func (f Filter) RedactJSONBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return f.RedactBytes(body)
	}
	encoded, err := json.Marshal(f.redactJSONValue(value))
	if err != nil {
		return f.RedactBytes(body)
	}
	return encoded
}

func (f Filter) RawAPIResponseSummary(statusCode int, body []byte) string {
	if len(body) == 0 {
		return "api_response: empty"
	}
	return "api_response: status=" + http.StatusText(statusCode) + " body=" + Redacted
}

func (f Filter) RedactedWriter(w io.Writer) io.Writer {
	return Writer{Writer: w, Filter: f}
}

type Writer struct {
	Writer io.Writer
	Filter Filter
}

func (w Writer) Write(p []byte) (int, error) {
	if w.Writer == nil {
		return len(p), nil
	}
	_, err := w.Writer.Write(w.Filter.RedactBytes(p))
	return len(p), err
}

func (f Filter) CaptureIsSanitized(parts ...string) bool {
	text := strings.Join(parts, "")
	for _, term := range f.SensitiveTerms {
		if term != "" && strings.Contains(text, term) {
			return false
		}
	}
	return !bearerPattern.MatchString(text) && !authHeaderPattern.MatchString(text)
}

func (f Filter) redactJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			if isJSONSecretKey(key) {
				out[key] = Redacted
				continue
			}
			out[key] = f.redactJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = f.redactJSONValue(child)
		}
		return out
	case string:
		return f.RedactText(v)
	default:
		return v
	}
}

func RedactText(text string, sensitiveTerms ...string) string {
	return NewFilter(sensitiveTerms...).RedactText(text)
}

func RedactBytes(payload []byte, sensitiveTerms ...string) []byte {
	return NewFilter(sensitiveTerms...).RedactBytes(payload)
}

func RedactHeaders(headers http.Header, sensitiveTerms ...string) map[string][]string {
	return NewFilter(sensitiveTerms...).RedactHeaders(headers)
}

func RedactURL(raw string, approvedHosts []string, sensitiveTerms ...string) string {
	return NewFilter(sensitiveTerms...).WithApprovedHosts(approvedHosts...).RedactURL(raw)
}

func RedactJSONBody(body []byte, sensitiveTerms ...string) []byte {
	return NewFilter(sensitiveTerms...).RedactJSONBody(body)
}

func NewRedactedWriter(w io.Writer, sensitiveTerms ...string) io.Writer {
	return NewFilter(sensitiveTerms...).RedactedWriter(w)
}

func BufferIsSanitized(buf bytes.Buffer, forbidden ...string) bool {
	return NewFilter(forbidden...).CaptureIsSanitized(buf.String())
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential")
}

func isJSONSecretKey(key string) bool {
	lower := strings.ToLower(key)
	return lower == "authorization" || lower == "cookie" || lower == "set-cookie" || lower == "owner" || lower == "repo" || lower == "repository" || isSecretKey(lower)
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

func compact(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
