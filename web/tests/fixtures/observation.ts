import type { ServiceSummary, MetricSnapshot, EventPage } from '../../src/lib/observation';

export const id = (digit: string) => digit.repeat(32);
export const ref = (kind: string, digit: string) => `${kind}-${id(digit)}`;
export function observationFixture(at = new Date().toISOString()): { summary: ServiceSummary; metrics: MetricSnapshot; page: EventPage } {
  const coverage = { state: 'ready', output_state: 'managed', dropped: 0, loss_epoch: 0, observed_boots: 2 };
  const summary: ServiceSummary = {
    schema: 1, protocol: 'observability.v1', admin_observation_version: '1', observed_at: at,
    event_filters: { stream: ['stdout', 'stderr'], severity: ['info', 'warning', 'error'], component: ['daemon', 'jobs', 'legacy'], code: ['daemon_boot', 'job_admitted', 'legacy_text_omitted'], phase: ['fetching'], collection: ['issues', 'other'], outcome: ['succeeded', 'failed'], failure_class: ['timeout'] },
    event_templates: { daemon_boot: { stream: 'stdout', severity: 'info', component: 'daemon', message: 'Daemon observation started.' }, job_admitted: { stream: 'stdout', severity: 'info', component: 'jobs', message: 'A new job was durably admitted.' }, legacy_text_omitted: { stream: 'stderr', severity: 'warning', component: 'legacy', message: 'Unrecognized legacy output was omitted.' } },
    collector: { boot_id: id('a'), generation: id('b'), latest_sequence: 2, coverage }, metrics_boot_id: id('a'), metrics_reset_at: at, metrics_sampled_at: at,
    observation_health: { state: 'partial', ledger_state: 'partial', output_state: 'managed', metrics_state: 'ready', dropped_events: 3, loss_epoch: 0, dropped_metrics: 0 },
    execution_health: { state: 'degraded', coverage: 'partial', reason: 'invalid_authority_evidence', source: 'durable_jobs_and_maintenance', limit: 128, omitted: 0, active: 1, recovered: 1, incidents: [
      { incident_ref: ref('correlation', 'c'), origin: 'repository_docs', collection: 'other', phase: 'inspection', state: 'active', reason: 'source_unavailable', coverage: 'partial', first_observed_at: at, last_observed_at: at, source_ref: ref('source', '1'), registration_ref: ref('registration', '2'), cache_ref: ref('cache', '3'), repo_ref: ref('repo', '4') },
      { incident_ref: ref('correlation', 'd'), origin: 'repository_docs', collection: 'other', phase: 'indexing', state: 'recovered', reason: 'embedding_failed', coverage: 'retained', first_observed_at: at, last_observed_at: at, last_success_at: at, source_ref: ref('source', '1'), registration_ref: ref('registration', '2'), cache_ref: ref('cache', '3'), repo_ref: ref('repo', '4') }
    ] }, resources: [{ name: 'daemon_uptime', labels: {}, value: 42 }, { name: 'jobs_queued', labels: {}, value: null, reason: 'probe_unavailable' }]
  };
  const metrics: MetricSnapshot = { boot_id: id('a'), reset_at: at, sampled_at: at, state: 'ready', dropped: 0, catalog: [
    { name: 'jobs_queued', type: 'gauge', unit: 'count', origin: 'jobs', reset: 'sample', window: '15s', availability: 'null_until_successful_probe', dimensions: {} },
    { name: 'daemon_uptime', type: 'gauge', unit: 'seconds', origin: 'runtime', reset: 'sample', window: '15s', availability: 'null_until_successful_probe', dimensions: {} }
  ], series: [{ name: 'jobs_queued', labels: {}, value: null, reason: 'probe_unavailable', history: [{ start: at, count: 0, sum: 0, min: null, max: null, delta: null, rate: null, samples: 0, complete: false, reason: 'interval_incomplete' }] }] };
  const page: EventPage = { events: [{ schema: 1, event_id: id('e'), occurred_at: at, boot_id: id('a'), sequence: 1, code: 'job_admitted', stream: 'stdout', severity: 'info', component: 'jobs', message: 'A new job was durably admitted.', job_ref: ref('job', '5') }], cursor: '', has_more: false, coverage };
  return { summary, metrics, page };
}
