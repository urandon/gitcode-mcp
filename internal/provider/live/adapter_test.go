package live

import (
	"errors"
	"testing"

	"gitcode-mcp/internal/gitcode"
)

func TestAdapterPackageExports(t *testing.T) {
	cfg := Config{
		Mode:        "live",
		LiveAllowed: true,
		Token:       "test-token",
	}
	provider, err := NewLiveProvider(cfg)
	if err != nil {
		t.Fatalf("NewLiveProvider returned error: %v", err)
	}
	if provider == nil {
		t.Fatalf("NewLiveProvider returned nil provider")
	}
	var _ Provider = provider
}

func TestNewLiveProviderRejectsWithoutLiveAllowed(t *testing.T) {
	cfg := Config{
		Mode:        "live",
		LiveAllowed: false,
		Token:       "test-token",
	}
	_, err := NewLiveProvider(cfg)
	if !IsProviderUnavailable(err) {
		t.Fatalf("expected IsProviderUnavailable, got %T %v", err, err)
	}
}

func TestNewLiveProviderRejectsEmptyToken(t *testing.T) {
	cfg := Config{
		Mode:        "live",
		LiveAllowed: true,
		Token:       "",
	}
	_, err := NewLiveProvider(cfg)
	if !IsProviderUnavailable(err) {
		t.Fatalf("expected IsProviderUnavailable, got %T %v", err, err)
	}
}

func TestNewLiveProviderRejectsNonLiveMode(t *testing.T) {
	cfg := Config{
		Mode:        "fixture",
		LiveAllowed: true,
		Token:       "test-token",
	}
	_, err := NewLiveProvider(cfg)
	if !IsProviderUnavailable(err) {
		t.Fatalf("expected IsProviderUnavailable for non-live mode, got %T %v", err, err)
	}
}

func TestNewHTTPClientConstructsSuccessfully(t *testing.T) {
	client, err := NewHTTPClient(HTTPClientConfig{
		BaseURL: "https://gitcode.com",
		Token:   "test-token",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}
	if client == nil {
		t.Fatalf("NewHTTPClient returned nil")
	}
	var _ *gitcode.HTTPClient = client
}

func TestNewHTTPClientRejectsEmptyBaseURL(t *testing.T) {
	client, err := NewHTTPClient(HTTPClientConfig{
		Token: "test-token",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient with empty base URL should default, got error: %v", err)
	}
	if client == nil {
		t.Fatalf("NewHTTPClient returned nil with empty base URL")
	}
}

func TestTypeAliasCompilation(t *testing.T) {
	var _ Client    = &gitcode.HTTPClient{}
	var _ Provider  = gitcode.NewUnavailableProvider("test")
	var _ Config    = gitcode.ProviderConfig{}
	var _ ErrProviderUnavailable = gitcode.ErrProviderUnavailable{}
}

func TestIsProviderUnavailableDelegates(t *testing.T) {
	err := gitcode.ErrProviderUnavailable{Reason: "test"}
	if !IsProviderUnavailable(err) {
		t.Fatalf("IsProviderUnavailable should return true for ErrProviderUnavailable")
	}
	if IsProviderUnavailable(errors.New("random")) {
		t.Fatalf("IsProviderUnavailable should return false for non-provider error")
	}
}
