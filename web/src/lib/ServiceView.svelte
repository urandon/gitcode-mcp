<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Activity, AlertTriangle, Clipboard, RefreshCw } from '@lucide/svelte';
  import { humanize, type ObservationSnapshot } from './admin';
  import StatusChip from './StatusChip.svelte';
  import { boundedObservationRead, eventProjection, eventRequest, filterKeys, incidentProjection, isID, isTime, metricValue, observationFreshness, observationProtocol, observationTarget, ObservationError, publicEnum, readStateText, refProjection, resetPage, safeObservationContext, type EventPage, type EventRecord, type Incident, type MetricSnapshot, type ObservationTarget, type ReadState, type ServiceQuery, type ServiceSummary } from './observation';

  export let ready = false;
  export let bootstrapFailed = false;
  export let snapshot: ObservationSnapshot;
  export let query: ServiceQuery = {};
  export let invalidQuery = false;
  export let invalidation = 0;
  export let connection: 'connecting' | 'connected' | 'disconnected' = 'connecting';
  export let now = Date.now();
  export let onquery: (query: ServiceQuery) => void;
  export let onnavigate: (target: ObservationTarget) => void;

  let summary: ServiceSummary | undefined;
  let metrics: MetricSnapshot | undefined;
  let page: EventPage | undefined;
  let summaryError: ReadState | undefined;
  let metricsError: ReadState | undefined;
  let eventsError: ReadState | undefined;
  let reading = false;
  let received = 0;
  let metricsReceived = 0;
  let eventReceived = 0;
  let controller: AbortController | undefined;
  let epoch = 0;
  let disposed = false;
  let pendingInvalidation = false;
  let queryKey = '';
  let seenInvalidation = 0;
  let frozen = false;
  let discontinuity = '';
  let copyState = '';
  let seriesIndex = 0;
  let since = '';
  let until = '';
  let timeError = false;
  let events: EventRecord[] = [];
  let incidents: Incident[] = [];
  $: events = summary && page ? page.events.slice(0, 200).flatMap(e => { const projected = eventProjection(e, summary!); return projected ? [projected] : []; }) : [];
  $: incidents = summary ? summary.execution_health.incidents.slice(0, 128).flatMap(i => { const projected = incidentProjection(i); return projected ? [projected] : []; }) : [];
  $: projectionLoss = Boolean(summary && ((page && events.length !== page.events.length) || incidents.length !== summary.execution_health.incidents.length));
  $: selected = query.event_id ? events.find(e => e.event_id === query.event_id) : query.incident_ref ? incidents.find(i => i.incident_ref === query.incident_ref) : undefined;
  $: selectionMissing = Boolean((query.event_id || query.incident_ref) && !reading && !selected && summary && !eventsError && !summaryError);
  $: selectedTarget = selected ? observationTarget(snapshot, refProjection(selected)) : undefined;
  $: metricSeries = metrics?.series[seriesIndex];
  $: metricDefinition = metrics?.catalog.find(d => d.name === metricSeries?.name);
  $: summaryFreshness = observationFreshness(summary?.observed_at || '', received, now);
  $: metricFreshness = observationFreshness(metrics?.sampled_at || '', metricsReceived, now);
  $: if (ready && JSON.stringify(query) !== queryKey) {
    queryKey = JSON.stringify(query); since = query.since || ''; until = query.until || '';
    frozen = false; copyState = ''; timeError = false;
    void load(true);
  }
  $: if (ready && invalidation !== seenInvalidation) {
    seenInvalidation = invalidation;
    if (reading) pendingInvalidation = true; else if (!frozen) void load(false);
  }

  function shapeSummary(s: ServiceSummary): boolean {
    return s?.schema === 1 && s.protocol === observationProtocol && s.admin_observation_version === '1' && Boolean(s.event_filters && s.event_templates && s.collector?.coverage && s.observation_health && s.execution_health && Array.isArray(s.execution_health.incidents) && s.execution_health.incidents.length <= 128 && Array.isArray(s.resources) && s.resources.length <= 9);
  }
  async function load(selectionChanged: boolean): Promise<void> {
    if (!ready || disposed) return;
    if (reading && !selectionChanged) { pendingInvalidation = true; return; }
    controller?.abort(); controller = new AbortController(); const signal = controller.signal; const token = ++epoch;
    reading = true; pendingInvalidation = false; copyState = '';
    if (selectionChanged) { page = undefined; eventsError = undefined; eventReceived = 0; }
    const captured = { ...query };
    const retain = () => !disposed && token === epoch && !signal.aborted;
    const state = (e: unknown) => e instanceof ObservationError ? e.state : 'unavailable' as ReadState;
    await Promise.all([
      boundedObservationRead<ServiceSummary>('/api/admin/v1/observability', 128 << 10, signal).then(s => {
        if (!retain()) return;
        if (!shapeSummary(s)) throw new ObservationError(s?.protocol === observationProtocol ? 'unsupported' : 'unavailable');
        const old = summary;
        if (old && (s.collector.boot_id !== old.collector.boot_id || s.collector.generation !== old.collector.generation || s.observation_health.loss_epoch !== old.observation_health.loss_epoch)) {
          discontinuity = 'Observation boundary changed: boot, retention generation or event loss. This is not continuous history.';
          if (captured.cursor || captured.event_id) frozen = true;
        }
        summary = s; received = Date.now(); summaryError = undefined;
      }).catch(e => { if (retain()) { summaryError = state(e); } }),
      boundedObservationRead<MetricSnapshot>('/api/admin/v1/observability/metrics?history_minutes=60', 4 << 20, signal).then(m => {
        if (!retain()) return;
        if (!Array.isArray(m?.catalog) || m.catalog.length > 22 || !Array.isArray(m.series) || m.series.length > 256 || m.series.some(s => !Array.isArray(s.history) || s.history.length > 60)) throw new ObservationError('unavailable');
        if (metrics && m.boot_id !== metrics.boot_id) discontinuity = 'Metrics reset at a new boot. Rates do not cross this boundary.';
        metrics = m; metricsReceived = Date.now(); metricsError = undefined;
        if (seriesIndex >= m.series.length) seriesIndex = 0;
      }).catch(e => { if (retain()) metricsError = state(e); }),
      boundedObservationRead<EventPage>(eventRequest(captured), 64 << 10, signal).then(p => {
        if (!retain()) return;
        if (!Array.isArray(p?.events) || p.events.length > 200 || !p.coverage || typeof p.has_more !== 'boolean' || typeof p.cursor !== 'string' || (p.cursor && !/^[A-Za-z0-9_-]{1,512}$/.test(p.cursor))) throw new ObservationError('unavailable');
        page = p; eventReceived = Date.now(); eventsError = undefined;
      }).catch(e => { if (retain()) { eventsError = state(e); if (eventsError === 'cursor_expired' || eventsError === 'invalid_cursor') { frozen = true; page = undefined; } } })
    ]);
    if (!retain()) return;
    reading = false;
    if (pendingInvalidation && !frozen) { pendingInvalidation = false; void load(false); }
  }
  function changeFilter(key: typeof filterKeys[number], value: string): void {
    const next = resetPage(query); if (value) next[key] = value; else delete next[key]; onquery(next);
  }
  function selectEvent(id: string): void { const next = { ...query, event_id: id }; delete next.incident_ref; onquery(next); }
  function selectIncident(id: string): void { const next = { ...query, incident_ref: id }; delete next.event_id; onquery(next); }
  function nextPage(): void { if (!page?.has_more || !page.cursor || frozen) return; onquery({ ...resetPage(query), cursor: page.cursor }); }
  function restart(): void { frozen = false; discontinuity = 'Retained timeline restarted explicitly; earlier history may no longer be retained.'; const next = resetPage(query); if (JSON.stringify(next) === JSON.stringify(query)) void load(true); else onquery(next); }
  function applyTime(): void {
    timeError = !isTime(since) || !isTime(until) || Date.parse(until) < Date.parse(since) || Date.parse(until) - Date.parse(since) > 7 * 86400000;
    if (!timeError) onquery({ ...resetPage(query), since, until });
  }
  async function copy(): Promise<void> {
    if (!summary || summaryError || eventsError || reading || frozen || selectionMissing) { copyState = 'Context unavailable for the current selection.'; return; }
    try { await navigator.clipboard.writeText(safeObservationContext(summary, page, query)); copyState = 'Safe context copied (schema 1, at most 16 KiB).'; }
    catch (e) { copyState = e instanceof ObservationError ? readStateText[e.state] : 'Clipboard unavailable. No unsanitized fallback was copied.'; }
  }
  function labelSeries(s: NonNullable<MetricSnapshot['series']>[number]): string {
    const d = metrics?.catalog.find(d => d.name === s.name);
    const labels = Object.entries(s.labels || {}).filter(([k,v]) => d?.dimensions[k]?.includes(v)).map(([k,v]) => `${publicEnum(k)}: ${publicEnum(v)}`).join(', ');
    return `${humanize(publicEnum(s.name))}${labels ? ` · ${labels}` : ''}`;
  }
  function time(value?: string): string { return isTime(value) ? new Date(value).toISOString() : 'Not observed'; }
  onDestroy(() => { disposed = true; epoch++; controller?.abort(); });
</script>

<div class="intro section-intro"><p class="eyebrow">BOUNDED LOCAL OBSERVATION</p><h1>Service</h1><p>Execution health, sampled resources and retained catalog events. Reads never sync, probe credentials, install, retry or repair work.</p></div>
<div class="actions"><button class="secondary" onclick={() => void load(false)} disabled={!ready || reading}><RefreshCw size={16} />{reading ? 'Refreshing observation…' : 'Refresh observation'}</button><button class="secondary" onclick={() => void copy()} disabled={!summary || Boolean(summaryError || eventsError) || reading || frozen || selectionMissing}><Clipboard size={16} />Copy safe context</button></div>
{#if bootstrapFailed}<p role="alert">Admin session unavailable. Open a fresh admin link. No observation or execution action was taken.</p>{:else if !ready || (reading && !summary)}<p role="status">Loading bounded service observation…</p>{/if}
{#if invalidQuery}<p role="status">Invalid URL selectors were discarded. No untrusted selector was sent.</p>{/if}
{#if connection !== 'connected'}<p class="service-notice" role="status">{connection === 'disconnected' ? 'Live observation disconnected. Last returned data is retained; freshness is independent of execution health.' : 'Live observation connecting.'}</p>{/if}
{#if copyState}<p role="status">{copyState}</p>{/if}
{#if discontinuity}<p class="service-notice" role="status">{discontinuity}</p>{/if}
{#if projectionLoss}<p class="service-notice" role="status">Some returned records did not match the reviewed observation catalog and were omitted. Display coverage is partial; no raw record was shown.</p>{/if}
{#if summaryError}<p class="service-notice" role="alert">Summary · {readStateText[summaryError]}</p>{/if}
{#if summary}
  <section class="service-card" aria-labelledby="service-health-title">
    <div class="section-heading"><div><p class="section-kicker">INDEPENDENT HEALTH</p><h2 id="service-health-title">Execution and observation</h2></div><StatusChip value={summaryFreshness} label={`Summary ${summaryFreshness}`} /></div>
    <dl class="service-grid"><div><dt>Execution health</dt><dd><StatusChip value={publicEnum(summary.execution_health.state)} /></dd><small>{humanize(publicEnum(summary.execution_health.coverage))} authority · {humanize(publicEnum(summary.execution_health.reason))}</small></div><div><dt>Observation health</dt><dd><StatusChip value={publicEnum(summary.observation_health.state)} /></dd><small>Ledger {humanize(publicEnum(summary.observation_health.ledger_state))} · Metrics {humanize(publicEnum(summary.observation_health.metrics_state))}</small></div><div><dt>Boot identity</dt><dd><code>{isID(summary.collector.boot_id) ? summary.collector.boot_id : 'Not observed'}</code></dd><small>Metrics reset {time(summary.metrics_reset_at)}</small></div><div><dt>Observed boots (retained)</dt><dd>{metricValue(summary.collector.coverage.observed_boots)}</dd><small>Not an operating-system restart count.</small></div><div><dt>Observation loss</dt><dd>{metricValue(summary.observation_health.dropped_events)} events · {metricValue(summary.observation_health.dropped_metrics)} metrics</dd><small>Loss epoch {metricValue(summary.observation_health.loss_epoch)} · Output {humanize(publicEnum(summary.observation_health.output_state))}</small></div><div><dt>Last returned summary</dt><dd>{time(summary.observed_at)}</dd><small>Metrics sampled {time(summary.metrics_sampled_at)}</small></div></dl>
    <p>Healthy execution does not imply complete logs or fresh metrics. Observation loss does not imply failed execution.</p>
  </section>
  <section class="service-card" aria-labelledby="resources-title"><div class="section-heading"><div><p class="section-kicker">SAMPLED GAUGES</p><h2 id="resources-title">Queue and runtime resources</h2></div><StatusChip value={metricFreshness} label={`Samples ${metricFreshness}`} /></div><dl class="service-grid">{#each summary.resources as resource}<div><dt>{humanize(publicEnum(resource.name))}</dt><dd>{metricValue(resource.value)}</dd><small>{resource.value === null ? 'Missing probe, not zero.' : publicEnum(metrics?.catalog.find(d => d.name === resource.name)?.unit)}</small></div>{/each}</dl>{#if !summary.resources.length}<p>No resource probes retained.</p>{/if}</section>
  <section class="service-card" aria-labelledby="incidents-title"><div class="section-heading"><div><p class="section-kicker">DURABLE AUTHORITY</p><h2 id="incidents-title">Active and recovered incidents</h2></div><span>{metricValue(summary.execution_health.active)} active · {metricValue(summary.execution_health.recovered)} recovered</span></div><p>Inspection availability and indexing readiness are separate phases. Indexing success cannot erase an inspection failure. Retry admission is an actual attempt, not a scheduled timer.</p>
    {#if !incidents.length}<p>{summary.execution_health.coverage === 'unavailable' ? 'Incident authority unavailable; health is unknown.' : 'No incident retained in this bounded authority projection.'}</p>{/if}
    <div class="service-list">{#each incidents as incident}<article class:chosen={query.incident_ref === incident.incident_ref}><div class="section-heading"><button class="text-action" onclick={() => selectIncident(incident.incident_ref)} aria-pressed={query.incident_ref === incident.incident_ref}>{humanize(incident.origin)} · {humanize(incident.collection)}{incident.phase ? ` · ${humanize(incident.phase)}` : ''}</button><StatusChip value={incident.state} /></div><p>{humanize(incident.reason)} · {humanize(incident.coverage)} coverage</p><dl class="service-grid"><div><dt>First observed</dt><dd>{time(incident.first_observed_at)}</dd></div><div><dt>Last observed</dt><dd>{time(incident.last_observed_at)}</dd></div><div><dt>Last success</dt><dd>{time(incident.last_success_at)}</dd></div><div><dt>Retry scheduled for</dt><dd>{time(incident.retry_after)}</dd></div></dl></article>{/each}</div>
    {#if summary.execution_health.omitted}<p>Additional incidents omitted by the bounded projection. Coverage is partial.</p>{/if}
  </section>
{/if}
<section class="service-card" aria-labelledby="metrics-title"><div class="section-heading"><div><p class="section-kicker">PER-BOOT · 60 MINUTES MAXIMUM</p><h2 id="metrics-title">Metric trends</h2></div><Activity size={18} /></div>
  {#if metricsError}<p role="alert">Metrics · {readStateText[metricsError]}</p>{/if}
  {#if metrics}<p>Sample {time(metrics.sampled_at)} · Reset {time(metrics.reset_at)} · {humanize(publicEnum(metrics.state))}. Missing intervals remain missing; rates do not span resets.</p>
    {#if metrics.series.length}<label class="service-select">Metric series<select bind:value={seriesIndex}>{#each metrics.series as series, i}<option value={i}>{labelSeries(series)}</option>{/each}</select></label>
      {#if metricSeries && metricDefinition}<dl class="service-grid"><div><dt>Origin / reset / window</dt><dd>{humanize(publicEnum(metricDefinition.origin))} · {publicEnum(metricDefinition.reset)} · {metricDefinition.window === '15s' ? '15s' : 'boot'}</dd></div><div><dt>Type / unit</dt><dd>{publicEnum(metricDefinition.type)} · {publicEnum(metricDefinition.unit)}</dd></div><div><dt>Current observation</dt><dd>{metricDefinition.type === 'histogram' ? `${metricValue(metricSeries.value === null ? null : (metricSeries.count ?? 0))} observations · sum ${metricValue(metricSeries.value === null ? null : (metricSeries.sum ?? 0))}` : metricValue(metricSeries.value)}</dd></div></dl>
        {#if metricDefinition.type === 'histogram'}<p>Non-cumulative duration buckets: {(metricDefinition.bounds || []).map(b => metricValue(b)).join(', ')} seconds + overflow. Counts and durations do not assert success.</p>{/if}
        <div class="table-wrap"><table><caption>Minute buckets · {humanize(publicEnum(metricSeries.name))}</caption><thead><tr><th>UTC minute</th><th>Samples</th><th>Min / max</th><th>Delta / rate</th><th>Count / sum</th><th>Coverage</th></tr></thead><tbody>{#each metricSeries.history as bucket}<tr><th scope="row">{time(bucket.start)}</th><td>{metricValue(bucket.samples)}</td><td>{metricValue(bucket.min)} / {metricValue(bucket.max)}</td><td>{metricValue(bucket.delta)} / {metricValue(bucket.rate)}</td><td>{bucket.samples ? `${metricValue(bucket.count)} / ${metricValue(bucket.sum)}` : 'Not observed'}</td><td>{bucket.complete ? 'Complete interval' : 'Partial / missing interval'}</td></tr>{/each}</tbody></table></div>
      {/if}
    {:else}<p>No metric series observed. This is not a zero-valued history.</p>{/if}
  {/if}
</section>
<section class="service-card" aria-labelledby="timeline-title"><div class="section-heading"><div><p class="section-kicker">ONE RETAINED PAGE · 50 EVENTS</p><h2 id="timeline-title">Event timeline</h2></div><button class="secondary" onclick={restart} disabled={!ready || reading}>Restart retained timeline</button></div>
  {#if summary}<div class="service-filters" aria-label="Event filters">{#each filterKeys as key}<label>{humanize(key)}<select value={query[key] || ''} onchange={event => changeFilter(key, event.currentTarget.value)}><option value="">All</option>{#each (summary.event_filters?.[key] || []) as value}<option value={value}>{humanize(publicEnum(value))}</option>{/each}</select></label>{/each}</div>{/if}
  <div class="service-filters"><label>Since (UTC RFC3339)<input bind:value={since} placeholder="2026-01-01T00:00:00Z" maxlength="30" /></label><label>Until (UTC RFC3339)<input bind:value={until} placeholder="2026-01-02T00:00:00Z" maxlength="30" /></label><button class="secondary" onclick={applyTime}>Apply time window</button><button class="secondary" onclick={() => onquery({})}>Clear filters and selection</button></div>
  {#if timeError}<p role="alert">Supply two UTC timestamps, ordered and at most seven days apart.</p>{/if}
  {#if Object.keys(refProjection(query)).length}<dl class="service-grid">{#each Object.entries(refProjection(query)) as [key, value]}<div><dt>{humanize(key)}</dt><dd><code>{value}</code></dd></div>{/each}</dl>{/if}
  {#if eventsError}<p role="alert">Timeline · {readStateText[eventsError]}</p>{/if}
  {#if page}<p>Ledger {humanize(publicEnum(page.coverage.state))} · {metricValue(page.coverage.dropped)} dropped · loss epoch {metricValue(page.coverage.loss_epoch)}. Returned {eventReceived ? new Date(eventReceived).toISOString() : 'Not observed'}.</p>
    {#if !events.length}<p>{page.has_more ? 'No match in this scanned page. More retained records remain to scan.' : 'No matching event retained. This is a valid empty result, not proof of complete history.'}</p>{/if}
    <ol class="service-list" aria-label="Retained catalog events">{#each events as event}<li class:chosen={query.event_id === event.event_id}><div class="section-heading"><button class="text-action" onclick={() => selectEvent(event.event_id)} aria-pressed={query.event_id === event.event_id}>{humanize(event.code)}</button><StatusChip value={event.severity} /></div><p>{event.message}</p><small>{time(event.occurred_at)} · {event.stream} · {event.component} · sequence {event.sequence}</small>{#if event.legacy}<p>Legacy output omitted; no raw text is retained or copied here.</p>{/if}</li>{/each}</ol>
    <button class="secondary" onclick={nextPage} disabled={!page.has_more || !page.cursor || frozen || reading}>{page.has_more ? 'Continue retained scan' : 'End of retained scan'}</button>
  {/if}
  {#if frozen}<p role="status">Continuation stopped at a discontinuity. Restart explicitly; no automatic skip was made.</p>{/if}
</section>
{#if query.event_id || query.incident_ref}<section class="service-card" aria-label="Selected observation"><h2>Selected observation</h2>{#if selectionMissing}<p role="status">Selection not retained. No fallback entity was selected.</p>{:else if selected}<p>{'code' in selected ? humanize(String(selected.code)) : humanize(selected.reason)}</p>{#if selectedTarget}<button class="secondary" onclick={() => selectedTarget && onnavigate(selectedTarget)}>Open exact {selectedTarget.kind === 'source' ? 'documentation source' : selectedTarget.kind}</button>{:else}<p>Entity authority unavailable or ambiguous. No arbitrary locator or sibling was resolved.</p>{/if}{/if}</section>{/if}

<style>
  .service-card { border: 1px solid var(--line); border-radius: var(--radius-card); padding: 24px; margin: 20px 0; background: var(--surface); min-width: 0; }
  .service-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(220px, 100%), 1fr)); gap: 16px; margin: 20px 0; }
  dt, small { color: var(--muted); font-size: 12px; }
  dd { margin: 6px 0; overflow-wrap: anywhere; }
  .service-filters { display: flex; align-items: end; flex-wrap: wrap; gap: 12px; margin: 16px 0; }
  label, .service-select { display: flex; flex-direction: column; gap: 8px; font-size: 12px; color: var(--muted); min-width: 0; }
  select, input { max-width: 100%; min-height: 40px; }
  .service-list { padding: 0; list-style: none; display: grid; gap: 12px; }
  .service-list li, .service-list article { padding: 16px; border: 1px solid var(--line); border-radius: var(--radius-card); min-width: 0; }
  .chosen { outline: 2px solid var(--accent); outline-offset: 2px; }
  .service-notice { padding: 14px; border-left: 3px solid var(--warning); background: var(--surface); }
  p, small, code { overflow-wrap: anywhere; }
  .section-heading { flex-wrap: wrap; gap: 12px; }
  @media (max-width: 600px) { .service-card { padding: 16px; } .service-filters label { width: 100%; } .service-grid { grid-template-columns: 1fr; } }
</style>
