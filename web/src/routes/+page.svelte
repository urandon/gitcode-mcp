<script lang="ts">
  import './+page.css';
  import { onDestroy, onMount } from 'svelte';
  import { Activity, AlertTriangle, ArrowLeft, Blocks, CheckCircle2, ChevronRight, CircleGauge, Clipboard, Clock3, Database, FolderCog, HeartPulse, History, Layers3, Monitor, Moon, RefreshCw, Search, ShieldCheck, Sun, Wrench } from '@lucide/svelte';
  import { applyTheme, normalizeTheme, themeStorageKey, type Theme } from '$lib/theme';
  import { adminApiVersion, cliHandoff, emptySnapshot, humanize, isSnapshotStale, laneSummary, type AdminView, type CacheObservation, type Diagnostic, type ObservationSnapshot, type Repository, type RepositoryTab } from '$lib/admin';
  import CoverageLaneCard from '$lib/CoverageLaneCard.svelte';
  import StatusChip from '$lib/StatusChip.svelte';

  const navigation: Array<{ name: AdminView; icon: typeof CircleGauge }> = [
    { name: 'Overview', icon: CircleGauge }, { name: 'Caches', icon: Database }, { name: 'Jobs', icon: Activity }, { name: 'Maintenance', icon: Wrench }, { name: 'Diagnostics', icon: HeartPulse }
  ];
  const repositoryTabs: Array<{ value: RepositoryTab; label: string }> = [
    { value: 'coverage', label: 'Coverage' }, { value: 'collections', label: 'Collections' }, { value: 'search', label: 'Search status' }, { value: 'activity', label: 'Activity' }
  ];
  const themes = [
    { value: 'light' as Theme, label: 'Light', icon: Sun }, { value: 'dark' as Theme, label: 'Dark', icon: Moon }, { value: 'system' as Theme, label: 'System', icon: Monitor }
  ];
  const activeJobStates = new Set(['queued', 'running']);

  let theme: Theme = 'system';
  let active: AdminView = 'Overview';
  let selectedCacheRef = '';
  let selectedRepoID = '';
  let repoTab: RepositoryTab = 'coverage';
  let diagnosticsFilter: 'current' | 'recovered' | 'all' = 'current';
  let loading = true;
  let error = '';
  let copied = '';
  let snapshot: ObservationSnapshot = structuredClone(emptySnapshot);
  let eventStream: EventSource | undefined;
  let selectedCache: CacheObservation | undefined;
  let selectedRepo: Repository | undefined;
  let activeJobs = snapshot.jobs.filter((job) => activeJobStates.has(job.status));
  let scopedJobs = snapshot.jobs;
  let visibleDiagnostics = snapshot.diagnostics;
  let stale = false;

  $: selectedCache = snapshot.caches.find((cache) => cache.cache_ref === selectedCacheRef);
  $: selectedRepo = selectedCache?.repositories.find((repo) => repo.repo_id === selectedRepoID);
  $: activeJobs = snapshot.jobs.filter((job) => activeJobStates.has(job.status));
  $: scopedJobs = snapshot.jobs.filter((job) => job.cache_ref === selectedCache?.cache_ref && job.repo_id === selectedRepo?.repo_id);
  $: visibleDiagnostics = snapshot.diagnostics.filter((item) => diagnosticsFilter === 'all' || (diagnosticsFilter === 'current' ? item.current : !item.current));
  $: stale = snapshot.revision !== '' && isSnapshotStale(snapshot.generated_at);

  function normalizeSnapshot(value: ObservationSnapshot): ObservationSnapshot {
    value.attention ||= []; value.caches ||= []; value.jobs ||= []; value.maintenance ||= []; value.diagnostics ||= []; value.capabilities ||= [];
    for (const cache of value.caches) for (const repo of (cache.repositories ||= [])) {
      repo.collections ||= []; repo.recent_sync_events ||= []; repo.execution ||= {}; repo.counts.by_kind ||= [];
    }
    return value;
  }

  async function establishSession(): Promise<void> {
    const launchToken = new URLSearchParams(location.hash.slice(1)).get('launch');
    if (!launchToken) return;
    history.replaceState(null, '', location.pathname + location.search);
    const response = await fetch('/api/admin/v1/session', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ launch_token: launchToken }) });
    if (!response.ok) throw new Error('The launch link is invalid or has expired. Run admin open again.');
  }

  async function refresh(): Promise<void> {
    loading = true; error = '';
    try {
      const response = await fetch('/api/admin/v1/snapshot');
      if (!response.ok) throw new Error(response.status === 401 ? 'Admin session required. Run admin open again.' : 'Observation is unavailable.');
      snapshot = normalizeSnapshot(await response.json());
    } catch (value) { error = value instanceof Error ? value.message : 'Observation is unavailable.'; }
    finally { loading = false; }
  }

  function connectEvents(): void {
    eventStream?.close(); eventStream = new EventSource('/api/admin/v1/events');
    for (const kind of ['snapshot_changed', 'snapshot_required']) eventStream.addEventListener(kind, () => void refresh());
  }

  function hydrateLocation(): void {
    const params = new URLSearchParams(location.search);
    const requestedView = params.get('view');
    if (navigation.some((item) => item.name === requestedView)) active = requestedView as AdminView;
    selectedCacheRef = params.get('cache') || ''; selectedRepoID = params.get('repo') || '';
    const requestedTab = params.get('tab');
    if (repositoryTabs.some((item) => item.value === requestedTab)) repoTab = requestedTab as RepositoryTab;
    const requestedFilter = params.get('diagnostics');
    if (requestedFilter === 'current' || requestedFilter === 'recovered' || requestedFilter === 'all') diagnosticsFilter = requestedFilter;
  }

  function updateLocation(replace = false): void {
    const params = new URLSearchParams();
    if (active !== 'Overview') params.set('view', active);
    if (selectedCacheRef) params.set('cache', selectedCacheRef);
    if (selectedRepoID) params.set('repo', selectedRepoID);
    if (selectedRepoID && repoTab !== 'coverage') params.set('tab', repoTab);
    if (active === 'Diagnostics' && diagnosticsFilter !== 'current') params.set('diagnostics', diagnosticsFilter);
    history[replace ? 'replaceState' : 'pushState'](null, '', `${location.pathname}${params.size ? `?${params}` : ''}`);
  }

  function selectView(view: AdminView): void {
    active = view;
    if (view !== 'Caches') { selectedCacheRef = ''; selectedRepoID = ''; }
    updateLocation();
  }
  function openRepository(cache: CacheObservation, repo: Repository): void { active = 'Caches'; selectedCacheRef = cache.cache_ref; selectedRepoID = repo.repo_id; repoTab = 'coverage'; updateLocation(); }
  function closeRepository(): void { selectedRepoID = ''; repoTab = 'coverage'; updateLocation(); }
  function selectRepositoryTab(value: RepositoryTab): void { repoTab = value; updateLocation(); }
  function selectDiagnosticFilter(value: 'current' | 'recovered' | 'all'): void { diagnosticsFilter = value; updateLocation(); }
  async function copyCommand(diagnostic: Diagnostic): Promise<void> {
    try { await navigator.clipboard.writeText(cliHandoff(diagnostic)); copied = diagnostic.id; window.setTimeout(() => (copied = ''), 1800); }
    catch { copied = ''; }
  }
  function selectTheme(value: Theme): void { theme = value; applyTheme(value); }
  function onPopState(): void { hydrateLocation(); }

  onMount(async () => {
    theme = normalizeTheme(localStorage.getItem(themeStorageKey)); applyTheme(theme); hydrateLocation(); window.addEventListener('popstate', onPopState);
    try { await establishSession(); await refresh(); connectEvents(); }
    catch (value) { error = value instanceof Error ? value.message : 'Admin session is unavailable.'; loading = false; }
  });
  onDestroy(() => { eventStream?.close(); window.removeEventListener('popstate', onPopState); });
</script>

<svelte:head><title>gitcode-mcp · Local operator console</title></svelte:head>

<div class="shell">
  <aside class="sidebar">
    <a class="brand" href="/" aria-label="gitcode-mcp overview"><span class="brand-mark"><Blocks size={18} strokeWidth={2.2} /></span><span>gitcode-mcp</span></a>
    <nav aria-label="Admin sections">
      {#each navigation as item}<button class:active={active === item.name} aria-current={active === item.name ? 'page' : undefined} onclick={() => selectView(item.name)} title={item.name}><item.icon size={18} strokeWidth={1.8} /><span>{item.name}</span></button>{/each}
    </nav>
    <div class="theme-control"><span class="theme-label">Theme</span><div class="theme-options" role="radiogroup" aria-label="Color theme">{#each themes as item}<button class:selected={theme === item.value} role="radio" aria-checked={theme === item.value} onclick={() => selectTheme(item.value)}><item.icon size={14} />{item.label}</button>{/each}</div><p>System is the default.</p></div>
  </aside>

  <main>
    <header class="topbar"><div class="crumb"><span>Admin</span><ChevronRight size={14} /><strong>{active}</strong></div><div class="version">v{snapshot.service.version || 'dev'}</div></header>
    <section class="content" aria-busy={loading}>
      {#if snapshot.api_version !== adminApiVersion}
        <section class="state-panel danger-panel" role="alert"><AlertTriangle size={22} /><div><strong>UI/API version mismatch</strong><p>This UI expects API v{adminApiVersion}, but the daemon returned v{snapshot.api_version || 'unknown'}. Update browser assets and daemon together.</p></div></section>
      {:else}
        {#if error}<section class="state-panel danger-panel" role="alert"><AlertTriangle size={22} /><div><strong>Observation unavailable</strong><p>{error}</p></div></section>{/if}
        {#if stale}<section class="state-panel warning-panel" role="status"><Clock3 size={21} /><div><strong>Snapshot may be stale</strong><p>Last observation was {new Date(snapshot.generated_at).toLocaleString()}. Recheck before acting.</p></div></section>{/if}

        {#if active === 'Overview'}
          <div class="intro"><p class="eyebrow">LOCAL ADMIN</p><h1>Local operator console</h1><p>Readiness, coverage, active work, and the next safe action across every managed local cache.</p></div>
          {#if loading && !snapshot.revision}<div class="loading-grid" aria-label="Loading operator snapshot"><span></span><span></span><span></span></div>
          {:else}
            <section class="section-block" aria-labelledby="attention-title">
              <div class="section-heading"><div><p class="section-kicker">NEEDS ATTENTION</p><h2 id="attention-title">Attention queue</h2></div><span class="count-badge">{snapshot.attention.length}</span></div>
              {#if snapshot.attention.length === 0}<div class="empty-inline"><CheckCircle2 size={19} /><div><strong>No current blockers</strong><span>Nothing requires an operator decision in this snapshot.</span></div></div>
              {:else}<div class="attention-list">{#each snapshot.attention as item}<article class="attention-item"><AlertTriangle size={18} /><div><strong>{item.message}</strong><span>{humanize(item.entity_type)} · {item.entity_id} · {humanize(item.code)}</span>{#if item.remediation}<p>{item.remediation}</p>{/if}</div><StatusChip value={item.severity} /></article>{/each}</div>{/if}
            </section>

            <section class="section-block" aria-labelledby="readiness-title">
              <div class="section-heading"><div><p class="section-kicker">CURRENT TRUTH</p><h2 id="readiness-title">Readiness</h2></div><span class="as-of">As of {new Date(snapshot.generated_at).toLocaleTimeString()}</span></div>
              <div class="status-list">
                <div class="status-row"><span class:ok={snapshot.service.running} class="status-icon"><Activity size={19} /></span><div><strong>Daemon</strong><span>{snapshot.service.protocol} · {snapshot.service.installed ? humanize(snapshot.service.install_kind) : 'foreground or unmanaged'}</span></div><StatusChip value={snapshot.service.running ? 'running' : 'unavailable'} /></div>
                <div class="status-row"><span class:ok={snapshot.service.admin_secure} class="status-icon"><ShieldCheck size={19} /></span><div><strong>Admin session</strong><span>Loopback-only, bounded, authenticated observation</span></div><StatusChip value={snapshot.service.admin_secure ? 'secure' : 'blocked'} /></div>
                <div class="status-row"><span class:ok={snapshot.caches.length > 0 && snapshot.caches.every((cache) => cache.readiness === 'ready')} class="status-icon"><Database size={19} /></span><div><strong>Cache estate</strong><span>{snapshot.caches.length} caches · {snapshot.caches.reduce((total, cache) => total + cache.repository_count, 0)} repositories</span></div><StatusChip value={snapshot.caches.length === 0 ? 'unavailable' : snapshot.caches.every((cache) => cache.readiness === 'ready') ? 'ready' : 'degraded'} /></div>
              </div>
            </section>

            <section class="section-block" aria-labelledby="active-title">
              <div class="section-heading"><div><p class="section-kicker">EXECUTION</p><h2 id="active-title">Active work</h2></div><span class="count-badge">{activeJobs.length}</span></div>
              {#if activeJobs.length === 0}<div class="empty-inline"><Clock3 size={19} /><div><strong>No queued or running jobs</strong><span>The daemon is idle; terminal history remains in Jobs.</span></div></div>
              {:else}<div class="work-list">{#each activeJobs as job}<article><div class="work-icon"><RefreshCw size={17} class={job.status === 'running' ? 'spin' : ''} /></div><div><strong>{humanize(job.type)}</strong><span>{job.cache_ref || 'unscoped'} · {job.repo_id || 'service-wide'} · {job.completed || 0}/{job.steps || '—'}</span></div><StatusChip value={job.status} /></article>{/each}</div>{/if}
            </section>

            <section class="section-block" aria-labelledby="cache-summary-title">
              <div class="section-heading"><div><p class="section-kicker">TOPOLOGY & COVERAGE</p><h2 id="cache-summary-title">Managed caches</h2></div><button class="text-action" onclick={() => selectView('Caches')}>View topology<ChevronRight size={15} /></button></div>
              {#if snapshot.caches.length === 0}<div class="empty-state"><Database size={24} /><h3>No managed caches</h3><p>Enroll cache maintenance from the CLI before the console can observe repositories.</p></div>
              {:else}<div class="cache-summary-grid">{#each snapshot.caches as cache}<article class="cache-summary"><div class="card-heading"><div><span class="entity-icon"><Database size={17} /></span><div><strong>{cache.cache_ref}</strong><span>{cache.repository_count} repositories · schema {cache.schema_version || 'unknown'}</span></div></div><StatusChip value={cache.readiness} /></div><div class="cohort-strip">{#each ['head', 'tail', 'secondary', 'rag'] as lane}{@const observed = cache.repositories[0]?.coverage[lane as 'head' | 'tail' | 'secondary' | 'rag']}<div><span>{humanize(lane)}</span><strong>{observed ? humanize(observed.state) : 'No repo'}</strong></div>{/each}</div><button class="card-link" onclick={() => { active = 'Caches'; selectedCacheRef = cache.cache_ref; selectedRepoID = ''; updateLocation(); }}>Inspect cache<ChevronRight size={15} /></button></article>{/each}</div>{/if}
            </section>

            {#if snapshot.diagnostics.some((item) => !item.current)}<section class="section-block compact-section" aria-labelledby="recovered-title"><div class="section-heading"><div><p class="section-kicker">RECOVERY</p><h2 id="recovered-title">Recently recovered</h2></div></div><div class="recovered-list">{#each snapshot.diagnostics.filter((item) => !item.current).slice(0, 3) as item}<div><CheckCircle2 size={17} /><span><strong>{humanize(item.failure_class)}</strong>{item.entity_id}</span><time>{item.observed_at ? new Date(item.observed_at).toLocaleString() : 'Recovered'}</time></div>{/each}</div></section>{/if}
            <div class="actions"><button class="primary" onclick={refresh} disabled={loading}><RefreshCw size={16} class={loading ? 'spin' : ''} />Refresh snapshot</button><button class="secondary" onclick={() => selectView('Diagnostics')}><FolderCog size={16} />Open diagnostics</button></div>
            <p class="privacy"><ShieldCheck size={15} />Bound to localhost. Credentials, raw content, and absolute cache paths are never exposed.</p>
          {/if}

        {:else if active === 'Caches'}
          {#if selectedRepo}
            <div class="entity-header"><button class="back-link" onclick={closeRepository}><ArrowLeft size={16} />All caches</button><div class="intro section-intro"><p class="eyebrow">REPOSITORY</p><h1>{selectedRepo.display_name || selectedRepo.repo_id}</h1><p>{selectedCache?.cache_ref} · {selectedRepo.scopes?.join(', ') || 'cached observation'} · {humanize(selectedRepo.binding_state)}</p></div></div>
            <div class="tab-list" role="tablist" aria-label="Repository details">{#each repositoryTabs as tab}<button role="tab" aria-selected={repoTab === tab.value} class:active={repoTab === tab.value} onclick={() => selectRepositoryTab(tab.value)}>{tab.label}</button>{/each}</div>
            {#if repoTab === 'coverage'}
              <section class="repository-section" aria-labelledby="coverage-title"><div class="section-heading"><div><p class="section-kicker">INDEPENDENT LANES</p><h2 id="coverage-title">Corpus coverage</h2></div></div><div class="coverage-grid">{#each Object.entries(selectedRepo.coverage) as [name, lane]}<CoverageLaneCard {name} {lane} />{/each}</div></section>
              <section class="repository-section" aria-labelledby="execution-title"><div class="section-heading"><div><p class="section-kicker">SEPARATE FROM COVERAGE</p><h2 id="execution-title">Execution context</h2></div></div><div class="context-grid"><article><span>Active jobs</span><strong>{selectedRepo.execution.active_job_ids?.length || 0}</strong><p>{selectedRepo.execution.active_job_ids?.join(', ') || 'No current work'}</p></article><article><span>Contention</span><strong>{selectedRepo.execution.contention ? humanize(selectedRepo.execution.contention.state) : 'None'}</strong><p>{selectedRepo.execution.contention?.operation ? `Waiting on ${selectedRepo.execution.contention.operation}` : 'No writer contention observed'}</p></article><article><span>Scheduled retry</span><strong>{selectedRepo.execution.scheduled_retry ? humanize(selectedRepo.execution.scheduled_retry.stage) : 'None'}</strong><p>{selectedRepo.execution.scheduled_retry ? new Date(selectedRepo.execution.scheduled_retry.at).toLocaleString() : 'No backoff scheduled'}</p></article></div>{#if selectedRepo.execution.last_stage_errors?.length}<div class="historical-note"><History size={17} /><div><strong>Last stage error is historical context</strong><p>{selectedRepo.execution.last_stage_errors.map((item) => `${humanize(item.stage)}: ${humanize(item.failure_class)}`).join(' · ')}. It does not replace current lane truth.</p></div></div>{/if}</section>
            {:else if repoTab === 'collections'}
              <section class="repository-section" aria-labelledby="collections-title"><div class="section-heading"><div><p class="section-kicker">CORPUS</p><h2 id="collections-title">Collections and frontiers</h2></div></div>{#if selectedRepo.collections.length === 0}<div class="empty-state"><Layers3 size={24} /><h3>No collection observations</h3><p>The binding exists, but no counts or frontiers have been observed.</p></div>{:else}<div class="table-wrap"><table><caption class="sr-only">Cached collection counts and coverage</caption><thead><tr><th>Collection</th><th>Cached</th><th>Head freshness</th><th>Tail completeness</th></tr></thead><tbody>{#each selectedRepo.collections as collection}<tr><th scope="row">{humanize(collection.kind)}</th><td>{collection.count.toLocaleString()}</td><td><StatusChip value={collection.head.state} label={laneSummary('head', collection.head)} /></td><td><StatusChip value={collection.tail.state} label={laneSummary('tail', collection.tail)} /></td></tr>{/each}</tbody></table></div>{/if}<div class="secondary-summary"><div><span>Secondary total</span><strong>{selectedRepo.counts.secondary.total.toLocaleString()}</strong></div><div><span>Pending</span><strong>{selectedRepo.counts.secondary.pending.toLocaleString()}</strong></div><div><span>Deferred</span><strong>{selectedRepo.counts.secondary.deferred.toLocaleString()}</strong></div><div><span>Complete</span><strong>{selectedRepo.counts.secondary.complete.toLocaleString()}</strong></div></div></section>
            {:else if repoTab === 'search'}
              <section class="repository-section" aria-labelledby="search-title"><div class="section-heading"><div><p class="section-kicker">READINESS, NOT A QUERY</p><h2 id="search-title">Search status</h2></div></div><div class="search-readiness"><article><span class="large-icon"><Search size={24} /></span><div><strong>{selectedRepo.coverage.rag.state === 'current' ? 'Hybrid search ready' : 'Full-text remains available'}</strong><p>{selectedRepo.coverage.rag.state === 'current' ? 'The RAG namespace covers the current content generation.' : `RAG is ${humanize(selectedRepo.coverage.rag.state)}; semantic fallback must remain explicit.`}</p></div><StatusChip value={selectedRepo.coverage.rag.state} /></article></div><div class="coverage-grid two-up"><CoverageLaneCard name="projection" lane={selectedRepo.coverage.projection} /><CoverageLaneCard name="rag" lane={selectedRepo.coverage.rag} /></div><div class="state-panel neutral-panel"><ShieldCheck size={19} /><div><strong>Observation has no indexing side effects</strong><p>Opening this tab does not contact a provider, refresh GitCode, or repair an index.</p></div></div></section>
            {:else}
              <section class="repository-section" aria-labelledby="activity-title"><div class="section-heading"><div><p class="section-kicker">BOUNDED HISTORY</p><h2 id="activity-title">Repository activity</h2></div></div><div class="activity-columns"><div><h3>Recent sync events</h3>{#if selectedRepo.recent_sync_events.length === 0}<div class="empty-inline"><History size={18} /><div><strong>No retained sync events</strong><span>Coverage may still contain frontier evidence.</span></div></div>{:else}<div class="timeline">{#each selectedRepo.recent_sync_events as event}<article><span class="timeline-mark"></span><div><strong>{humanize(event.kind)}</strong><span>{event.zero_delta ? 'No content delta' : humanize(event.status)}</span></div><time>{new Date(event.completed_at).toLocaleString()}</time></article>{/each}</div>{/if}</div><div><h3>Scoped jobs</h3>{#if scopedJobs.length === 0}<div class="empty-inline"><Clock3 size={18} /><div><strong>No retained jobs</strong><span>No work history for this pair.</span></div></div>{:else}<div class="mini-job-list">{#each scopedJobs.slice(-8).reverse() as job}<div><span><strong>{humanize(job.type)}</strong>{new Date(job.updated_at).toLocaleString()}</span><StatusChip value={job.status} /></div>{/each}</div>{/if}</div></div></section>
            {/if}
          {:else}
            <div class="intro section-intro"><p class="eyebrow">TOPOLOGY</p><h1>Caches</h1><p>Cache identity, repository bindings, maintenance registration, and coverage—without absolute paths.</p></div>
            {#if snapshot.caches.length === 0}<div class="empty-state large-empty"><Database size={27} /><h2>No managed caches</h2><p>The daemon has no enrolled or primary cache to observe.</p></div>{:else}<div class="topology-list">{#each snapshot.caches as cache}<article class="topology-cache"><div class="topology-heading"><div><span class="entity-icon"><Database size={18} /></span><div><h2>{cache.cache_ref}</h2><p>{cache.storage_mode || 'managed'} · fingerprint {cache.path_fingerprint || 'not available'}</p></div></div><StatusChip value={cache.readiness} /></div><dl class="cache-metadata"><div><dt>Schema</dt><dd>{cache.schema_version || 'Unknown'}</dd></div><div><dt>WAL</dt><dd>{cache.wal_capable ? humanize(cache.journal_mode || 'capable') : 'Unavailable'}</dd></div><div><dt>Records</dt><dd>{cache.record_count.toLocaleString()}</dd></div><div><dt>Chunks</dt><dd>{cache.chunk_count.toLocaleString()}</dd></div></dl><div class="repository-list">{#if cache.repositories.length === 0}<div class="empty-inline"><Layers3 size={18} /><div><strong>No repository bindings</strong><span>The cache is open but contains no repositories.</span></div></div>{:else}{#each cache.repositories as repo}<button onclick={() => openRepository(cache, repo)}><span class="repo-main"><strong>{repo.display_name || repo.repo_id}</strong><small>{repo.counts.records.toLocaleString()} records · {repo.counts.chunks.toLocaleString()} chunks · {humanize(repo.binding_state)}</small></span><span class="repo-lanes"><StatusChip value={repo.coverage.head.state} label={`Head ${humanize(repo.coverage.head.state)}`} /><StatusChip value={repo.coverage.tail.state} label={`Tail ${humanize(repo.coverage.tail.state)}`} /><StatusChip value={repo.coverage.rag.state} label={`RAG ${humanize(repo.coverage.rag.state)}`} /></span><ChevronRight size={17} /></button>{/each}{/if}</div></article>{/each}</div>{/if}
          {/if}

        {:else if active === 'Jobs'}
          <div class="intro section-intro"><p class="eyebrow">EXECUTION</p><h1>Jobs</h1><p>Read-only active and terminal coordinator work. Supervision controls arrive next.</p></div>
          <div class="metric-strip"><div><span>Active</span><strong>{activeJobs.length}</strong></div><div><span>Terminal</span><strong>{snapshot.jobs.length - activeJobs.length}</strong></div><div><span>Retained</span><strong>{snapshot.jobs.length}</strong></div></div>
          {#if snapshot.jobs.length === 0}<div class="empty-state large-empty"><Activity size={27} /><h2>No retained jobs</h2><p>The coordinator has no active or terminal work.</p></div>{:else}<div class="table-wrap"><table><caption class="sr-only">Coordinator jobs</caption><thead><tr><th>Job</th><th>Scope</th><th>Status</th><th>Progress</th><th>Updated</th></tr></thead><tbody>{#each [...snapshot.jobs].reverse() as job}<tr><th scope="row"><span class="table-primary">{job.id}</span><small>{humanize(job.type)}</small></th><td><span class="table-primary">{job.repo_id || 'service-wide'}</span><small>{job.cache_ref || 'unscoped'}</small></td><td><StatusChip value={job.status} /></td><td>{job.completed || 0}/{job.steps || '—'}</td><td>{new Date(job.updated_at).toLocaleString()}</td></tr>{/each}</tbody></table></div>{/if}

        {:else if active === 'Maintenance'}
          <div class="intro section-intro"><p class="eyebrow">POLICY</p><h1>Maintenance</h1><p>What the daemon is configured to keep current, without mutation controls.</p></div>
          {#if snapshot.maintenance.length === 0}<div class="empty-state large-empty"><Wrench size={27} /><h2>No maintenance registrations</h2><p>Cache data can still be read, but no background policy is enrolled.</p></div>{:else}<div class="maintenance-grid">{#each snapshot.maintenance as item}<article><div class="card-heading"><div><span class="entity-icon"><Wrench size={17} /></span><div><strong>{item.repo_id}</strong><span>{item.cache_ref} · generation {item.generation}</span></div></div><StatusChip value={item.enabled ? item.state || 'ready' : 'disabled'} /></div><dl><div><dt>Sync</dt><dd>{item.policy.sync_enabled ? humanize(item.policy.sync_mode) : 'Off'}</dd></div><div><dt>Collections</dt><dd>{item.policy.collections?.map(humanize).join(', ') || 'None'}</dd></div><div><dt>RAG</dt><dd>{item.policy.rag_enabled ? item.policy.profile || 'Maintained' : 'Off'}</dd></div><div><dt>Head bound</dt><dd>{item.policy.head_max_pages || 'Default'} pages</dd></div><div><dt>Tail slice</dt><dd>{item.policy.tail_slice_pages || 'Default'} pages</dd></div><div><dt>Next reconcile</dt><dd>{item.next_reconcile_at ? new Date(item.next_reconcile_at).toLocaleString() : 'Not scheduled'}</dd></div></dl></article>{/each}</div>{/if}

        {:else}
          <div class="intro section-intro"><p class="eyebrow">GOVERNANCE & RECOVERY</p><h1>Diagnostics</h1><p>Typed failures, recovered state, exact remediation, and capability boundaries.</p></div>
          <div class="filter-bar" aria-label="Diagnostic state filter">{#each ['current', 'recovered', 'all'] as value}<button class:active={diagnosticsFilter === value} aria-pressed={diagnosticsFilter === value} onclick={() => selectDiagnosticFilter(value as 'current' | 'recovered' | 'all')}>{humanize(value)}</button>{/each}</div>
          <section class="repository-section" aria-labelledby="diagnostics-title"><div class="section-heading"><div><p class="section-kicker">TYPED FAILURES</p><h2 id="diagnostics-title">{humanize(diagnosticsFilter)} diagnostics</h2></div><span class="count-badge">{visibleDiagnostics.length}</span></div>{#if visibleDiagnostics.length === 0}<div class="empty-state"><CheckCircle2 size={24} /><h3>No {diagnosticsFilter} diagnostics</h3><p>No matching typed failure is retained.</p></div>{:else}<div class="diagnostic-list">{#each visibleDiagnostics as item}<article class:recovered={!item.current}><div class="diagnostic-heading"><div><span class="entity-icon"><AlertTriangle size={17} /></span><div><strong>{humanize(item.failure_class)}</strong><span>{humanize(item.entity_type)} · {item.entity_id}</span></div></div><StatusChip value={item.current ? item.severity : 'current'} label={item.current ? humanize(item.severity) : 'Recovered'} /></div><p>{item.message}</p>{#if item.remediation}<div class="remediation"><strong>Safe remediation</strong><span>{item.remediation}</span></div>{/if}<div class="command-row"><code>{cliHandoff(item)}</code><button onclick={() => void copyCommand(item)} aria-label={`Copy CLI handoff for ${humanize(item.failure_class)}`}><Clipboard size={15} />{copied === item.id ? 'Copied' : 'Copy CLI'}</button></div></article>{/each}</div>{/if}</section>
          <section class="repository-section" aria-labelledby="capabilities-title"><div class="section-heading"><div><p class="section-kicker">SAFETY CATALOG</p><h2 id="capabilities-title">Capabilities</h2></div><span class="count-badge">{snapshot.capabilities.length}</span></div>{#if snapshot.capabilities.length === 0}<div class="empty-state"><ShieldCheck size={24} /><h3>No capability catalog</h3><p>The daemon did not expose capability metadata.</p></div>{:else}<div class="table-wrap"><table><caption class="sr-only">Capability safety and availability</caption><thead><tr><th>Capability</th><th>Safety</th><th>UI</th><th>CLI</th><th>MCP</th></tr></thead><tbody>{#each snapshot.capabilities as capability}<tr><th scope="row"><span class="table-primary">{capability.id}</span><small>{capability.description}</small></th><td>{humanize(capability.safety_class)}</td><td><StatusChip value={capability.ui_enabled ? 'ready' : 'disabled'} label={capability.ui_enabled ? 'Available' : capability.ui_reason || 'Unavailable'} /></td><td>{capability.cli_enabled ? capability.cli_name || 'Available' : 'Unavailable'}</td><td>{capability.mcp_enabled ? capability.mcp_name || 'Available' : 'Unavailable'}</td></tr>{/each}</tbody></table></div>{/if}</section>
        {/if}
      {/if}
    </section>
  </main>
</div>
