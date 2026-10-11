import { describe, expect, it, vi } from 'vitest';
import { emptySnapshot } from './admin';
import { boundedObservationRead, eventProjection, eventRequest, incidentProjection, metricValue, observationFreshness, observationTarget, readServiceQuery, safeObservationContext, writeServiceQuery } from './observation';
import { id, observationFixture, ref } from '../../tests/fixtures/observation';

describe('bounded observation browser contract', () => {
  it('round trips only bounded opaque URL selectors and never echoes rejected coordinates', () => {
    const params = new URLSearchParams({ obs_job_ref: ref('job', '5'), obs_code: 'job_admitted', obs_cursor: 'e30', obs_event_id: id('e'), other: 'private-coordinate' });
    const parsed = readServiceQuery(params); expect(parsed.invalid).toBe(false);
    const out = new URLSearchParams(); writeServiceQuery(out, parsed.query); expect(out.get('other')).toBeNull();
    expect(eventRequest(parsed.query)).toContain(`job_ref=${ref('job', '5')}`); expect(eventRequest(parsed.query)).not.toContain('event_id');
    for (const bad of ['obs_job_ref=private-coordinate', `obs_job_ref=${ref('repo', '5')}`, 'obs_code=token%3Dprivate', `obs_cursor=${'x'.repeat(513)}`, 'obs_since=2026-01-01T00%3A00%3A00Z', 'obs_since=2026-01-01T00%3A00%3A00Z&obs_until=2026-02-01T00%3A00%3A00Z', 'obs_code=daemon_boot&obs_code=job_admitted']) {
      const parsed = readServiceQuery(new URLSearchParams(bad)); expect(parsed.invalid).toBe(true); expect(eventRequest(parsed.query)).not.toContain('private');
    }
  });
  it('does not conflate absent gauges or incomplete samples with zero', () => {
    expect(metricValue(null)).toBe('Not observed'); expect(metricValue(0)).toBe('0'); expect(metricValue(undefined)).toBe('Not observed');
    expect(observationFreshness('0001-01-01T00:00:00Z', 100, 100)).toBe('missing');
    const at = '2026-01-01T00:00:00Z'; const now = Date.parse(at);
    expect(observationFreshness(at, now, now + 31000)).toBe('stale');
    expect(observationFreshness(at, now + 50000, now + 50000)).toBe('stale');
    expect(observationFreshness(at, now, now + 100)).toBe('current');
  });
  it('resolves exact sources only and refuses disappearance or ambiguity', () => {
    const s: any = structuredClone(emptySnapshot); const refs = { source_ref: ref('source', '1'), registration_ref: ref('registration', '2'), cache_ref: ref('cache', '3'), repo_ref: ref('repo', '4') };
    s.caches = [{ cache_ref: 'cache-public', observation_refs: { cache_ref: refs.cache_ref }, repositories: [{ repo_id: 'example/repo', observation_refs: { cache_ref: refs.cache_ref, repo_ref: refs.repo_ref }, documentation: { sources: [{ source_registration_id: 'source-public-a', observation_refs: refs }, { source_registration_id: 'source-public-b', observation_refs: { ...refs, source_ref: ref('source', '6') } }] } }] }];
    expect(observationTarget(s, refs)).toEqual({ kind: 'source', cache: 'cache-public', repo: 'example/repo', source: 'source-public-a' });
    s.caches[0].repositories[0].documentation.sources.shift(); expect(observationTarget(s, refs)).toBeUndefined();
    const source = { source_registration_id: 'source-public-a', observation_refs: refs }; s.caches[0].repositories[0].documentation.sources.push(source, source); expect(observationTarget(s, refs)).toBeUndefined();
  });
  it('copies a fresh allowlist with one returned selection, never broad/raw state', () => {
    const { summary, page } = observationFixture();
    (summary as any).raw_body = 'synthetic-private-secret'; (page.events[0] as any).credential = 'synthetic-private-secret';
    const copy = JSON.parse(safeObservationContext(summary, page, { event_id: id('e') }));
    expect(copy.schema).toBe(1); expect(copy.protocol).toBe('observability.v1'); expect(copy.event.event_id).toBe(id('e'));
    expect(JSON.stringify(copy)).not.toContain('synthetic-private-secret'); expect(copy.execution_state).toBe('degraded'); expect(copy.observation_state).toBe('partial');
    expect(new TextEncoder().encode(JSON.stringify(copy)).length).toBeLessThanOrEqual(16384);
    expect(() => safeObservationContext(summary, page, { event_id: id('f') })).toThrow('not_retained');
    expect(() => safeObservationContext(summary, page, { event_id: id('e'), incident_ref: ref('correlation', 'c') })).toThrow('invalid_query');
    page.events[0].message = 'synthetic-private-secret'; expect(eventProjection(page.events[0], summary)).toBeUndefined(); expect(() => safeObservationContext(summary, page, { event_id: id('e') })).toThrow('not_retained');
    page.events[0].message = 'A new job was durably admitted.'; page.events[0].source_ref = 'synthetic-private-secret'; expect(eventProjection(page.events[0], summary)).toBeUndefined();
    summary.execution_health.incidents[0].reason = 'synthetic-private-secret'; expect(incidentProjection(summary.execution_health.incidents[0])).toBeUndefined();
  });
  it('isolates inspection failure from recovered indexing and keeps authority timestamps', () => {
    const { summary } = observationFixture(); const context = JSON.parse(safeObservationContext(summary, undefined, { incident_ref: ref('correlation', 'c') }));
    expect(context.incident.phase).toBe('inspection'); expect(context.incident.state).toBe('active'); expect(context.incident.first_observed_at).toBe(summary.execution_health.incidents[0].first_observed_at);
    expect(context.execution_state).toBe('degraded'); expect(context.incident.source_ref).toBe(ref('source', '1'));
  });
  it('bounds streaming response bytes and maps provider text to fixed errors', async () => {
    const signal = new AbortController().signal;
    await expect(boundedObservationRead('/local', 16, signal, vi.fn().mockResolvedValue(new Response('x'.repeat(17))))).rejects.toThrow('response_limit');
    await expect(boundedObservationRead('/local', 1024, signal, vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'snapshot_required', reason: 'synthetic-private-secret' } }), { status: 409 })))).rejects.toThrow('cursor_expired');
    await expect(boundedObservationRead('/local', 1024, signal, vi.fn().mockResolvedValue(new Response('synthetic-private-secret', { status: 404 })))).rejects.toThrow('unsupported');
  });
  it('cancels bounded reads at a browser deadline and joins timer/listener ownership', async () => {
    vi.useFakeTimers();
    try {
      const fetcher: any = (_: string, options: RequestInit) => new Promise((_, reject) => options.signal?.addEventListener('abort', () => reject(new Error('synthetic-private-secret'))));
      const read = boundedObservationRead('/local', 16, new AbortController().signal, fetcher); const check = expect(read).rejects.toThrow('timeout');
      await vi.advanceTimersByTimeAsync(4000); await check; expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });
});
