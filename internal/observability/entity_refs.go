package observability

// EntityRefs is an additive bridge on already-listed local resources. It does
// not authorize a lookup and never substitutes a private/public entity id.
type EntityRefs struct {
	Job          string `json:"job_ref,omitempty"`
	Registration string `json:"registration_ref,omitempty"`
	Cache        string `json:"cache_ref,omitempty"`
	Repo         string `json:"repo_ref,omitempty"`
	Source       string `json:"source_ref,omitempty"`
	Project      string `json:"project_ref,omitempty"`
}

func ProjectEntityRefs(known Context) *EntityRefs {
	if !validRef(known.Project.value, "project") {
		return nil
	}
	if known.Project.value != "" && (known.Job.value != "" || known.Registration.value != "" || known.Cache.value != "" || known.Repo.value != "" || known.Source.value != "") {
		return nil // mixed authority is unknown, never a fabricated remote bridge
	}
	refs := EntityRefs{Job: known.Job.value, Registration: known.Registration.value, Cache: known.Cache.value, Repo: known.Repo.value, Source: known.Source.value, Project: known.Project.value}
	if refs == (EntityRefs{}) {
		return nil
	}
	return &refs
}
