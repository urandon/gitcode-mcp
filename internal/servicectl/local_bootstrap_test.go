package servicectl

import (
	"context"
	"encoding/json"
	"testing"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localauthority"
)

func TestLocalBootstrapRefusesLegacyOptionsBeforeConfig(t *testing.T) {
	for name, mutate := range map[string]func(*Manager){
		"source": func(m *Manager) {
			m.Source = refusedLocalSource{}
		},
		"config":          func(m *Manager) { m.EffectiveConfig = &config.Config{} },
		"admin":           func(m *Manager) { m.AdminAutoStart = true },
		"cache":           func(m *Manager) { m.AdminCachePath = "synthetic-private" },
		"network runtime": func(m *Manager) { m.RuntimeDir = "synthetic-private" },
	} {
		t.Run(name, func(t *testing.T) {
			m := Manager{Local: &localauthority.Runtime{}}
			mutate(&m)
			requireAuthorityCode(t, m.Run(context.Background()), authority.PolicyDenied)
		})
	}
}

type refusedLocalSource struct{}

func (refusedLocalSource) Env(string) string               { panic("legacy environment source invoked") }
func (refusedLocalSource) UserHomeDir() (string, error)    { panic("legacy home lookup invoked") }
func (refusedLocalSource) UserConfigDir() (string, error)  { panic("legacy config lookup invoked") }
func (refusedLocalSource) UserCacheDir() (string, error)   { panic("legacy cache lookup invoked") }
func (refusedLocalSource) ReadFile(string) ([]byte, error) { panic("legacy config file invoked") }

func TestLocalServerCannotReachLegacyDispatch(t *testing.T) {
	s := RPCServer{Local: &localauthority.Runtime{}}
	for _, method := range []string{"Service.Status", "Service.Doctor", "Admin.Open", "Jobs.StartFake", "Jobs.StartRAGIndex", "Jobs.StartSync", "ObservabilityV1.Summary"} {
		_, err := s.dispatch(context.Background(), method, json.RawMessage(`{}`))
		requireAuthorityCode(t, err, authority.PolicyDenied)
	}
	// Runtime dispatch is deliberately still unavailable in this increment.
	raw, _ := json.Marshal(authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1})
	result, err := s.dispatch(context.Background(), "AuthorityV1.Negotiate", raw)
	caps, ok := result.(authority.Negotiation)
	if err != nil || !ok || caps.Available || len(caps.Methods) != 0 {
		t.Fatal("bootstrap advertised unfinished domain methods")
	}
}
