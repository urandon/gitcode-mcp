import type { ObservationRefs, ObservationSnapshot } from './admin';

export const observationProtocol = 'observability.v1';
export const filterKeys = ['stream', 'severity', 'component', 'code', 'phase', 'collection', 'outcome', 'failure_class'] as const;
export const eventDetailKeys = ['phase', 'collection', 'outcome', 'failure_class', 'work_kind', 'admin_route', 'maintenance_scope', 'previous_state', 'previous_failure_class'] as const;
export const refKinds = { job_ref: 'job', registration_ref: 'registration', cache_ref: 'cache', repo_ref: 'repo', source_ref: 'source', correlation_ref: 'correlation', stage_ref: 'stage', previous_registration_ref: 'registration' } as const;
export type RefKey = keyof typeof refKinds;
export type ServiceQuery = Partial<Record<typeof filterKeys[number] | RefKey | 'since' | 'until' | 'cursor' | 'event_id' | 'incident_ref', string>>;
// Page-session state only: no retained event bytes or browser storage. Keep the
// last observed boundary when the Service component is unmounted by navigation.
export type ObservationBoundary = { frozen: boolean; boot_id?: string; generation?: string; loss_epoch?: number };
export type EventTemplate = { stream: string; severity: string; component: string; message: string };
export type EventRecord = { schema: number; event_id: string; occurred_at: string; boot_id: string; sequence: number; code: string; stream: string; severity: string; component: string; message: string; legacy?: boolean; observed_bytes?: number } & ServiceQuery & Record<string, unknown>;
export type Incident = ObservationRefs & { incident_ref: string; origin: string; collection: string; phase?: string; state: string; reason: string; coverage: string; first_observed_at: string; last_observed_at: string; last_success_at?: string; retry_after?: string };
export type Coverage = { state: string; reason?: string; output_state: string; dropped: number; loss_epoch: number; observed_boots: number };
export type EventPage = { events: EventRecord[]; cursor: string; has_more: boolean; coverage: Coverage };
export type MetricPoint = { name: string; labels: Record<string, string>; value: number | null; reason?: string; count?: number; sum?: number; distribution?: number[] };
export type MetricBucket = { start: string; count: number; sum: number; min: number | null; max: number | null; delta: number | null; rate: number | null; samples: number; complete: boolean; reason?: string };
export type MetricDefinition = { name: string; type: string; unit: string; origin: string; reset: string; window: string; availability: string; dimensions: Record<string, string[]>; bounds?: number[] };
export type MetricSnapshot = { boot_id: string; reset_at: string; sampled_at: string; state: string; dropped: number; catalog: MetricDefinition[]; series: Array<MetricPoint & { history: MetricBucket[] }> };
export type ServiceSummary = {
  schema: number; protocol: string; admin_observation_version?: string; observed_at: string;
  event_filters?: Record<string, string[]>; event_templates?: Record<string, EventTemplate>;
  event_detail_values?: Record<string, string[]>;
  collector: { boot_id: string; generation: string; latest_sequence: number; coverage: Coverage };
  metrics_boot_id: string; metrics_reset_at: string; metrics_sampled_at: string;
  observation_health: { state: string; ledger_state: string; output_state: string; metrics_state: string; dropped_events: number; loss_epoch: number; dropped_metrics: number };
  execution_health: { state: string; coverage: string; reason: string; source: string; limit: number; omitted: number; active: number; recovered: number; incidents: Incident[] };
  resources: MetricPoint[];
};
const hex = /^[a-f0-9]{32}$/;
const enumValue = /^[a-z][a-z0-9_]{0,63}$/;
export const isID = (value: unknown): value is string => typeof value === 'string' && hex.test(value);
export const isRef = (value: unknown, kind: string): value is string => typeof value === 'string' && value.startsWith(`${kind}-`) && isID(value.slice(kind.length + 1));
export const isTime = (value: unknown): value is string => typeof value === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(value) && Number.isFinite(Date.parse(value)) && !value.startsWith('0001-');
const finite = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);
const count = (v: unknown): v is number => finite(v) && Number.isSafeInteger(v) && v >= 0;
const member = (v: unknown, choices: readonly string[]): v is string => typeof v === 'string' && choices.includes(v);
const records = (value: unknown): Record<string, unknown> => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
export function publicEnum(value: unknown, fallback = 'unknown'): string { return typeof value === 'string' && enumValue.test(value) ? value : fallback; }

// Only closed selectors, opaque identities and bounded tokens enter history.
// Rejected input is never echoed in an error or a request.
export function readServiceQuery(params: URLSearchParams): { query: ServiceQuery; invalid: boolean } {
  const query: ServiceQuery = {}; let invalid = false;
  const keys = [...filterKeys, ...Object.keys(refKinds), 'since', 'until', 'cursor', 'event_id', 'incident_ref'];
  for (const key of params.keys()) if (key.startsWith('obs_') && !keys.includes(key.slice(4))) invalid = true;
  for (const key of keys) {
    const values = params.getAll(`obs_${key}`);
    if (!values.length) continue;
    const value = values[0];
    const valid = values.length === 1 && (key in refKinds ? isRef(value, refKinds[key as RefKey]) : key === 'event_id' ? isID(value) : key === 'incident_ref' ? isRef(value, 'correlation') : key === 'cursor' ? /^[A-Za-z0-9_-]{1,512}$/.test(value) : key === 'since' || key === 'until' ? isTime(value) : enumValue.test(value));
    if (valid) query[key as keyof ServiceQuery] = value; else invalid = true;
  }
  if (Boolean(query.since) !== Boolean(query.until) || (query.since && query.until && (Date.parse(query.until) < Date.parse(query.since) || Date.parse(query.until) - Date.parse(query.since) > 7 * 86400000))) { delete query.since; delete query.until; invalid = true; }
  if (query.event_id && query.incident_ref) { delete query.event_id; delete query.incident_ref; invalid = true; }
  if (invalid) { delete query.cursor; delete query.event_id; delete query.incident_ref; }
  return { query, invalid };
}
export function writeServiceQuery(params: URLSearchParams, query: ServiceQuery): void {
  const clean = readServiceQuery(new URLSearchParams(Object.entries(query).filter(([,v]) => Boolean(v)).map(([k,v]) => [`obs_${k}`, v as string]))).query;
  for (const [key, value] of Object.entries(clean)) params.set(`obs_${key}`, value as string);
}
export function eventRequest(query: ServiceQuery): string {
  const p = new URLSearchParams(); writeServiceQuery(p, query);
  const request = new URLSearchParams({ limit: '50' });
  for (const [key, value] of p) if (key !== 'obs_event_id' && key !== 'obs_incident_ref') request.set(key.slice(4), value);
  return `/api/admin/v1/observability/events?${request}`;
}
export function resetPage(query: ServiceQuery): ServiceQuery { const { cursor, event_id, incident_ref, ...rest } = query; return rest; }
export function catalogServiceQuery(query: ServiceQuery, filters: Record<string, string[]>): { query: ServiceQuery; invalid: boolean } {
  const clean = { ...query }; let invalid = false;
  for (const key of filterKeys) if (clean[key] && !filters[key]?.includes(clean[key]!)) { delete clean[key]; invalid = true; }
  return { query: invalid ? resetPage(clean) : clean, invalid };
}

export type ObservationTarget = { kind: 'job'; id: string } | { kind: 'maintenance'; id: string } | { kind: 'cache'; cache: string } | { kind: 'repo' | 'source'; cache: string; repo: string; source?: string };
// Join only to already-listed authority. Source is strict and wins over its
// parent: disappearing or ambiguous sources must never select the first source.
export function observationTarget(snapshot: ObservationSnapshot, refs: ObservationRefs): ObservationTarget | undefined {
  const matches: ObservationTarget[] = [];
  if (refs.source_ref) {
    for (const cache of snapshot.caches) for (const repo of cache.repositories) for (const source of repo.documentation.sources || []) {
      const r = source.observation_refs;
      if (r?.source_ref === refs.source_ref && (!refs.cache_ref || r.cache_ref === refs.cache_ref) && (!refs.repo_ref || r.repo_ref === refs.repo_ref) && (!refs.registration_ref || r.registration_ref === refs.registration_ref)) matches.push({ kind: 'source', cache: cache.cache_ref, repo: repo.repo_id, source: source.source_registration_id });
    }
  } else if (refs.job_ref) {
    for (const job of snapshot.jobs) if (job.observation_refs?.job_ref === refs.job_ref) matches.push({ kind: 'job', id: job.id });
  } else if (refs.registration_ref) {
    for (const registration of snapshot.maintenance) if (registration.observation_refs?.registration_ref === refs.registration_ref) matches.push({ kind: 'maintenance', id: registration.registration_id });
  } else if (refs.repo_ref) {
    for (const cache of snapshot.caches) for (const repo of cache.repositories) if (repo.observation_refs?.repo_ref === refs.repo_ref && (!refs.cache_ref || repo.observation_refs.cache_ref === refs.cache_ref)) matches.push({ kind: 'repo', cache: cache.cache_ref, repo: repo.repo_id });
  } else if (refs.cache_ref) {
    for (const cache of snapshot.caches) if (cache.observation_refs?.cache_ref === refs.cache_ref) matches.push({ kind: 'cache', cache: cache.cache_ref });
  }
  return matches.length === 1 ? matches[0] : undefined;
}
export function refProjection(value: unknown): ServiceQuery {
  const result: ServiceQuery = {};
  for (const [key, kind] of Object.entries(refKinds)) { const v = records(value)[key]; if (isRef(v, kind)) result[key as RefKey] = v; }
  return result;
}
function validRefs(value: Record<string, unknown>): boolean {
  return Object.entries(refKinds).every(([key, kind]) => value[key] === undefined || value[key] === '' || isRef(value[key], kind));
}

const incidentReasons = ['operation_failed', 'provider_timeout', 'provider_unavailable', 'provider_forbidden', 'provider_rate_limited', 'provider_invalid', 'cache_authority', 'configuration_invalid', 'embedding_failed', 'interrupted', 'writer_wait', 'retry_wait', 'succeeded', 'source_unavailable', 'source_policy_invalid', 'source_state_unavailable', 'source_inspection_unobserved'];
export function incidentProjection(value: unknown): Incident | undefined {
  const i = records(value);
  if (!validRefs(i) || !isRef(i.incident_ref, 'correlation') || !member(i.origin, ['sync', 'rag', 'repository_docs', 'maintenance', 'other']) || !member(i.collection, ['issues', 'issue_comments', 'wiki', 'pulls', 'pr_comments', 'other']) || !member(i.state, ['active', 'recovered', 'retrying', 'waiting_writer', 'interrupted']) || !member(i.coverage, ['retained', 'partial']) || !member(i.reason, incidentReasons) || !isTime(i.first_observed_at) || !isTime(i.last_observed_at) || Date.parse(i.last_observed_at) < Date.parse(i.first_observed_at)) return;
  if (i.phase && (!member(i.phase, ['inspection', 'indexing']) || i.origin !== 'repository_docs')) return;
  const out = { incident_ref: i.incident_ref, origin: i.origin, collection: i.collection, state: i.state, reason: i.reason, coverage: i.coverage, first_observed_at: i.first_observed_at, last_observed_at: i.last_observed_at, ...refProjection(i) } as Incident;
  if (i.phase) out.phase = i.phase as string;
  for (const key of ['last_success_at', 'retry_after'] as const) if (isTime(i[key])) out[key] = i[key];
  return out;
}
export function eventProjection(value: unknown, summary: ServiceSummary): EventRecord | undefined {
  const e = records(value);
  if (!Object.hasOwn(summary.event_templates || {}, String(e.code))) return;
  const template = summary.event_templates?.[String(e.code)];
  if (!validRefs(e) || !template || !isID(e.event_id) || !isID(e.boot_id) || !isTime(e.occurred_at) || !count(e.sequence) || !e.sequence || e.schema !== 1 || e.message !== template.message || e.severity !== template.severity || e.component !== template.component || (e.stream !== template.stream && !(e.legacy === true && member(e.stream, ['stdout', 'stderr'])))) return;
  const out = { schema: 1, event_id: e.event_id, boot_id: e.boot_id, occurred_at: e.occurred_at, sequence: e.sequence, code: String(e.code), stream: String(e.stream), severity: template.severity, component: template.component, message: template.message, ...refProjection(e) } as EventRecord;
  for (const key of eventDetailKeys) if (member(e[key], summary.event_detail_values?.[key] || summary.event_filters?.[key] || [])) out[key] = e[key];
  if (count(e.attempt) && e.attempt <= 1024) out.attempt = e.attempt;
  if (e.legacy === true) { out.legacy = true; if (count(e.observed_bytes) && e.observed_bytes <= 65536) out.observed_bytes = e.observed_bytes; }
  return out;
}

// Copy is a new allowlisted DTO, never JSON.stringify(snapshot/response/error).
export function safeObservationContext(summary: ServiceSummary, page: EventPage | undefined, query: ServiceQuery): string {
  const o = summary.observation_health, h = summary.execution_health;
  const out: Record<string, unknown> = { schema: 1, protocol: observationProtocol, observed_at: isTime(summary.observed_at) ? summary.observed_at : undefined };
  if (!out.observed_at || summary.protocol !== observationProtocol) throw new ObservationError('unavailable');
  for (const [key, value] of Object.entries({ boot_id: summary.collector.boot_id, generation: summary.collector.generation, metrics_boot_id: summary.metrics_boot_id })) { if (value && !isID(value)) throw new ObservationError('unavailable'); if (value) out[key] = value; }
  const states: Array<[string, unknown, string[], string]> = [
    ['execution_state', h.state, ['healthy', 'degraded', 'unknown'], 'unknown'], ['execution_coverage', h.coverage, ['retained', 'partial', 'unavailable'], 'unavailable'],
    ['observation_state', o.state, ['ready', 'initializing', 'partial', 'unavailable'], 'unavailable'], ['ledger_state', o.ledger_state, ['ready', 'initializing', 'partial', 'unavailable'], 'unavailable'],
    ['output_state', o.output_state, ['managed', 'migration_required', 'legacy_unbounded', 'unsupported', 'unavailable'], 'unavailable'], ['metrics_state', o.metrics_state, ['ready', 'initializing', 'partial', 'unavailable'], 'unavailable']
  ];
  for (const [key, value, choices, fallback] of states) out[key] = member(value, choices) ? value : fallback;
  for (const [key, value] of Object.entries({ dropped_events: o.dropped_events, loss_epoch: o.loss_epoch, dropped_metrics: o.dropped_metrics })) { if (!count(value)) throw new ObservationError('unavailable'); out[key] = value; }
  if (query.event_id && query.incident_ref) throw new ObservationError('invalid_query');
  if (query.event_id) {
    const events = page?.events.filter(e => e.event_id === query.event_id) || [];
    const selected = events.length === 1 ? eventProjection(events[0], summary) : undefined;
    if (!selected) throw new ObservationError('not_retained'); out.event = selected;
  }
  if (query.incident_ref) {
    const incidents = h.incidents.filter(i => i.incident_ref === query.incident_ref);
    const selected = incidents.length === 1 ? incidentProjection(incidents[0]) : undefined;
    if (!selected) throw new ObservationError('not_retained'); out.incident = selected;
  }
  const text = JSON.stringify(out, null, 2);
  if (new TextEncoder().encode(text).length > 16384) throw new ObservationError('response_limit');
  return text;
}

export type ReadState = 'unsupported' | 'session_required' | 'cursor_expired' | 'invalid_cursor' | 'invalid_query' | 'busy' | 'timeout' | 'unavailable' | 'response_limit' | 'not_retained';
export class ObservationError extends Error { constructor(public state: ReadState) { super(state); } }
export const readStateText: Record<ReadState, string> = { unsupported: 'Unsupported by this daemon. Update browser assets and daemon together.', session_required: 'Admin session required. Open a fresh admin link.', cursor_expired: 'Cursor expired or observation reset. Restart the retained timeline explicitly.', invalid_cursor: 'Invalid cursor. Restart the retained timeline explicitly.', invalid_query: 'Unsupported filter combination. Clear or change the filters.', busy: 'Observation is busy. Retry the bounded read.', timeout: 'Observation read timed out. No execution action was taken.', unavailable: 'Observation unavailable. Existing execution state is not inferred.', response_limit: 'Observation exceeds the response limit. Narrow the selection.', not_retained: 'Selection not retained. No fallback entity was selected.' };
export async function boundedObservationRead<T>(path: string, maxBytes: number, parent: AbortSignal, fetcher: typeof fetch = fetch): Promise<T> {
  const controller = new AbortController(); const abort = () => controller.abort(); parent.addEventListener('abort', abort, { once: true });
  if (parent.aborted) controller.abort();
  const timer = setTimeout(abort, 4000);
  try {
    for (let attempt = 0; attempt < 5; attempt++) {
      if (controller.signal.aborted) throw new ObservationError('timeout');
      const response = await fetcher(path, { method: 'GET', signal: controller.signal, cache: 'no-store' });
      if (response.status === 404 || response.status === 405 || response.status === 501) throw new ObservationError('unsupported');
      if (response.status === 401 || response.status === 403) throw new ObservationError('session_required');
      // Older daemons route unknown API paths to the HTML SPA with status 200.
      // This is a compatibility boundary, not a failed observation payload.
      if (response.ok && /^text\/html(?:;|$)/i.test(response.headers.get('content-type') || '')) {
        await response.body?.cancel().catch(() => {});
        throw new ObservationError('unsupported');
      }
      const reader = response.body?.getReader(); if (!reader) throw new ObservationError('unavailable');
      let bytes = 0; const chunks: Uint8Array[] = [];
      try { while (true) { const { value, done } = await reader.read(); if (done) break; bytes += value.length; if (bytes > maxBytes) throw new ObservationError('response_limit'); chunks.push(value); } }
      finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
      const data = new Uint8Array(bytes); let offset = 0; for (const chunk of chunks) { data.set(chunk, offset); offset += chunk.length; }
      const parsed = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data));
      if (!response.ok) {
        const code = records(records(parsed).error).code;
        // Nonblocking local locks can reject concurrent summary/events reads.
        // Retry only their typed 503, preserving query/cursor and this one shared
        // deadline. No execution action or cursor-discontinuity retry is allowed.
        if (response.status === 503 && code === 'observation_busy' && attempt < 4) {
          await new Promise<void>((resolve, reject) => {
            const signal = controller.signal;
            const cancelled = () => { clearTimeout(wait); signal.removeEventListener('abort', cancelled); reject(new ObservationError('timeout')); };
            const wait = setTimeout(() => { signal.removeEventListener('abort', cancelled); resolve(); }, 10);
            signal.addEventListener('abort', cancelled, { once: true });
            if (signal.aborted) cancelled();
          });
          continue;
        }
        const state: ReadState = code === 'snapshot_required' ? 'cursor_expired' : code === 'invalid_cursor' ? 'invalid_cursor' : code === 'invalid_query' ? 'invalid_query' : code === 'observation_busy' ? 'busy' : code === 'observation_timeout' || response.status === 504 ? 'timeout' : code === 'response_limit' ? 'response_limit' : 'unavailable';
        throw new ObservationError(state);
      }
      return parsed as T;
    }
    throw new ObservationError('busy'); // finite retry budget; never silent success
  } catch (error) { if (error instanceof ObservationError) throw error; throw new ObservationError(controller.signal.aborted ? 'timeout' : 'unavailable'); }
  finally { clearTimeout(timer); parent.removeEventListener('abort', abort); }
}
export function observationFreshness(at: string, received: number, now: number): 'missing' | 'stale' | 'current' { return !isTime(at) || !received ? 'missing' : now - received > 30000 || now - Date.parse(at) > 45000 ? 'stale' : 'current'; }
export function metricValue(value: unknown): string { return finite(value) ? new Intl.NumberFormat('en', { maximumFractionDigits: 3 }).format(value) : 'Not observed'; }
