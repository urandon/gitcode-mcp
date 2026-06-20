package testnet

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

var ErrExternalNetwork = errors.New("external network blocked in offline test")

type guardTransport struct {
	base http.RoundTripper
}

func NoExternalNetwork(t testing.TB) *http.Client {
	t.Helper()
	return &http.Client{Transport: GuardedTransport(http.DefaultTransport)}
}

func GuardedTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return guardTransport{base: base}
}

func (g guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !isLoopbackHost(host) {
		return nil, ErrExternalNetwork
	}
	return g.base.RoundTrip(req)
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func RequireLiveIntegration(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("live integration skipped in short mode")
	}
	if os.Getenv("GITCODE_LIVE_TEST") != "1" {
		t.Skip("live integration skipped: GITCODE_LIVE_TEST=1 unset")
	}
	if strings.TrimSpace(os.Getenv("GITCODE_LIVE_TOKEN")) == "" && strings.TrimSpace(os.Getenv("GITCODE_TEST_TOKEN")) == "" {
		t.Skip("live integration skipped: GITCODE_LIVE_TOKEN unset")
	}
}

func LiveToken() string {
	if token := strings.TrimSpace(os.Getenv("GITCODE_LIVE_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GITCODE_TEST_TOKEN"))
}
