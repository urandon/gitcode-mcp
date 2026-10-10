package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Directory          string // fixed private product directory; never a public DTO
	LegacyDirectory    string
	ManagedOutput      bool
	ManagedOutputProbe func() bool
}
type pending struct {
	legacy        bool
	observedBytes int
	code          Code
	refs          Context
	at            time.Time
	bytes         int
}
type Coverage struct {
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
	OutputState   string `json:"output_state"`
	Dropped       uint64 `json:"dropped"`
	LossEpoch     uint64 `json:"loss_epoch"`
	ObservedBoots uint64 `json:"observed_boots"`
}
type Collector struct {
	mu                 sync.Mutex
	queue              chan pending
	stop, done         chan struct{}
	closing            bool
	queued, queueBytes int
	cleanRequested     bool
	storageFailed      bool
	generation, boot   string
	cursorKey          string
	sequence           uint64
	events             []Event
	historyBytes       int
	coverage           Coverage
	config             Config
	// A deterministic private storage seam for fault tests, not a public hook.
	open func(string) (*storage, []Event, bool, error)
}

var entropyFallback atomic.Uint64

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Observation must not abort execution on an OS entropy failure.
		n := entropyFallback.Add(1)
		stamp := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(n >> (8 * i))
			b[i+8] = byte(stamp >> (8 * i))
		}
	}
	return hex.EncodeToString(b[:])
}
func New(cfg Config) *Collector { return newCollector(cfg, openStorage) }
func newCollector(cfg Config, open func(string) (*storage, []Event, bool, error)) *Collector {
	c := &Collector{queue: make(chan pending, MaxEvents), stop: make(chan struct{}), done: make(chan struct{}), boot: randomID(), config: cfg, open: open,
		coverage: Coverage{State: "initializing", OutputState: "migration_required"}}
	if cfg.ManagedOutput {
		c.coverage.OutputState = "managed"
	}
	go c.run()
	return c
}

// Emit never waits for disk or execution locks. Rejected codes and references
// never enter the queue. Diagnostic loss must not become a job failure.
func (c *Collector) Emit(code Code, refs Context) bool {
	t, ok := catalog[code]
	if !ok {
		return false
	}
	e := Event{Schema: 1, EventID: c.boot, OccurredAt: time.Now().UTC(), BootID: c.boot, Sequence: 1, Stream: t.stream, Severity: t.severity, Component: t.component, Code: code, Message: t.message,
		JobRef: refs.Job.value, RegistrationRef: refs.Registration.value, CacheRef: refs.Cache.value, RepoRef: refs.Repo.value, CorrelationRef: refs.Correlation.value}
	b, err := marshalEvent(e)
	if err != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return false
	}
	if c.queued >= MaxEvents || c.queueBytes+len(b) > MaxQueueBytes {
		c.loseLocked("queue_overflow")
		c.coverage.Dropped++
		return false
	}
	c.queued++
	c.queueBytes += len(b)
	c.queue <- pending{code: code, refs: refs, at: e.OccurredAt, bytes: len(b)}
	return true
}
func (c *Collector) loseLocked(reason string) {
	c.coverage.State = "partial"
	c.coverage.Reason = reason
	c.coverage.LossEpoch++
}
func (c *Collector) Coverage() Coverage { c.mu.Lock(); defer c.mu.Unlock(); return c.coverage }
func (c *Collector) Close(ctx context.Context, workersUnwound bool) bool {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		c.cleanRequested = workersUnwound
		close(c.stop)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
		return true
	case <-ctx.Done():
		return false
	}
}
func (c *Collector) run() {
	defer close(c.done)
	s, events, unclean, err := c.open(c.config.Directory)
	managed := c.config.ManagedOutput
	if c.config.ManagedOutputProbe != nil {
		managed = c.config.ManagedOutputProbe()
	}
	legacyState := ""
	if c.config.LegacyDirectory != "" {
		files, legacyErr := LegacyInventory(c.config.LegacyDirectory)
		if legacyErr != nil {
			legacyState = "unsupported"
		} else {
			for _, file := range files {
				if file.Present {
					legacyState = "migration_required"
				}
			}
		}
		if !managed && legacyState == "" {
			legacyState = "unsupported"
		}
	}
	c.mu.Lock()
	if managed {
		c.coverage.OutputState = "managed"
	}
	if legacyState != "" {
		c.coverage.OutputState = legacyState
	}
	if err != nil {
		c.generation = randomID()
		c.cursorKey = randomID()
		c.loseLocked("storage_unavailable")
	} else {
		c.generation = s.state.Generation
		c.cursorKey = s.state.CursorKey
		c.sequence = s.state.Sequence
		c.events = events
		for _, e := range events {
			b, _ := json.Marshal(e)
			c.historyBytes += len(b) + 1
		}
		c.coverage.Dropped += s.state.Dropped
		c.coverage.LossEpoch += s.state.LossEpoch
		if c.coverage.State == "initializing" {
			c.coverage.State = "ready"
		}
		if s.state.Partial {
			c.loseLocked("recovered_partial")
		}
	}
	// A durable bounded counter is not an OS restart count, and does not
	// disappear when older boot markers expire from the ledger.
	if s != nil {
		c.coverage.ObservedBoots = s.state.ObservedBoots
	} else {
		c.coverage.ObservedBoots = 1
	}
	c.mu.Unlock()
	if s != nil {
		defer s.close()
	}
	c.write(s, pending{code: Boot, at: time.Now().UTC()})
	if unclean {
		c.write(s, pending{code: PriorBootUnclean, at: time.Now().UTC()})
	}
	if legacyState == "migration_required" {
		for index, stream := range []string{"stdout", "stderr"} {
			position := LegacyCursor{}
			if s != nil {
				position = s.state.LegacyCursors[index]
			}
			sample, err := ReadLegacyFrom(c.config.LegacyDirectory, stream, position)
			if err != nil {
				c.mu.Lock()
				c.loseLocked("legacy_unavailable")
				c.mu.Unlock()
				continue
			}
			for i, code := range sample.Codes {
				observed := 0
				if i == 0 {
					observed = sample.Bytes
				}
				c.write(s, pending{code: code, at: time.Now().UTC(), legacy: true, observedBytes: observed})
			}
			if s != nil {
				s.state.LegacyCursors[index] = sample.Cursor
				if err := s.checkpoint(); err != nil {
					c.mu.Lock()
					c.storageFailed = true
					c.loseLocked("storage_failed")
					c.mu.Unlock()
				}
			}
		}
	}
	for {
		select {
		case p := <-c.queue:
			c.write(s, p)
			c.mu.Lock()
			c.queued--
			c.queueBytes -= p.bytes
			c.mu.Unlock()
		case <-c.stop:
			for {
				select {
				case p := <-c.queue:
					c.write(s, p)
					c.mu.Lock()
					c.queued--
					c.queueBytes -= p.bytes
					c.mu.Unlock()
				default:
					goto drained
				}
			}
		drained:
			c.mu.Lock()
			clean := c.cleanRequested
			c.mu.Unlock()
			code := Shutdown
			if !clean {
				code = ShutdownIncomplete
			}
			c.write(s, pending{code: code, at: time.Now().UTC()})
			if s != nil {
				if err := s.syncFiles(); err != nil {
					c.mu.Lock()
					c.storageFailed = true
					c.loseLocked("storage_failed")
					c.mu.Unlock()
				}
				c.mu.Lock()
				s.state.Dropped = c.coverage.Dropped
				s.state.LossEpoch = c.coverage.LossEpoch
				s.state.Partial = c.coverage.State != "ready"
				s.state.Clean = clean && !c.storageFailed
				s.state.Sequence = c.sequence
				c.mu.Unlock()
				if err := s.checkpoint(); err != nil {
					c.mu.Lock()
					c.loseLocked("storage_failed")
					c.mu.Unlock()
				}
			}
			return
		}
	}
}
func (c *Collector) write(s *storage, p pending) {
	t := catalog[p.code]
	c.mu.Lock()
	if c.sequence == ^uint64(0) {
		c.loseLocked("sequence_exhausted")
		c.mu.Unlock()
		return
	}
	c.sequence++
	sequence := c.sequence
	c.mu.Unlock()
	e := Event{Schema: 1, EventID: randomID(), OccurredAt: p.at, BootID: c.boot, Sequence: sequence, Stream: t.stream, Severity: t.severity, Component: t.component, Code: p.code, Message: t.message,
		Legacy: p.legacy, ObservedBytes: p.observedBytes,
		JobRef: p.refs.Job.value, RegistrationRef: p.refs.Registration.value, CacheRef: p.refs.Cache.value, RepoRef: p.refs.Repo.value, CorrelationRef: p.refs.Correlation.value}
	b, err := marshalEvent(e)
	if err != nil {
		c.mu.Lock()
		c.loseLocked("catalog_invalid")
		c.mu.Unlock()
		return
	}
	var evicted uint64
	if err == nil && s != nil && !c.storageFailed {
		c.mu.Lock()
		s.state.Dropped = c.coverage.Dropped
		s.state.LossEpoch = c.coverage.LossEpoch
		s.state.Partial = c.coverage.State != "ready"
		c.mu.Unlock()
		evicted, err = s.append(e, b)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.storageFailed = true
		c.loseLocked("storage_failed")
	}
	// Copy-on-write bounded snapshots; readers never wait for disk and cannot
	// mutate retained events. Eviction also invalidates old scan positions.
	start := 0
	size := c.historyBytes
	for start < len(c.events) && c.events[start].Sequence <= evicted {
		encoded, _ := json.Marshal(c.events[start])
		size -= len(encoded) + 1
		start++
	}
	kept := make([]Event, 0, len(c.events)-start+1)
	kept = append(kept, c.events[start:]...)
	kept = append(kept, e)
	size += len(b)
	for size > LedgerBytes && len(kept) > 1 {
		encoded, _ := json.Marshal(kept[0])
		size -= len(encoded) + 1
		kept = kept[1:]
	}
	c.events = kept
	c.historyBytes = size
}
