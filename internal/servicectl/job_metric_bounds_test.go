package servicectl

import (
	"strconv"
	"testing"
)

func TestJobMetricProbeWorkBoundIncludesEveryAuthorityRow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		jobs, queues int
		available    bool
	}{
		{"jobs_at_bound", maxJobMetricRows, 0, true},
		{"queues_at_bound", 0, maxJobMetricRows, true},
		{"combined_at_bound", maxJobMetricRows - 1, 1, true},
		{"jobs_over_bound", maxJobMetricRows + 1, 0, false},
		{"queues_over_bound", 0, maxJobMetricRows + 1, false},
		{"combined_over_bound", maxJobMetricRows, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewJobManager("")
			for i := 0; i < tc.jobs; i++ {
				// Nil and terminal rows still consume traversal work.
				if i%2 == 0 {
					m.jobs[strconv.Itoa(i)] = nil
				} else {
					m.jobs[strconv.Itoa(i)] = &Job{Status: JobStatusSucceeded}
				}
			}
			if tc.jobs >= 3 {
				m.jobs["0"] = &Job{Status: JobStatusQueued}
				m.jobs["1"] = &Job{Status: JobStatusRunning}
				m.jobs["2"] = &Job{Status: JobStatusCancelling}
			}
			for i := 0; i < tc.queues; i++ {
				m.syncCommitQueues[strconv.Itoa(i)] = nil
			}
			if tc.queues > 0 {
				m.syncCommitQueues["0"] = []syncCommitWaiter{{stageID: "first"}, {stageID: "second"}, {stageID: "third"}}
			}
			values := m.MetricProbes()
			if !m.mu.TryLock() {
				t.Fatal("bounded probe retained the execution mutex")
			}
			unchanged := len(m.jobs) == tc.jobs && len(m.syncCommitQueues) == tc.queues
			m.mu.Unlock()
			if !unchanged {
				t.Fatal("observation pruned execution authority")
			}
			if len(values) != 3 {
				t.Fatal("bounded probe changed its finite gauge catalog")
			}
			want := map[string]float64{"jobs_queued": 0, "jobs_active": 0, "writer_waiters": 0}
			if tc.jobs >= 3 {
				want["jobs_queued"], want["jobs_active"] = 1, 2
			}
			if tc.queues > 0 {
				want["writer_waiters"] = 2
			}
			for name, value := range values {
				if !tc.available {
					if value.Value != nil || value.Reason != "probe_unavailable" {
						t.Fatal("oversized authority produced a partial or fabricated total")
					}
				} else if value.Value == nil || *value.Value != want[name] {
					t.Fatal("exact-bound authority did not produce complete gauges")
				}
			}
		})
	}
}
