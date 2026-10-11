package observability

// EntityRefs is an additive bridge on already-listed local resources. It does
// not authorize a lookup and never substitutes a private/public entity id.
type EntityRefs struct {
	Job          string `json:"job_ref,omitempty"`
	Registration string `json:"registration_ref,omitempty"`
	Cache        string `json:"cache_ref,omitempty"`
	Repo         string `json:"repo_ref,omitempty"`
	Source       string `json:"source_ref,omitempty"`
}

func ProjectEntityRefs(known Context) *EntityRefs {
	refs := EntityRefs{known.Job.value, known.Registration.value, known.Cache.value, known.Repo.value, known.Source.value}
	if refs == (EntityRefs{}) {
		return nil
	}
	return &refs
}
