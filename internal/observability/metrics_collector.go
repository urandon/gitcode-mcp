package observability

// AttachCollector binds a concrete per-boot scalar, not a supplied probe. It
// acquires no collector/execution lock and performs no disk or network work.
// Construction attaches before publishing the daemon's observation surfaces.
func (m *Metrics) AttachCollector(c *Collector) bool {
	if c == nil || c.boot != m.boot {
		return false
	}
	if !m.mu.TryLock() {
		m.loss()
		return false
	}
	defer m.mu.Unlock()
	if m.collector != nil && m.collector != c {
		return false
	}
	m.collector = c
	m.origins["collector"] = true
	m.refreshCollectorCounterLocked()
	return true
}

func (m *Metrics) refreshCollectorCounterLocked() {
	if m.collector == nil {
		return
	}
	c := m.cells["event_drops"]
	value := float64(m.collector.admissionDrops.Load())
	if !finiteMetric(value) {
		c.available, c.reason = false, "probe_unavailable"
		m.partial = true
		return
	}
	c.value, c.available, c.reason = value, true, ""
}
