package localauthority

import (
	"sync"

	"gitcode-mcp/internal/observability"
)

// A runtime boundary carries only immutable admitted observers and one finite
// outcome. It is historical proof, not a current-health image or caller event.
// Serving publication registers under admission ownership; the shutdown owner
// itself retains closing/stopped publication until it closes done.
type runtimePublication struct {
	observers [CoordinatorProjectLimit]observability.LocalProjectObserver
	count     int
	outcome   string
	joined    *sync.WaitGroup
	once      sync.Once
}

func (c *Coordinator) runtimePublication(outcome string) *runtimePublication {
	var publication *runtimePublication
	for _, project := range c.order {
		if !project.observation.Bound() {
			continue
		}
		if publication == nil {
			publication = &runtimePublication{outcome: outcome}
		}
		publication.observers[publication.count] = project.observation
		publication.count++
	}
	return publication
}

func (p *runtimePublication) publish() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.joined != nil {
			defer p.joined.Done()
		}
		for i := 0; i < p.count; i++ {
			if p.outcome == "" {
				p.observers[i].UnconfirmedCommit()
			} else {
				p.observers[i].PhaseChanged("runtime", p.outcome)
			}
		}
	})
}
