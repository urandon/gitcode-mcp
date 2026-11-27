package capability

// OnboardingEffects describes the intended operation, not an authorization
// grant. Keep local cache mutation distinct from mutation of GitCode itself.
type OnboardingEffects struct {
	LocalCacheWrite bool `json:"local_cache_write"`
	RemoteRead      bool `json:"remote_read"`
	RemoteWrite     bool `json:"remote_write"`
}

// ToolOnboardingEffects covers the sync/feedback onboarding cohort only. An
// absent entry makes no claim about other tools' effects or readiness.
func ToolOnboardingEffects(name string) (OnboardingEffects, bool) {
	switch name {
	case "sync_live":
		return OnboardingEffects{LocalCacheWrite: true, RemoteRead: true}, true
	case "feedback_status", "prepare_feedback":
		return OnboardingEffects{}, true
	case "submit_feedback":
		return OnboardingEffects{LocalCacheWrite: true, RemoteRead: true, RemoteWrite: true}, true
	default:
		return OnboardingEffects{}, false
	}
}

func (e OnboardingEffects) Description() string {
	if e.RemoteWrite {
		return "Creates a remote GitCode feedback issue and updates the local audit/cache; explicit live intent and a caller idempotency key are required."
	}
	if e.LocalCacheWrite {
		return "Reads GitCode and writes the local cache; it does not mutate remote GitCode data. The coarse read ceiling still excludes local cache writes."
	}
	return "Inspects/prepares feedback locally; it does not submit an issue or enable feedback. Explicit feedback status/preparation may resolve credential readiness without a provider request."
}
