package testnet

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoExternalNetwork(t *testing.T) {
	client := NoExternalNetwork(t)
	_, err := client.Get("https://api.example.com/api/v5/issues")
	if !errors.Is(err, ErrExternalNetwork) {
		t.Fatalf("external request error = %v, want ErrExternalNetwork", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("loopback request blocked: %v", err)
	}
	resp.Body.Close()
}

func TestIntegrationRequireLiveIntegration(t *testing.T) {
	RequireLiveIntegration(t)
}

func TestGitCodeAPIServerRecordsRequestsAndAuth(t *testing.T) {
	server := NewGitCodeAPIServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.BaseURL()+"/api/v5/repos/owner-a/repo-a/issues", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request mock API: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "MOCK-ISSUE-100") {
		t.Fatalf("response status=%d body=%q", resp.StatusCode, string(body))
	}

	counts := server.Counts()
	if counts.TotalRequests != 1 || counts.ListIssues != 1 || counts.AuthFailures != 0 || counts.UnexpectedRequests != 0 {
		t.Fatalf("counts = %#v", counts)
	}
	requests := server.Requests()
	if len(requests) != 1 || requests[0].Operation != "list_issues" || !requests[0].AuthorizationOK {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestGitCodeAPIServerFailureModes(t *testing.T) {
	tests := []struct {
		name string
		mode GitCodeAPIFailureMode
		want int
	}{
		{name: "bad request", mode: GitCodeAPIFailure400, want: http.StatusBadRequest},
		{name: "rate limited", mode: GitCodeAPIFailure429, want: http.StatusTooManyRequests},
		{name: "server error", mode: GitCodeAPIFailure500, want: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewGitCodeAPIServer(t, WithGitCodeAPIFailureMode(tt.mode))
			defer server.Close()

			req, err := http.NewRequest(http.MethodGet, server.BaseURL()+"/api/v5/repos/owner-a/repo-a/issues", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer test-token")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request mock API: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if counts := server.Counts(); counts.TotalRequests != 1 || counts.ListIssues != 1 {
				t.Fatalf("counts = %#v", counts)
			}
		})
	}
}

func TestGitCodeAPIServerCapturesCreateIdempotency(t *testing.T) {
	server := NewGitCodeAPIServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.BaseURL()+"/api/v5/repos/owner-a/repo-a/issues", strings.NewReader(`{"title":"Issue"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Idempotency-Key", "create-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request mock API: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	creates := server.CapturedCreateRequests()
	if len(creates) != 1 || !creates[0].AuthorizationOK || creates[0].IdempotencyKey != "create-1" {
		t.Fatalf("creates = %#v", creates)
	}
}
