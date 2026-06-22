package diagnostics

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestFilterRedactsDiagnosticSecrets(t *testing.T) {
	filter := NewFilter("glpat-private-token", "private-owner", "private-repo")
	input := `Authorization: Bearer glpat-private-token
Cookie: session=abc
https://private.example.test/path?access_token=glpat-private-token&safe=ok
{"token":"glpat-private-token","owner":"private-owner","repo":"private-repo","message":"raw api response body"}`
	got := filter.RedactText(input)
	for _, forbidden := range []string{"glpat-private-token", "private-owner", "private-repo", "Bearer glpat-private-token", "session=abc"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redacted text contains %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, Redacted) {
		t.Fatalf("redacted text missing marker: %s", got)
	}
}

func TestFilterRedactsHeadersURLJSONAndWriter(t *testing.T) {
	filter := NewFilter("secret-token", "private-owner", "private-repo").WithApprovedHosts("api.gitcode.com")
	headers := http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"Set-Cookie":    []string{"sid=secret-token"},
		"X-Trace":       []string{"private-owner/private-repo"},
	}
	redactedHeaders := filter.RedactHeaders(headers)
	if redactedHeaders["Authorization"][0] != Redacted || redactedHeaders["Set-Cookie"][0] != Redacted {
		t.Fatalf("sensitive headers not redacted: %#v", redactedHeaders)
	}
	if strings.Contains(redactedHeaders["X-Trace"][0], "private-owner") {
		t.Fatalf("non-sensitive header value not filtered: %#v", redactedHeaders)
	}

	redactedURL := filter.RedactURL("https://private.example.test/api?token=secret-token&repo=private-repo")
	for _, forbidden := range []string{"private.example.test", "secret-token", "private-repo"} {
		if strings.Contains(redactedURL, forbidden) {
			t.Fatalf("redacted URL contains %q: %s", forbidden, redactedURL)
		}
	}

	body := filter.RedactJSONBody([]byte(`{"authorization":"Bearer secret-token","data":{"owner":"private-owner","repo":"private-repo"}}`))
	for _, forbidden := range []string{"secret-token", "private-owner", "private-repo"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("redacted body contains %q: %s", forbidden, body)
		}
	}

	var buf bytes.Buffer
	writer := filter.RedactedWriter(&buf)
	if _, err := writer.Write([]byte("Authorization: Bearer secret-token for private-owner/private-repo")); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-token", "private-owner", "private-repo"} {
		if strings.Contains(buf.String(), forbidden) {
			t.Fatalf("redacted writer contains %q: %s", forbidden, buf.String())
		}
	}
}
