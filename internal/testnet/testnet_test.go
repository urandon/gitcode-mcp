package testnet

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
