package gitcode

import (
	"bytes"
	"net/http"
	"strings"

	"gitcode-mcp/internal/diagnostics"
)

func NewRedactedCapture(rawURL string, headers http.Header, body []byte, err error, approvedHosts []string, sensitiveTerms ...string) RedactedCapture {
	capture := RedactedCapture{URL: RedactURL(rawURL, approvedHosts, sensitiveTerms...), Headers: RedactHeaders(headers, sensitiveTerms...), Body: RedactJSONBody(body, sensitiveTerms...)}
	if err != nil {
		capture.Error = RedactText(err.Error(), sensitiveTerms...)
	}
	return capture
}

func RedactHeaders(headers http.Header, sensitiveTerms ...string) map[string][]string {
	return diagnostics.RedactHeaders(headers, sensitiveTerms...)
}

func RedactURL(raw string, approvedHosts []string, sensitiveTerms ...string) string {
	return diagnostics.RedactURL(raw, approvedHosts, sensitiveTerms...)
}

func RedactJSONBody(body []byte, sensitiveTerms ...string) []byte {
	return diagnostics.RedactJSONBody(body, sensitiveTerms...)
}

func RedactText(text string, sensitiveTerms ...string) string {
	return diagnostics.RedactText(text, sensitiveTerms...)
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
