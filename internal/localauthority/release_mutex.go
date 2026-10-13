package localauthority

import (
	"sync"
	"sync/atomic"
)

type releaseEpoch struct{ done chan struct{} }

// A nonblocking acquisition may wait for the exact observed lock epoch. Every
// release broadcasts to its waiters; no worker consumes another's wake or spins
// on notifications produced by its own unrelated metadata reads.
type releaseMutex struct {
	lock  sync.Mutex
	epoch atomic.Pointer[releaseEpoch]
}

func (m *releaseMutex) changed() <-chan struct{} {
	if current := m.epoch.Load(); current != nil {
		return current.done
	}
	initial := &releaseEpoch{done: make(chan struct{})}
	if m.epoch.CompareAndSwap(nil, initial) {
		return initial.done
	}
	return m.epoch.Load().done
}
func (m *releaseMutex) Lock()         { m.changed(); m.lock.Lock() }
func (m *releaseMutex) TryLock() bool { m.changed(); return m.lock.TryLock() }
func (m *releaseMutex) Unlock() {
	m.unlockWithCheckpoint(nil)
}

// The checkpoint is private deterministic-test instrumentation, never a
// runtime/configured callback. A new epoch must not become observable while
// this owner still holds the real lock: waiters would miss its release.
func (m *releaseMutex) unlockWithCheckpoint(released func()) {
	m.lock.Unlock()
	if released != nil {
		released()
	}
	old := m.epoch.Swap(&releaseEpoch{done: make(chan struct{})})
	close(old.done)
}
