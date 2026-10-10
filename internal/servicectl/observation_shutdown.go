package servicectl

import (
	"context"
	"time"
)

// Cancel outside the execution lock, then wait for actual worker exit rather
// than a terminal job status. Filesystem stalls may outlive the deadline; they
// leave incomplete diagnostic evidence, never a false clean shutdown marker.
func (m *JobManager) shutdown(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		m.mu.Lock()
		m.closing = true
		cancels := make([]context.CancelFunc, 0, len(m.cancel))
		for _, cancel := range m.cancel {
			cancels = append(cancels, cancel)
		}
		m.mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			m.mu.Lock()
			active := len(m.inflightWorkers) + len(m.directCacheWriters)
			m.mu.Unlock()
			if active == 0 {
				close(done)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
